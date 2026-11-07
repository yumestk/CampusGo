package application

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"github.com/yumestk/CampusGo/internal/contracts"
	"github.com/yumestk/CampusGo/internal/platform/fault"
)

type GrabStore interface {
	RegisterGrab(context.Context, contracts.Actor, string, contracts.GrabCommand, []byte) (contracts.GrabRequest, bool, error)
	ObserveGrab(context.Context, contracts.GrabRequest) (contracts.GrabResult, error)
	CheckGrab(context.Context, contracts.Actor, contracts.GrabCommand, int64) (string, error)
	FinishGrab(context.Context, contracts.GrabRequest, string, string) (contracts.GrabResult, error)
	CommitGrab(context.Context, contracts.Actor, contracts.GrabRequest) (contracts.GrabResult, error)
	GrabResult(context.Context, contracts.Actor, string, int64) (contracts.GrabResult, error)
}
type GrabProjection interface {
	SyncProjection(context.Context, int64) error
	AcquireGrab(context.Context, contracts.GrabRequest) (int64, error)
	PublishGrabProjection(context.Context, int64)
}
type GrabAdmission interface {
	Admit(context.Context, int64, int64, bool) (func(), error)
}
type Grab struct {
	Store      GrabStore
	Projection GrabProjection
	Admission  GrabAdmission
}

func (g *Grab) Grab(ctx context.Context, u contracts.Actor, key string, c contracts.GrabCommand) (contracts.GrabResult, error) {
	if !contracts.ValidRequestKey(key) || c.ErrandID <= 0 || c.ExpectedRound < 0 || c.ExpectedRound > 2147483647 {
		return contracts.GrabResult{}, fault.New("INVALID_REQUEST", 400)
	}
	release, err := g.Admission.Admit(ctx, u.ID, c.ErrandID, false)
	if err != nil {
		return contracts.GrabResult{}, err
	}
	defer release()
	return g.grab(ctx, u, key, c)
}
func (g *Grab) grab(ctx context.Context, u contracts.Actor, key string, c contracts.GrabCommand) (contracts.GrabResult, error) {
	if !contracts.ValidRequestKey(key) || c.ErrandID <= 0 || c.ExpectedRound < 0 || c.ExpectedRound > 2147483647 {
		return contracts.GrabResult{}, fault.New("INVALID_REQUEST", 400)
	}
	data, _ := json.Marshal(c)
	hash := sha256.Sum256(data)
	r, owner, err := g.Store.RegisterGrab(ctx, u, key, c, hash[:])
	if err != nil {
		return contracts.GrabResult{}, err
	}
	if !owner {
		return g.Store.ObserveGrab(ctx, r)
	}
	// Business checks are authoritative SQL checks; Redis can only pre-admit execution.
	reason, err := g.Store.CheckGrab(ctx, u, c, r.BaseVersion)
	if err != nil {
		return r.Result(), fault.Dependency(err)
	}
	if reason != "" {
		return g.Store.FinishGrab(ctx, r, "REJECTED", reason)
	}
	if err = g.Projection.SyncProjection(ctx, c.ErrandID); err != nil {
		return r.Result(), &fault.Error{Code: "DEPENDENCY_UNAVAILABLE", Status: 503, Cause: err}
	}
	acquired, err := g.Projection.AcquireGrab(ctx, r)
	if err != nil {
		return r.Result(), &fault.Error{Code: "DEPENDENCY_UNAVAILABLE", Status: 503, Cause: err}
	}
	switch acquired {
	case 0:
	case 1, 4:
		return r.Result(), nil
	case 2, 3:
		reason, e := g.Store.CheckGrab(ctx, u, c, r.BaseVersion)
		if e != nil {
			return r.Result(), fault.Dependency(e)
		}
		if reason != "" {
			return g.Store.FinishGrab(ctx, r, "REJECTED", reason)
		}
		return r.Result(), nil // A Redis-only busy state is provisional, never proof of a SQL loser.
	case 5, 6:
		return g.Store.FinishGrab(ctx, r, "EXPIRED", "REQUEST_EXPIRED")
	default:
		return r.Result(), fault.New("INTERNAL_ERROR", 500)
	}
	result, err := g.Store.CommitGrab(ctx, u, r)
	if err != nil {
		return r.Result(), err
	} // COMMIT ambiguity must never release the reservation.
	// Repair is already durable in the business transaction. These are latency optimizations.
	g.Projection.PublishGrabProjection(ctx, c.ErrandID)
	return result, nil
}

func (g *Grab) Result(ctx context.Context, u contracts.Actor, key string, id int64) (contracts.GrabResult, error) {
	if !contracts.ValidRequestKey(key) || id <= 0 {
		return contracts.GrabResult{}, fault.New("INVALID_REQUEST", 400)
	}
	release, err := g.Admission.Admit(ctx, u.ID, id, true)
	if err != nil {
		return contracts.GrabResult{}, err
	}
	defer release()
	return g.Store.GrabResult(ctx, u, key, id)
}
