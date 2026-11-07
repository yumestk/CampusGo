package application

import (
	"context"
	"github.com/yumestk/CampusGo/internal/contracts"
	"time"
)

type MessageStore interface {
	ClaimMessages(context.Context, int) ([]contracts.TimeoutMessage, error)
	CompleteMessage(context.Context, contracts.TimeoutMessage, error) (bool, error)
	DeadMessages(context.Context) ([]contracts.TimeoutMessage, error)
	ReplayMessage(context.Context, int64, string) (bool, error)
}
type Messages struct{ Store MessageStore }

func (g *Messages) DeadMessages(ctx context.Context) ([]contracts.TimeoutMessage, error) {
	return g.Store.DeadMessages(ctx)
}
func (g *Messages) ReplayMessage(ctx context.Context, id int64, reason string) (bool, error) {
	return g.Store.ReplayMessage(ctx, id, reason)
}
func (g *Messages) DispatchMessages(ctx context.Context, send func(context.Context, contracts.TimeoutMessage) error) error {
	messages, err := g.Store.ClaimMessages(ctx, 10)
	if err != nil {
		return err
	}
	for _, m := range messages {
		sendCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		cause := send(sendCtx, m)
		cancel()
		if _, err = g.Store.CompleteMessage(ctx, m, cause); err != nil {
			return err
		}
	}
	return nil
}
