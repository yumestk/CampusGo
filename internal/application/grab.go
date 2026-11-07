package application

import (
	"context"
	"crypto/sha256"
	"encoding/json"

	"github.com/yumestk/CampusGo/internal/contracts"
	fault "github.com/yumestk/CampusGo/internal/failure"
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
	store      GrabStore
	projection GrabProjection
	admission  GrabAdmission
}

func (g *Grab) Grab(ctx context.Context, u contracts.Actor, key string, c contracts.GrabCommand) (contracts.GrabResult, error) {
	if !contracts.ValidRequestKey(key) || c.ErrandID <= 0 || c.ExpectedRound < 0 || c.ExpectedRound > 2147483647 {
		return contracts.GrabResult{}, fault.New("INVALID_REQUEST", 400)
	}
	release, err := g.admission.Admit(ctx, u.ID, c.ErrandID, false)
	if err != nil {
		return contracts.GrabResult{}, err
	}
	defer release()
	return g.grab(ctx, u, key, c)
}
func (g *Grab) grab(ctx context.Context, u contracts.Actor, key string, c contracts.GrabCommand) (contracts.GrabResult, error) {
	data, _ := json.Marshal(c)
	hash := sha256.Sum256(data)
	r, owner, err := g.store.RegisterGrab(ctx, u, key, c, hash[:])
	if err != nil {
		return contracts.GrabResult{}, err
	}
	if !owner {
		return g.store.ObserveGrab(ctx, r)
	}
	// Business checks are authoritative SQL checks; Redis can only pre-admit execution.
	reason, err := g.store.CheckGrab(ctx, u, c, r.BaseVersion)
	if err != nil {
		return r.Result(), err
	}
	if reason != "" {
		return g.store.FinishGrab(ctx, r, "REJECTED", reason)
	}
	if err = g.projection.SyncProjection(ctx, c.ErrandID); err != nil {
		return r.Result(), &fault.Error{Code: "DEPENDENCY_UNAVAILABLE", Status: 503, Cause: err}
	}
	acquired, err := g.projection.AcquireGrab(ctx, r)
	if err != nil {
		return r.Result(), &fault.Error{Code: "DEPENDENCY_UNAVAILABLE", Status: 503, Cause: err}
	}
	switch acquired {
	case 0:
	case 1, 4:
		return r.Result(), nil
	case 2, 3:
		reason, e := g.store.CheckGrab(ctx, u, c, r.BaseVersion)
		if e != nil {
			return r.Result(), e
		}
		if reason != "" {
			return g.store.FinishGrab(ctx, r, "REJECTED", reason)
		}
		return r.Result(), nil // A Redis-only busy state is provisional, never proof of a SQL loser.
	case 5, 6:
		return g.store.FinishGrab(ctx, r, "EXPIRED", "REQUEST_EXPIRED")
	default:
		return r.Result(), fault.New("INTERNAL_ERROR", 500)
	}
	result, err := g.store.CommitGrab(ctx, u, r)
	if err != nil {
		return r.Result(), err
	} // COMMIT ambiguity must never release the reservation.
	// Repair is already durable in the business transaction. These are latency optimizations.
	g.projection.PublishGrabProjection(ctx, c.ErrandID)
	return result, nil
}

func (g *Grab) Result(ctx context.Context, u contracts.Actor, key string, id int64) (contracts.GrabResult, error) {
	if !contracts.ValidRequestKey(key) || id <= 0 {
		return contracts.GrabResult{}, fault.New("INVALID_REQUEST", 400)
	}
	release, err := g.admission.Admit(ctx, u.ID, id, true)
	if err != nil {
		return contracts.GrabResult{}, err
	}
	defer release()
	return g.store.GrabResult(ctx, u, key, id)
}

// NewGrab requires each production capability explicitly; there is no permissive
// admission fallback. Core protocol fixtures inject their own admission policy.
func NewGrab(store GrabStore, projection GrabProjection, admission GrabAdmission) (*Grab, error) {
	if store == nil || projection == nil || admission == nil {
		return nil, fault.New("DEPENDENCY_UNAVAILABLE", 503)
	}
	return &Grab{store: store, projection: projection, admission: admission}, nil
}

type AdmissionFunc func(context.Context, int64, int64, bool) (func(), error)

func (f AdmissionFunc) Admit(ctx context.Context, user, task int64, query bool) (func(), error) {
	return f(ctx, user, task, query)
}
