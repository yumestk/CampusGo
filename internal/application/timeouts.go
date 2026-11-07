package application

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/yumestk/CampusGo/internal/contracts"
	"io"
	"strconv"
	"time"
)

type TimeoutStore interface {
	TimeoutSnapshot(context.Context, int64) (contracts.Card, bool, error)
	TimeoutCycle(context.Context, int64) (int, error)
	TimeoutStep(context.Context, contracts.Card, *contracts.Candidate) (string, error)
	DueTimeouts(context.Context, int) ([]contracts.DueTask, error)
	ValidTimeout(context.Context, int64, int64, int, int64, time.Time) (bool, error)
}
type Candidates interface {
	PeekCandidate(context.Context, int64) (*contracts.Candidate, error)
	RemoveCandidate(context.Context, int64, contracts.Candidate) error
}
type Timeouts struct {
	Store      TimeoutStore
	Candidates Candidates
}

func (g *Timeouts) HandleTimeout(ctx context.Context, id int64, round int) (string, error) {
	c, due, err := g.Store.TimeoutSnapshot(ctx, id)
	if err != nil {
		return "", err
	}
	if !due || c.Status != "LOCKED" || c.Round != round {
		return "SKIPPED", nil
	}
	cycle, err := g.Store.TimeoutCycle(ctx, id)
	if err != nil {
		return "", err
	}
	if cycle > round {
		return "SKIPPED", nil
	}
	if round-cycle >= 5 {
		return g.Store.TimeoutStep(ctx, c, nil)
	}
	for range 32 {
		candidate, e := g.Candidates.PeekCandidate(ctx, id)
		if e != nil || candidate == nil {
			return g.Store.TimeoutStep(ctx, c, nil)
		} // Original policy: Redis outage returns to pool.
		if candidate.Round > round {
			return "SKIPPED", nil
		}
		if candidate.Round < cycle || candidate.RunnerID == c.PublisherID || (c.GrabberID != nil && candidate.RunnerID == *c.GrabberID) {
			if e = g.Candidates.RemoveCandidate(ctx, id, *candidate); e != nil {
				return "", e
			}
			continue
		}
		outcome, e := g.Store.TimeoutStep(ctx, c, candidate)
		if e != nil {
			return "", e
		}
		if outcome == "INELIGIBLE" {
			if e = g.Candidates.RemoveCandidate(ctx, id, *candidate); e != nil {
				return "", e
			}
			continue
		}
		if outcome == "TRANSFERRED" {
			_ = g.Candidates.RemoveCandidate(ctx, id, *candidate)
		}
		return outcome, nil
	}
	return "SKIPPED", nil // Batch budget is not proof of an empty queue. The SQL scan retries.
}
func (g *Timeouts) ConsumeTimeout(ctx context.Context, body []byte) (string, error) {
	var payload struct {
		ErrandID string `json:"errandId"`
		CampusID string `json:"campusId"`
		Round    int    `json:"round"`
		Version  string `json:"version"`
		Deadline string `json:"deadline"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if len(body) > 4096 || decoder.Decode(&payload) != nil {
		return "INVALID", nil
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return "INVALID", nil
	}
	id, e := strconv.ParseInt(payload.ErrandID, 10, 64)
	campus, e2 := strconv.ParseInt(payload.CampusID, 10, 64)
	version, e3 := strconv.ParseInt(payload.Version, 10, 64)
	deadline, e4 := time.Parse(time.RFC3339Nano, payload.Deadline)
	if e != nil || e2 != nil || e3 != nil || e4 != nil || id <= 0 || campus <= 0 || version < 0 || payload.Round < 0 {
		return "INVALID", nil
	}
	valid, e := g.Store.ValidTimeout(ctx, id, campus, payload.Round, version, deadline)
	if e != nil {
		return "", e
	}
	if !valid {
		return "SKIPPED", nil
	}
	return g.HandleTimeout(ctx, id, payload.Round)
}

func (g *Timeouts) ScanDue(ctx context.Context, limit int) error {
	tasks, err := g.Store.DueTimeouts(ctx, limit)
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if _, err = g.HandleTimeout(ctx, t.ID, t.Round); err != nil {
			return err
		}
	}
	return nil
}
func (g *Timeouts) RunTimeoutScan(ctx context.Context, onError func(error)) {
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		batch, cancel := context.WithTimeout(ctx, 8*time.Second)
		if err := g.ScanDue(batch, 50); err != nil {
			onError(err)
		}
		cancel()
	}
}
