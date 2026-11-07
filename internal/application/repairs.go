package application

import (
	"context"
	"fmt"
	"github.com/yumestk/CampusGo/internal/contracts"
	"time"
)

type RepairStore interface {
	ExpiredGrabs(context.Context, int) ([]contracts.GrabRequest, error)
	FinishGrab(context.Context, contracts.GrabRequest, string, string) (contracts.GrabResult, error)
	ClaimRepairs(context.Context, int) ([]contracts.RepairTask, error)
	ApplyRepair(context.Context, contracts.RepairTask) error
	CompleteRepair(context.Context, contracts.RepairTask, error) (bool, error)
	ReconcileBatch(context.Context, int64, int) (int64, error)
	DeadRepairs(context.Context) ([]contracts.RepairTask, error)
	ReplayRepair(context.Context, int64, string) (bool, error)
}
type Repairs struct{ Store RepairStore }

func (g *Repairs) DeadRepairs(ctx context.Context) ([]contracts.RepairTask, error) {
	return g.Store.DeadRepairs(ctx)
}
func (g *Repairs) ReplayRepair(ctx context.Context, id int64, reason string) (bool, error) {
	return g.Store.ReplayRepair(ctx, id, reason)
}
func (g *Repairs) RecoverBatch(ctx context.Context, limit int) error {
	if limit < 1 || limit > 100 {
		return fmt.Errorf("recovery limit must be in [1,100]")
	}
	expired, err := g.Store.ExpiredGrabs(ctx, limit)
	if err != nil {
		return err
	}
	for _, r := range expired {
		if _, err = g.Store.FinishGrab(ctx, r, "EXPIRED", "REQUEST_EXPIRED"); err != nil {
			return err
		}
	}
	tasks, err := g.Store.ClaimRepairs(ctx, limit)
	if err != nil {
		return err
	}
	for _, r := range tasks {
		cause := g.Store.ApplyRepair(ctx, r)
		if _, err = g.Store.CompleteRepair(ctx, r, cause); err != nil {
			return err
		}
	}
	return nil
}
func (g *Repairs) RunRecovery(ctx context.Context, onError func(error)) {
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	var cursor int64
	cycle := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		batch, cancel := context.WithTimeout(ctx, 10*time.Second)
		if err := g.RecoverBatch(batch, 50); err != nil {
			onError(err)
		}
		if cycle%10 == 0 {
			next, err := g.Store.ReconcileBatch(batch, cursor, 50)
			if err != nil {
				onError(err)
			} else {
				cursor = next
			}
		}
		cancel()
		cycle++
	}
}
