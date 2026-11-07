package fundmq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	rmq "github.com/apache/rocketmq-clients/golang/v5"
	"github.com/apache/rocketmq-clients/golang/v5/credentials"
	v2 "github.com/apache/rocketmq-clients/golang/v5/protocol/v2"

	"github.com/yumestk/CampusGo/internal/contracts"
)

const Topic = "campusgo-funds"

type Client struct {
	mu        sync.RWMutex
	producer  rmq.Producer
	restart   chan struct{}
	Namespace string
}

func (c *Client) Send(ctx context.Context, e contracts.FundEvent) (func(bool) error, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	p := c.producer
	if p == nil {
		return nil, fmt.Errorf("fund producer unavailable")
	}
	body, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	m := &rmq.Message{Topic: Topic, Body: body}
	m.SetKeys(c.Namespace + ":" + e.RequestID)
	m.SetTag(c.Namespace)
	restart := c.restart
	signalRestart := func() {
		select {
		case restart <- struct{}{}:
		default:
		}
	}
	tx := p.BeginTransaction()
	if _, err = p.SendWithTransaction(ctx, m, tx); err != nil {
		signalRestart()
		return nil, err
	}
	return func(commit bool) error {
		var e error
		if commit {
			e = tx.Commit()
		} else {
			e = tx.RollBack()
		}
		if e != nil {
			signalRestart()
		}
		return e
	}, nil
}
func (c *Client) Run(ctx context.Context, f FundHandler, endpoint string, consume bool, onError func(error)) {
	for ctx.Err() == nil {
		if err := c.session(ctx, f, endpoint, consume, onError); err != nil && ctx.Err() == nil {
			onError(err)
		}
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
func (c *Client) session(ctx context.Context, f FundHandler, endpoint string, consume bool, onError func(error)) error {
	p, err := rmq.NewProducer(&rmq.Config{Endpoint: endpoint, Credentials: &credentials.SessionCredentials{}}, rmq.WithTopics(Topic), rmq.WithTransactionChecker(&rmq.TransactionChecker{Check: func(m *rmq.MessageView) rmq.TransactionResolution {
		if m.GetTag() == nil || *m.GetTag() != c.Namespace {
			return rmq.UNKNOWN
		}
		var e contracts.FundEvent
		if json.Unmarshal(m.GetBody(), &e) != nil {
			return rmq.UNKNOWN
		}
		keys := m.GetKeys()
		if len(keys) != 1 || keys[0] != c.Namespace+":"+e.RequestID {
			return rmq.UNKNOWN
		}
		check, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		switch f.Check(check, e) {
		case "COMMIT":
			return rmq.COMMIT
		case "ROLLBACK":
			return rmq.ROLLBACK
		}
		return rmq.UNKNOWN
	}}))
	if err != nil {
		return err
	}
	if err = p.Start(); err != nil {
		return err
	}
	c.mu.Lock()
	restart := make(chan struct{}, 1)
	c.restart = restart
	c.producer = p
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.producer = nil; c.mu.Unlock(); p.GracefulStop() }()
	if !consume {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-restart:
			return fmt.Errorf("fund producer session requires registration recovery")
		}
	}
	return consumeSession(ctx, f, f, endpoint, "campusgo-funds-"+c.Namespace, c.Namespace, restart, nil, onError)
}

// RunPush has no producer and never gates funds.Send availability. A broken
// realtime subscription must not withdraw the business transaction producer.
func RunPush(ctx context.Context, f FundChecker, endpoint, namespace string, push func(context.Context, contracts.FundEvent) error, onError func(error)) {
	for ctx.Err() == nil {
		if err := consumeSession(ctx, f, nil, endpoint, "campusgo-funds-push-"+namespace, namespace, nil, push, onError); err != nil && ctx.Err() == nil {
			onError(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}
func consumeSession(ctx context.Context, f FundChecker, receiver FundReceiver, endpoint, group, namespace string, restart <-chan struct{}, push func(context.Context, contracts.FundEvent) error, onError func(error)) error {
	consumer, err := rmq.NewSimpleConsumer(&rmq.Config{Endpoint: endpoint, ConsumerGroup: group, Credentials: &credentials.SessionCredentials{}}, rmq.WithSimpleAwaitDuration(6*time.Second), rmq.WithSimpleSubscriptionExpressions(map[string]*rmq.FilterExpression{Topic: rmq.NewFilterExpression(namespace)}))
	if err != nil {
		return err
	}
	if err = consumer.Start(); err != nil {
		return err
	}
	defer consumer.GracefulStop()
	for ctx.Err() == nil {
		select {
		case <-restart:
			return fmt.Errorf("fund producer session requires registration recovery")
		default:
		}
		batch, cancel := context.WithTimeout(ctx, 12*time.Second)
		messages, e := consumer.Receive(batch, 8, 20*time.Second)
		cancel()
		if e != nil {
			var status *rmq.ErrRpcStatus
			if errors.As(e, &status) && status.Code == int32(v2.Code_MESSAGE_NOT_FOUND) {
				continue
			}
			return e
		}
		for _, m := range messages {
			work, cancel := context.WithTimeout(ctx, 5*time.Second)
			keys := m.GetKeys()
			var event contracts.FundEvent
			if (m.GetTag() == nil || *m.GetTag() != namespace) || json.Unmarshal(m.GetBody(), &event) != nil || len(keys) != 1 || keys[0] != namespace+":"+event.RequestID {
				e = consumer.Ack(work, m)
			} else {
				if push != nil {
					if f.Check(work, event) == "COMMIT" {
						if pushErr := push(work, event); pushErr != nil {
							onError(pushErr)
						}
					}
					e = nil // Realtime never consumes the durable receipt or retries delivery.
				} else {
					_, e = receiver.Receive(work, m.GetBody())
				}
				if e == nil {
					e = consumer.Ack(work, m)
				}
			}
			cancel()
			if e != nil && ctx.Err() == nil {
				onError(e)
			}
		}
	}
	return ctx.Err()
}

type FundChecker interface {
	Check(context.Context, contracts.FundEvent) string
}
type FundReceiver interface {
	Receive(context.Context, []byte) (string, error)
}
type FundHandler interface {
	FundChecker
	FundReceiver
}
