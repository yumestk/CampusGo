package application

import (
	"github.com/yumestk/CampusGo/internal/domain/notification"

	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/yumestk/CampusGo/internal/contracts"
	creditdomain "github.com/yumestk/CampusGo/internal/domain/credit"
	taskdomain "github.com/yumestk/CampusGo/internal/domain/errand"
	walletdomain "github.com/yumestk/CampusGo/internal/domain/wallet"
	"github.com/yumestk/CampusGo/internal/platform/fault"
	"io"
	"strconv"
	"sync"
	"time"
)

type FundCommand = contracts.FundCommand
type FundEvent = contracts.FundEvent
type FundResult = contracts.FundResult
type FundRecovery = contracts.FundRecovery

// SettlementStore owns complete local atomic operations. Prepare, Apply, Check
// and Receive deliberately remain separate transaction boundaries, not CRUD.
type SettlementStore interface {
	Prepare(context.Context, contracts.Actor, string, FundCommand, []byte) (FundEvent, *FundResult, error)
	Apply(context.Context, contracts.Actor, FundEvent, []byte, SettlementPlan) (FundResult, error)
	Result(context.Context, contracts.Actor, string) (FundResult, error)
	Reject(context.Context, string, string) error
	Check(context.Context, FundEvent) string
	Receive(context.Context, FundEvent, map[int64]string) (string, error)
	Expired(context.Context) ([]FundEvent, error)
	ClaimRecovery(context.Context) ([]FundRecovery, error)
	FinishRecovery(context.Context, FundRecovery, error) error
	Dead(context.Context) ([]FundRecovery, error)
	Replay(context.Context, string, string) (bool, error)
	DueSettlements(context.Context) ([]contracts.DueSettlement, error)
	ValidAutoSettle(context.Context, int64, int64, int, int64, time.Time) (bool, error)
}
type Funds struct {
	Store  SettlementStore
	Send   func(context.Context, FundEvent) (func(bool) error, error)
	Record func(string, string)
}

func (f *Funds) record(op, outcome string) {
	if f.Record != nil {
		f.Record(op, outcome)
	}
}
func (f *Funds) Result(ctx context.Context, u contracts.Actor, key string) (FundResult, error) {
	return f.Store.Result(ctx, u, key)
}
func (f *Funds) Check(ctx context.Context, e FundEvent) string {
	state := f.Store.Check(ctx, e)
	f.record("check", state)
	return state
}
func (f *Funds) Receive(ctx context.Context, body []byte) (string, error) {
	e, err := DecodeFundEvent(body)
	if err != nil {
		return "INVALID", nil
	}
	return f.Store.Receive(ctx, e, FundNotices(e))
}
func (f *Funds) Dead(ctx context.Context) ([]FundRecovery, error) { return f.Store.Dead(ctx) }
func (f *Funds) Replay(ctx context.Context, key, reason string) (bool, error) {
	return f.Store.Replay(ctx, key, reason)
}
func (f *Funds) Execute(ctx context.Context, u contracts.Actor, key string, c FundCommand) (FundResult, error) {
	r, err := f.execute(ctx, u, key, c)
	outcome := r.State
	if err != nil {
		outcome = "ERROR"
	}
	if outcome == "" {
		outcome = "UNKNOWN"
	}
	f.record("execute", outcome)
	return r, err
}
func (f *Funds) execute(ctx context.Context, u contracts.Actor, key string, c FundCommand) (FundResult, error) {
	if !contracts.ValidRequestKey(key) || !validFundCommand(u, c) {
		return FundResult{}, fault.New("INVALID_REQUEST", 400)
	}
	data, _ := json.Marshal(c)
	hash := sha256.Sum256(data)
	event, existing, err := f.Store.Prepare(ctx, u, key, c, hash[:])
	if err != nil {
		return FundResult{}, err
	}
	if existing != nil {
		return fundOutcome(*existing)
	}
	if f.Send == nil {
		return FundResult{}, fault.New("DEPENDENCY_UNAVAILABLE", 503)
	}
	resolve, err := f.Send(ctx, event)
	if err != nil {
		// Do not execute SQL on an ambiguous half-message send. PREPARED can be retried
		// with the same key; expiry is fenced under the same row lock as execution.
		return FundResult{}, &fault.Error{Code: "FUND_RESULT_UNKNOWN", Status: 503, Cause: err}
	}
	r, err := f.Store.Apply(ctx, u, event, hash[:], planSettlement(event))
	// executeSQL has already rolled its transaction back on a known posting
	// rejection. Persist the rejection separately; never do this for COMMIT errors.
	var rejection *fault.Error
	if errors.As(err, &rejection) && rejection.Status == 409 && r.State == "PREPARED" {
		if e := f.Store.Reject(ctx, event.RequestID, rejection.Code); e != nil {
			return r, &fault.Error{Code: "FUND_RESULT_UNKNOWN", Status: 503, Cause: e}
		}
	}
	// Only durable known states resolve. No-row, unavailable DB and in-flight SQL
	// stay UNKNOWN; a failed commit acknowledgment is never treated as rollback.
	state := f.Check(ctx, event)
	if state == "COMMIT" {
		_ = resolve(true)
	} else if state == "ROLLBACK" {
		_ = resolve(false)
	}
	if err != nil {
		return r, err
	}
	return fundOutcome(r)
}
func validFundCommand(u contracts.Actor, c FundCommand) bool {
	if c.ErrandID <= 0 || c.Round < 0 || c.Round > 2147483647 {
		return false
	}
	switch c.Action {
	case "SETTLE", "CANCEL", "ARBITRATE_RUNNER", "ARBITRATE_PUBLISHER":
		return u.ID > 0
	case "AUTO_SETTLE":
		return u.ID == -1
	}
	return false
}
func fundOutcome(r FundResult) (FundResult, error) {
	if r.State == "ABORTED" {
		code := r.ErrorCode
		if code == "" {
			code = "FUND_EXPIRED"
		}
		return r, fault.New(code, 409)
	}
	return r, nil
}
func (f *Funds) Recover(ctx context.Context) error {
	// Expiry also runs when MQ is down. The request lock prevents a late SQL commit.
	events, err := f.Store.Expired(ctx)
	if err != nil {
		return err
	}
	for _, e := range events {
		c, cancel := context.WithTimeout(ctx, time.Second)
		f.Check(c, e)
		cancel()
	}
	if f.Send == nil {
		return nil
	}
	tasks, err := f.Store.ClaimRecovery(ctx)
	if err != nil {
		return err
	}
	for _, t := range tasks {
		c, cancel := context.WithTimeout(ctx, 2*time.Second)
		var resolve func(bool) error
		var e error
		if f.Check(c, t.Event) != "COMMIT" {
			e = fmt.Errorf("funds facts unavailable or inconsistent")
		} else {
			resolve, e = f.Send(c, t.Event)
		}
		if e == nil {
			e = resolve(true)
		}
		cancel()
		// Confirmation only proves send/commit, not consumption. Republish the same
		// event after a bounded wait until a receipt exists, with a finite retry budget.
		err = f.Store.FinishRecovery(ctx, t, e)
		if err != nil {
			return err
		}
		if e != nil {
			f.record("recovery", "ERROR")
		} else {
			f.record("recovery", "SENT")
		}
	}
	return nil
}
func (f *Funds) ScanAutoSettle(ctx context.Context) error {
	tasks, err := f.Store.DueSettlements(ctx)
	if err != nil {
		return err
	}
	for _, t := range tasks {
		key := autoUUID(fmt.Sprintf("auto-settle:%d:%d:%d", t.ID, t.Round, time.Now().Unix()/60))
		_, e := f.Execute(ctx, contracts.Actor{ID: -1, CampusID: t.CampusID}, key, FundCommand{ErrandID: t.ID, Round: t.Round, Action: "AUTO_SETTLE"})
		if e != nil {
			return e
		}
	}
	return nil
}
func (f *Funds) ConsumeAutoSettle(ctx context.Context, body []byte) (string, error) {
	var p struct {
		ErrandID string `json:"errandId"`
		CampusID string `json:"campusId"`
		Round    int    `json:"round"`
		Version  string `json:"version"`
		Deadline string `json:"deadline"`
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if len(body) > 4096 || d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF {
		return "INVALID", nil
	}
	id, e1 := strconv.ParseInt(p.ErrandID, 10, 64)
	campus, e2 := strconv.ParseInt(p.CampusID, 10, 64)
	version, e3 := strconv.ParseInt(p.Version, 10, 64)
	deadline, e4 := time.Parse(time.RFC3339Nano, p.Deadline)
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || id <= 0 || campus <= 0 || version < 0 || p.Round < 0 {
		return "INVALID", nil
	}
	valid, err := f.Store.ValidAutoSettle(ctx, id, campus, p.Round, version, deadline)
	if err != nil {
		return "", err
	}
	if !valid {
		return "SKIPPED", nil
	}
	_, err = f.Execute(ctx, contracts.Actor{ID: -1, CampusID: campus}, autoUUID(fmt.Sprintf("auto-settle:%d:%d:%d", id, p.Round, time.Now().Unix()/60)), FundCommand{ErrandID: id, Round: p.Round, Action: "AUTO_SETTLE"})
	return "TRIGGERED", err
}
func (f *Funds) Run(ctx context.Context, onError func(error)) {
	var wg sync.WaitGroup
	for _, job := range []struct {
		interval time.Duration
		fn       func(context.Context) error
	}{
		{time.Second, f.Recover},
		{time.Second, f.ScanAutoSettle},
	} {
		wg.Add(1)
		go func() { defer wg.Done(); RunEvery(ctx, job.interval, job.fn, onError) }()
	}
	wg.Wait()
}
func RunEvery(ctx context.Context, d time.Duration, fn func(context.Context) error, onError func(error)) {
	tick := time.NewTicker(d)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			c, cancel := context.WithTimeout(ctx, 8*time.Second)
			if err := fn(c); err != nil && ctx.Err() == nil {
				onError(err)
			}
			cancel()
		}
	}
}
func autoUUID(s string) string { return uuid.NewSHA1(uuid.NameSpaceOID, []byte(s)).String() }

// SettlementPlan coordinates task, wallet and credit decisions. The store applies
// this plan only after revalidating the persisted event and locking its facts.
// It must commit all effects together; the plan contains no database handles.
type SettlementPlan struct {
	Status, EscrowStatus, CreditKind string
	CreditDelta                      int
	Allocation                       walletdomain.Allocation
}

func planSettlement(e FundEvent) SettlementPlan {
	status := taskdomain.FundStatus(e.Kind)
	p := SettlementPlan{Status: status, EscrowStatus: "REFUNDED", Allocation: walletdomain.Allocate(e.Amount, status == "SETTLED")}
	if status == "SETTLED" {
		p.EscrowStatus = "RELEASED"
		p.CreditKind = "SETTLE"
	} else if e.Kind == "ARBITRATE_PUBLISHER" {
		p.CreditKind = "DISPUTE_LOSE"
	}
	if p.CreditKind != "" {
		p.CreditDelta, _ = creditdomain.Delta(p.CreditKind)
	}
	return p
}

func FundNotices(e FundEvent) map[int64]string {
	return notification.Funds(notification.Settlement{Kind: e.Kind, PublisherID: e.PublisherID, RunnerID: e.RunnerID, Amount: e.Amount, Commission: e.Commission})
}
