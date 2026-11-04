package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"

	rmq "github.com/apache/rocketmq-clients/golang/v5"
	"github.com/apache/rocketmq-clients/golang/v5/credentials"
	v2 "github.com/apache/rocketmq-clients/golang/v5/protocol/v2"
	"github.com/yumestk/CampusGo/internal/platform/database"
)

func probeMQ(ctx context.Context, runID string) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	db, err := database.Open(ctx, "campusgo_verification")
	if err != nil {
		return err
	}
	defer db.Close()
	const txTopic, delayTopic = "campusgo-d0-transaction", "campusgo-d0-delay"
	endpoint := os.Getenv("CAMPUSGO_MQ_ENDPOINT")
	ongoingChecked := make(chan struct{}, 1)
	var checks atomic.Int32
	producer, err := rmq.NewProducer(&rmq.Config{Endpoint: endpoint, Credentials: &credentials.SessionCredentials{}},
		rmq.WithTopics(txTopic, delayTopic),
		rmq.WithTransactionChecker(&rmq.TransactionChecker{Check: func(message *rmq.MessageView) rmq.TransactionResolution {
			keys := message.GetKeys()
			if len(keys) != 1 || !strings.HasPrefix(keys[0], runID+"/") {
				return rmq.UNKNOWN
			}
			checkCtx, done := context.WithTimeout(context.Background(), 2*time.Second)
			defer done()
			var state string
			if err := db.QueryRowContext(checkCtx, "SELECT state FROM d0_protocol_probe WHERE id=?", keys[0]).Scan(&state); err != nil {
				if keys[0] == runID+"/ongoing" && err == sql.ErrNoRows {
					select {
					case ongoingChecked <- struct{}{}:
					default:
					}
				}
				return rmq.UNKNOWN
			}
			if state == "committed" {
				checks.Add(1)
				return rmq.COMMIT
			}
			return rmq.UNKNOWN
		}}))
	if err != nil {
		return err
	}
	if err := producer.Start(); err != nil {
		return err
	}
	defer producer.GracefulStop()
	consumer, err := rmq.NewSimpleConsumer(&rmq.Config{
		Endpoint: endpoint, ConsumerGroup: "campusgo-d0-tx-consumer", Credentials: &credentials.SessionCredentials{},
	}, rmq.WithSimpleAwaitDuration(6*time.Second), rmq.WithSimpleSubscriptionExpressions(map[string]*rmq.FilterExpression{
		txTopic: rmq.NewFilterExpression(runID), delayTopic: rmq.NewFilterExpression(runID),
	}))
	if err != nil {
		return err
	}
	if err := consumer.Start(); err != nil {
		return err
	}
	defer consumer.GracefulStop()

	for _, kind := range []string{"commit", "rollback", "checked", "ongoing"} {
		key := runID + "/" + kind
		message := &rmq.Message{Topic: txTopic, Body: []byte(key)}
		message.SetKeys(key)
		message.SetTag(runID)
		transaction := producer.BeginTransaction()
		if _, err := producer.SendWithTransaction(ctx, message, transaction); err != nil {
			return err
		}
		if kind == "commit" || kind == "checked" {
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO d0_protocol_probe (id,state) VALUES (?,'committed')", key); err != nil {
				tx.Rollback()
				return err
			}
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("probe SQL commit unknown for %s: %w", key, err)
			}
		}
		switch kind {
		case "commit":
			if err := transaction.Commit(); err != nil {
				return err
			}
		case "rollback":
			if err := transaction.RollBack(); err != nil {
				return err
			}
		}
	}
	select {
	case <-ongoingChecked:
		if _, err := db.ExecContext(ctx, "INSERT INTO d0_protocol_probe (id,state) VALUES (?,'committed')", runID+"/ongoing"); err != nil {
			return err
		}
	case <-ctx.Done():
		return fmt.Errorf("no UNKNOWN callback for in-progress transaction: %w", ctx.Err())
	}
	deliveryAt := time.Now().Add(3 * time.Second)
	delay := &rmq.Message{Topic: delayTopic, Body: []byte(runID + "/delay")}
	delay.SetKeys(runID + "/delay")
	delay.SetTag(runID)
	delay.SetDelayTimestamp(deliveryAt)
	if _, err := producer.Send(ctx, delay); err != nil {
		return err
	}
	seen := make(map[string]bool)
	var observationEnd time.Time
	for {
		if !observationEnd.IsZero() && time.Now().After(observationEnd) {
			break
		}
		messages, err := consumer.Receive(ctx, 16, 20*time.Second)
		if err != nil {
			var status *rmq.ErrRpcStatus
			if !errors.As(err, &status) || status.Code != int32(v2.Code_MESSAGE_NOT_FOUND) {
				return err
			}
		}
		for _, message := range messages {
			key := string(message.GetBody())
			if key == runID+"/rollback" {
				return fmt.Errorf("rolled-back message became visible")
			}
			if !strings.HasPrefix(key, runID+"/") {
				return fmt.Errorf("unexpected probe message")
			}
			if key == runID+"/delay" && time.Now().Before(deliveryAt) {
				return fmt.Errorf("delayed message arrived early")
			}
			if err := consumer.Ack(ctx, message); err != nil {
				return err
			}
			seen[key] = true
		}
		if len(seen) == 4 && observationEnd.IsZero() {
			observationEnd = time.Now().Add(5 * time.Second)
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("observed %d/4 expected messages: %w", len(seen), err)
		}
	}
	if checks.Load() < 2 {
		return fmt.Errorf("durable transaction callbacks=%d, want at least 2", checks.Load())
	}
	_, err = db.ExecContext(ctx, "DELETE FROM d0_protocol_probe WHERE id LIKE ?", runID+"/%")
	return err
}
