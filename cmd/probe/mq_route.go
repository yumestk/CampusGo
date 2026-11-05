package main

import (
	"context"
	"fmt"
	rmq "github.com/apache/rocketmq-clients/golang/v5"
	"github.com/apache/rocketmq-clients/golang/v5/credentials"
	"os"
	"time"
)

// An open proxy port and mqadmin topic creation do not prove the proxy's route
// cache has converged. Check the same SDK startup path used by business clients.
func probeMQRoute(ctx context.Context, _ string) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	var last error
	for ctx.Err() == nil {
		p, err := rmq.NewProducer(&rmq.Config{Endpoint: os.Getenv("CAMPUSGO_MQ_ENDPOINT"), Credentials: &credentials.SessionCredentials{}}, rmq.WithTopics("campusgo-funds", "campusgo-confirm-timeout", "campusgo-auto-settle"))
		if err != nil {
			return err
		}
		last = p.Start() // SDK stops a failed startup itself; never send probe funds.
		if last == nil {
			return p.GracefulStop()
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("business MQ routes unavailable: %w", last)
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("business MQ routes unavailable: %w", last)
}
