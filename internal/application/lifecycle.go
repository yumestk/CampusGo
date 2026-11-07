package application

import (
	"context"
	"github.com/yumestk/CampusGo/internal/contracts"
	"github.com/yumestk/CampusGo/internal/platform/fault"
)

type TaskTransitions interface {
	ApplyConfirm(context.Context, contracts.Actor, int64, int) (contracts.Card, error)
	ApplyFulfill(context.Context, contracts.Actor, int64, int, string) (contracts.Card, error)
}
type Lifecycle struct{ Store TaskTransitions }

func (g *Lifecycle) Confirm(ctx context.Context, u contracts.Actor, id int64, round int) (contracts.Card, error) {
	if id <= 0 || round < 0 || round > 2147483647 {
		return contracts.Card{}, fault.New("INVALID_REQUEST", 400)
	}
	return g.Store.ApplyConfirm(ctx, u, id, round)
}
func (g *Lifecycle) Fulfill(ctx context.Context, u contracts.Actor, id int64, round int, action string) (contracts.Card, error) {
	if id <= 0 || round < 0 || round > 2147483647 || (action != "PICK_UP" && action != "DELIVER" && action != "DISPUTE" && action != "CANCEL_DRAFT") {
		return contracts.Card{}, fault.New("INVALID_REQUEST", 400)
	}
	return g.Store.ApplyFulfill(ctx, u, id, round, action)
}
