package mysql

import (
	"context"
	"database/sql"
	"github.com/yumestk/CampusGo/internal/application"
	"github.com/yumestk/CampusGo/internal/auth"
)

func (g *TaskStore) Grab(ctx context.Context, u auth.User, key string, c GrabCommand) (GrabResult, error) {
	return (&application.Grab{Store: g, Projection: g, Admission: g}).Grab(ctx, u, key, c)
}
func (g *TaskStore) grab(ctx context.Context, u auth.User, key string, c GrabCommand, hook func(string)) (GrabResult, error) {
	copy := *g
	copy.grabHook = hook
	copy.Limits = nil
	return copy.Grab(ctx, u, key, c)
}
func (g *TaskStore) Result(ctx context.Context, u auth.User, key string, id int64) (GrabResult, error) {
	return (&application.Grab{Store: g, Projection: g, Admission: g}).Result(ctx, u, key, id)
}
func (p *PublishStore) Publish(ctx context.Context, u auth.User, key string, c PublishCommand) (PublishResult, error) {
	return (&application.Publisher{Store: p}).Publish(ctx, u, key, c)
}
func (p *PublishStore) publish(ctx context.Context, u auth.User, key string, c PublishCommand, before, commit func(*sql.Tx) error) (PublishResult, error) {
	copy := *p
	copy.beforeCommit = before
	copy.commit = commit
	return copy.Publish(ctx, u, key, c)
}
func (g *TaskStore) HandleTimeout(ctx context.Context, id int64, round int) (string, error) {
	return (&application.Timeouts{Store: g, Candidates: g}).HandleTimeout(ctx, id, round)
}
func (g *TaskStore) handleTimeout(ctx context.Context, id int64, round int, hook func(string)) (string, error) {
	copy := *g
	copy.timeoutHook = hook
	return copy.HandleTimeout(ctx, id, round)
}
func (g *TaskStore) ScanDue(ctx context.Context, limit int) error {
	return (&application.Timeouts{Store: g, Candidates: g}).ScanDue(ctx, limit)
}
func (g *TaskStore) ConsumeTimeout(ctx context.Context, body []byte) (string, error) {
	return (&application.Timeouts{Store: g, Candidates: g}).ConsumeTimeout(ctx, body)
}

func (g *TaskStore) Confirm(ctx context.Context, u auth.User, id int64, round int) (Card, error) {
	return (&application.Lifecycle{Store: g}).Confirm(ctx, u, id, round)
}
func (g *TaskStore) Fulfill(ctx context.Context, u auth.User, id int64, round int, action string) (Card, error) {
	return (&application.Lifecycle{Store: g}).Fulfill(ctx, u, id, round, action)
}

func (g *TaskStore) CreditSelf(ctx context.Context, u auth.User) (CreditProfile, error) {
	return (&CreditQuery{DB: g.DB, Redis: g.Redis, Namespace: g.Namespace}).CreditSelf(ctx, u)
}
func (g *TaskStore) CreditRanking(ctx context.Context, u auth.User, campus int64, limit int) ([]CreditRank, error) {
	return (&CreditQuery{DB: g.DB, Redis: g.Redis, Namespace: g.Namespace}).CreditRanking(ctx, u, campus, limit)
}
func (g *TaskStore) calibrateUser(ctx context.Context, id int64) error {
	return (&CreditStore{DB: g.DB}).calibrateUser(ctx, id)
}
func (g *TaskStore) recalibrate(ctx context.Context, id int64, due bool) error {
	return (&CreditStore{DB: g.DB}).recalibrate(ctx, id, due)
}
func (g *TaskStore) CalibrateDueCredit(ctx context.Context) error {
	return (&CreditStore{DB: g.DB}).CalibrateDueCredit(ctx)
}

func (g *TaskStore) RecoverBatch(ctx context.Context, limit int) error {
	return (&application.Repairs{Store: g}).RecoverBatch(ctx, limit)
}
func (g *TaskStore) repair(ctx context.Context, r RepairTask) error { return g.ApplyRepair(ctx, r) }
func (g *TaskStore) DispatchMessages(ctx context.Context, send func(context.Context, TimeoutMessage) error) error {
	return (&application.Messages{Store: g}).DispatchMessages(ctx, send)
}
