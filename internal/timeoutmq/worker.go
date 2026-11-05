package timeoutmq

import (
	"context"
	"errors"
	"time"

	rmq "github.com/apache/rocketmq-clients/golang/v5"
	"github.com/apache/rocketmq-clients/golang/v5/credentials"
	v2 "github.com/apache/rocketmq-clients/golang/v5/protocol/v2"
	"github.com/yumestk/CampusGo/internal/errand"
)

const Topic = "campusgo-confirm-timeout"

// SQL scans run independently in the worker. Failure to connect or publish never
// disables that recovery path. Group and tag isolate live and verification data.
func Run(ctx context.Context, g *errand.Grabber, endpoint string, onError func(error)) {
	for ctx.Err() == nil {
		if err := session(ctx, g, endpoint, onError); err != nil && ctx.Err() == nil {
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
func session(ctx context.Context, g *errand.Grabber, endpoint string, onError func(error)) error {
	producer, err := rmq.NewProducer(&rmq.Config{Endpoint: endpoint, Credentials: &credentials.SessionCredentials{}}, rmq.WithTopics(Topic))
	if err != nil {
		return err
	}
	if err = producer.Start(); err != nil {
		return err
	}
	defer producer.GracefulStop()
	consumer, err := rmq.NewSimpleConsumer(&rmq.Config{Endpoint: endpoint, ConsumerGroup: "campusgo-timeout-" + g.Namespace, Credentials: &credentials.SessionCredentials{}}, rmq.WithSimpleAwaitDuration(6*time.Second), rmq.WithSimpleSubscriptionExpressions(map[string]*rmq.FilterExpression{Topic: rmq.NewFilterExpression(g.Namespace)}))
	if err != nil {
		return err
	}
	if err = consumer.Start(); err != nil {
		return err
	}
	defer consumer.GracefulStop()
	sendDone := make(chan struct{})
	sendCtx, stop := context.WithCancel(ctx)
	defer func() { stop(); <-sendDone }()
	go func() {
		defer close(sendDone)
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-sendCtx.Done():
				return
			case <-tick.C:
			}
			err := g.DispatchMessages(sendCtx, func(c context.Context, m errand.TimeoutMessage) error {
				message := &rmq.Message{Topic: Topic, Body: m.Payload}
				message.SetKeys(m.Key)
				message.SetTag(g.Namespace)
				// Past-due retries use a near-future timer; SQL always rechecks its own deadline.
				at := m.DeliverAt
				if at.Before(time.Now().Add(time.Second)) {
					at = time.Now().Add(time.Second)
				}
				message.SetDelayTimestamp(at)
				_, e := producer.Send(c, message)
				return e
			})
			if err != nil && sendCtx.Err() == nil {
				onError(err)
			}
		}
	}()
	for ctx.Err() == nil {
		batch, cancel := context.WithTimeout(ctx, 12*time.Second)
		messages, e := consumer.Receive(batch, 8, 20*time.Second)
		cancel()
		if e != nil {
			var status *rmq.ErrRpcStatus
			if errors.As(e, &status) && status.Code == int32(v2.Code_MESSAGE_NOT_FOUND) {
				continue
			}
			if ctx.Err() == nil {
				onError(e)
			}
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
			case <-timer.C:
			}
			continue
		}
		for _, message := range messages {
			work, cancel := context.WithTimeout(ctx, 5*time.Second)
			_, e = g.ConsumeTimeout(work, message.GetBody())
			if e == nil {
				e = consumer.Ack(work, message)
			}
			cancel()
			if e != nil && ctx.Err() == nil {
				onError(e)
			}
		}
	}
	return ctx.Err()
}
