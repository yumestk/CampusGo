package application

import (
	"context"
	"errors"
	"testing"

	"github.com/yumestk/CampusGo/internal/contracts"
	fault "github.com/yumestk/CampusGo/internal/failure"
)

// Unused storage operations deliberately remain unimplemented: a protocol branch
// that unexpectedly reaches one fails immediately rather than succeeding.
type settlementProbe struct {
	SettlementStore
	existing                   *FundResult
	result                     FundResult
	applyError                 error
	resolution                 string
	applied, checked, rejected bool
}

func (s *settlementProbe) Prepare(context.Context, contracts.Actor, string, FundCommand, []byte) (FundEvent, *FundResult, error) {
	return FundEvent{Kind: "SETTLE", Amount: 1001, RunnerID: 2}, s.existing, nil
}
func (s *settlementProbe) Apply(_ context.Context, _ contracts.Actor, _ FundEvent, _ []byte, p SettlementPlan) (FundResult, error) {
	s.applied = true
	if p.Allocation.Recipient != 951 || p.Allocation.Commission != 50 || p.CreditKind != "SETTLE" || p.CreditDelta != 2 {
		panic("invalid settlement plan")
	}
	return s.result, s.applyError
}
func (s *settlementProbe) Check(context.Context, FundEvent) string {
	s.checked = true
	return s.resolution
}
func (s *settlementProbe) Reject(context.Context, string, string) error {
	s.rejected = true
	return nil
}

func TestSettlementResolutionUsesDurableFacts(t *testing.T) {
	ctx := context.Background()
	u := contracts.Actor{ID: 1, CampusID: 1}
	cmd := FundCommand{ErrandID: 3, Action: "SETTLE"}
	key := "9b61a2ed-d35e-43ef-aac8-a6d99f83dff3"
	for _, c := range []struct {
		name, resolution                                                 string
		sendError, applyError                                            error
		state                                                            string
		wantApplied, wantChecked, wantResolved, wantCommit, wantRejected bool
	}{
		{name: "ambiguous half send cannot post", sendError: errors.New("lost half receipt")},
		{name: "unknown SQL stays unresolved", resolution: "UNKNOWN", applyError: fault.New("FUND_RESULT_UNKNOWN", 503), wantApplied: true, wantChecked: true},
		{name: "lost commit ack still resolves durable commit", resolution: "COMMIT", applyError: fault.New("FUND_RESULT_UNKNOWN", 503), wantApplied: true, wantChecked: true, wantResolved: true, wantCommit: true},
		{name: "known posting rejection resolves rollback", resolution: "ROLLBACK", applyError: fault.New("BALANCE_LIMIT", 409), state: "PREPARED", wantApplied: true, wantChecked: true, wantResolved: true, wantRejected: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			store := &settlementProbe{resolution: c.resolution, applyError: c.applyError, result: FundResult{State: c.state}}
			resolved, commit := false, false
			app := &Funds{Store: store, Send: func(context.Context, FundEvent) (func(bool) error, error) {
				if c.sendError != nil {
					return nil, c.sendError
				}
				return func(v bool) error { resolved, commit = true, v; return nil }, nil
			}}
			_, err := app.Execute(ctx, u, key, cmd)
			if err == nil {
				t.Fatal("uncertainty/rejection was hidden")
			}
			if store.applied != c.wantApplied || store.checked != c.wantChecked || store.rejected != c.wantRejected || resolved != c.wantResolved || commit != c.wantCommit {
				t.Fatalf("applied=%v checked=%v rejected=%v resolved=%v commit=%v", store.applied, store.checked, store.rejected, resolved, commit)
			}
		})
	}
	t.Run("durable replay does not send", func(t *testing.T) {
		store := &settlementProbe{existing: &FundResult{State: "COMMITTED"}}
		app := &Funds{Store: store, Send: func(context.Context, FundEvent) (func(bool) error, error) {
			t.Fatal("replay sent another half message")
			return nil, nil
		}}
		if r, err := app.Execute(ctx, u, key, cmd); err != nil || r.State != "COMMITTED" || store.applied {
			t.Fatal(r, err)
		}
	})
}
func TestGrabRequiresExplicitCapabilities(t *testing.T) {
	if _, err := NewGrab(nil, nil, nil); err == nil {
		t.Fatal("missing dependencies accepted")
	}
}
