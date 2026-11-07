package mysql

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/yumestk/CampusGo/internal/application"
	"github.com/yumestk/CampusGo/internal/contracts"
	taskdomain "github.com/yumestk/CampusGo/internal/domain/errand"
	walletdomain "github.com/yumestk/CampusGo/internal/domain/wallet"
	"github.com/yumestk/CampusGo/internal/platform/idgen"
	"math"
	"sort"

	"github.com/google/uuid"
	"github.com/yumestk/CampusGo/internal/auth"
	"github.com/yumestk/CampusGo/internal/platform/fault"
)

// Send returns only after the broker confirms the half message. Resolution is
// separate: an uncertain SQL COMMIT must leave the message for durable checking.

func autoUUID(s string) string      { return uuid.NewSHA1(uuid.NameSpaceOID, []byte(s)).String() }
func Commission(amount int64) int64 { return walletdomain.Commission(amount) }
func validFundCommand(u auth.User, c FundCommand) bool {
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
func fundStatus(kind string) string {
	return taskdomain.FundStatus(kind)
}

func authorizeFund(ctx context.Context, tx *sql.Tx, u auth.User, c Card, action string) error {
	if code := taskModel(c).AuthorizeFund(taskActor(u), action); code != "" {
		status := 409
		if code == "FORBIDDEN" {
			status = 403
		}
		return fault.New(code, status)
	}
	if action == "AUTO_SETTLE" {
		var due bool
		if err := tx.QueryRowContext(ctx, "SELECT delivered_at<=TIMESTAMPADD(SECOND,-86400,CURRENT_TIMESTAMP(3)) FROM errand WHERE id=?", c.ID).Scan(&due); err != nil {
			return err
		}
		if !due {
			return fault.New("NOT_DUE", 409)
		}
	}
	return nil
}
func (f *FundStore) Result(ctx context.Context, u auth.User, key string) (FundResult, error) {
	if !ValidRequestKey(key) {
		return FundResult{}, fault.New("INVALID_REQUEST", 400)
	}
	r := FundResult{RequestID: key}
	var payload []byte
	err := f.DB.QueryRowContext(ctx, "SELECT state,errand_id,error_code,payload FROM fund_request WHERE request_id=? AND actor_id=? AND campus_id=?", key, u.ID, u.CampusID).Scan(&r.State, &r.ErrandID, &r.ErrorCode, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		r.State = "NOT_OBSERVED"
		return r, nil
	}
	if err != nil {
		return r, fault.Dependency(err)
	}
	if r.State == "COMMITTED" {
		var e FundEvent
		if err = json.Unmarshal(payload, &e); err != nil {
			return r, fault.Dependency(err)
		}
		r.Status = fundStatus(e.Kind)
	}
	return r, nil
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
func (f *FundStore) Prepare(ctx context.Context, u auth.User, key string, c FundCommand, hash []byte) (FundEvent, *FundResult, error) {
	tx, err := f.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return FundEvent{}, nil, fault.Dependency(err)
	}
	defer tx.Rollback()
	// Existing request identity is checked before current task status, so a retry
	// returns its immutable outcome even after the task became terminal.
	var actor, campus int64
	var stored, payload []byte
	var state, code string
	err = tx.QueryRowContext(ctx, "SELECT actor_id,campus_id,fingerprint,payload,state,error_code FROM fund_request WHERE request_id=?", key).Scan(&actor, &campus, &stored, &payload, &state, &code)
	if err == nil {
		if actor != u.ID || campus != u.CampusID || !bytes.Equal(stored, hash) {
			return FundEvent{}, nil, fault.New("IDEMPOTENCY_CONFLICT", 409)
		}
		var e FundEvent
		if err = json.Unmarshal(payload, &e); err != nil {
			return e, nil, fault.Dependency(err)
		}
		if state != "PREPARED" {
			r := FundResult{RequestID: key, State: state, ErrandID: e.ErrandID, ErrorCode: code}
			if state == "COMMITTED" {
				r.Status = fundStatus(e.Kind)
			}
			return e, &r, nil
		}
		return e, nil, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return FundEvent{}, nil, fault.Dependency(err)
	}
	card, err := scan(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM errand WHERE id=? AND campus_id=?", c.ErrandID, u.CampusID))
	if errors.Is(err, sql.ErrNoRows) {
		return FundEvent{}, nil, fault.New("ERRAND_NOT_FOUND", 404)
	}
	if err != nil {
		return FundEvent{}, nil, fault.Dependency(err)
	}
	if card.Round != c.Round {
		return FundEvent{}, nil, fault.New("STALE_VERSION", 409)
	}
	if err = authorizeFund(ctx, tx, u, card, c.Action); err != nil {
		return FundEvent{}, nil, err
	}
	runner := int64(0)
	if card.GrabberID != nil {
		runner = *card.GrabberID
	}
	event := FundEvent{RequestID: key, CampusID: u.CampusID, ErrandID: c.ErrandID, ActorID: u.ID, PublisherID: card.PublisherID, RunnerID: runner, Round: c.Round, Kind: c.Action, Amount: card.RewardCents}
	if fundStatus(c.Action) == "SETTLED" {
		if runner <= 0 {
			return event, nil, fault.New("INVALID_STATUS", 409)
		}
		event.Commission = walletdomain.Allocate(event.Amount, true).Commission
	}
	payload, _ = json.Marshal(event)
	result, err := tx.ExecContext(ctx, `INSERT INTO fund_request(request_id,actor_id,campus_id,errand_id,fingerprint,payload,expires_at,next_recovery_at) VALUES (?,?,?,?,?,?,TIMESTAMPADD(SECOND,60,CURRENT_TIMESTAMP(3)),TIMESTAMPADD(SECOND,120,CURRENT_TIMESTAMP(3))) ON DUPLICATE KEY UPDATE request_id=request_id`, key, u.ID, u.CampusID, c.ErrandID, hash, payload)
	if err != nil {
		return event, nil, fault.Dependency(err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return event, nil, fault.Dependency(err)
	}
	if err = tx.Commit(); err != nil {
		return event, nil, &fault.Error{Code: "FUND_RESULT_UNKNOWN", Status: 503, Cause: err}
	}
	if n != 1 {
		return f.Prepare(ctx, u, key, c, hash)
	}
	return event, nil, nil
}
func (f *FundStore) Apply(ctx context.Context, u auth.User, e FundEvent, hash []byte, plan application.SettlementPlan) (FundResult, error) {
	beforeCommit, commit := f.beforeCommit, f.commit
	r := FundResult{RequestID: e.RequestID, ErrandID: e.ErrandID}
	tx, err := f.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return r, fault.Dependency(err)
	}
	defer tx.Rollback()
	var stored []byte
	var live bool
	if err = tx.QueryRowContext(ctx, "SELECT state,error_code,fingerprint FROM fund_request WHERE request_id=? FOR UPDATE", e.RequestID).Scan(&r.State, &r.ErrorCode, &stored); err != nil {
		return r, fault.Dependency(err)
	}
	if !bytes.Equal(hash, stored) {
		return r, fault.New("IDEMPOTENCY_CONFLICT", 409)
	}
	if r.State != "PREPARED" {
		if r.State == "COMMITTED" {
			r.Status = fundStatus(e.Kind)
		}
		return r, nil
	}
	reject := func(code string) (FundResult, error) {
		r.State = "ABORTED"
		r.ErrorCode = code
		_, x := tx.ExecContext(ctx, "UPDATE fund_request SET state='ABORTED',error_code=? WHERE request_id=?", code, e.RequestID)
		if x != nil {
			return r, fault.Dependency(x)
		}
		if x = tx.Commit(); x != nil {
			return r, &fault.Error{Code: "FUND_RESULT_UNKNOWN", Status: 503, Cause: x}
		}
		return r, nil
	}
	if err = tx.QueryRowContext(ctx, "SELECT expires_at>CURRENT_TIMESTAMP(3) FROM fund_request WHERE request_id=?", e.RequestID).Scan(&live); err != nil {
		return r, fault.Dependency(err)
	}
	if !live {
		return reject("FUND_EXPIRED")
	}
	// Lock participants in ascending order, before task and wallet locks. This is
	// compatible with quota-changing transitions and the credit calibrator.
	userIDs := []int64{}
	if u.ID > 0 {
		userIDs = append(userIDs, u.ID)
	}
	if e.RunnerID > 0 && e.RunnerID != u.ID {
		userIDs = append(userIDs, e.RunnerID)
	}
	sort.Slice(userIDs, func(i, j int) bool { return userIDs[i] < userIDs[j] })
	for _, id := range userIDs {
		var campus int64
		var active bool
		var role string
		if err = tx.QueryRowContext(ctx, "SELECT campus_id,active,role FROM app_user WHERE id=? FOR UPDATE", id).Scan(&campus, &active, &role); err != nil {
			return r, fault.Dependency(err)
		}
		if id == u.ID {
			if !active || campus != u.CampusID {
				return reject("FORBIDDEN")
			}
			u.Role = role
		}
	}
	card, err := scan(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM errand WHERE id=? AND campus_id=? FOR UPDATE", e.ErrandID, e.CampusID))
	if err != nil {
		return r, fault.Dependency(err)
	}
	if card.Round != e.Round {
		return reject("STALE_VERSION")
	}
	if err = authorizeFund(ctx, tx, u, card, e.Kind); err != nil {
		var fe *fault.Error
		if errors.As(err, &fe) {
			return reject(fe.Code)
		}
		return r, fault.Dependency(err)
	}
	if card.RewardCents != e.Amount || card.PublisherID != e.PublisherID || (card.GrabberID != nil && *card.GrabberID != e.RunnerID) {
		return reject("STALE_VERSION")
	}
	var amount int64
	var escrowState string
	if err = tx.QueryRowContext(ctx, "SELECT amount,status FROM escrow_order WHERE errand_id=? AND campus_id=? FOR UPDATE", e.ErrandID, e.CampusID).Scan(&amount, &escrowState); err != nil {
		return r, fault.Dependency(err)
	}
	if amount != e.Amount || escrowState != "HELD" {
		return r, fault.New("FUNDS_INCONSISTENT", 500)
	}
	if err = f.post(ctx, tx, e, plan.Allocation); err != nil {
		var fe *fault.Error
		if errors.As(err, &fe) {
			return r, err
		}
		return r, fault.Dependency(err)
	}
	status, escrowTarget := plan.Status, plan.EscrowStatus
	if _, err = tx.ExecContext(ctx, "UPDATE escrow_order SET status=? WHERE errand_id=?", escrowTarget, e.ErrandID); err != nil {
		return r, fault.Dependency(err)
	}
	if _, err = tx.ExecContext(ctx, "UPDATE errand SET status=?,version=version+1,slot_taken=IF(?='CANCELLED',0,slot_taken) WHERE id=?", status, status, e.ErrandID); err != nil {
		return r, fault.Dependency(err)
	}
	if err = transitionEffects(ctx, tx, card, status, e.Round, u.ID); err != nil {
		return r, fault.Dependency(err)
	}
	if plan.CreditKind != "" {
		err = applyCredit(ctx, tx, e.RunnerID, e.ErrandID, e.Round, plan.CreditKind, plan.CreditDelta)
	}
	if err != nil {
		return r, fault.Dependency(err)
	}
	if _, err = tx.ExecContext(ctx, "UPDATE fund_request SET state='COMMITTED',committed_at=CURRENT_TIMESTAMP(3) WHERE request_id=?", e.RequestID); err != nil {
		return r, fault.Dependency(err)
	}
	if beforeCommit != nil {
		if err = beforeCommit(tx); err != nil {
			return r, fault.Dependency(err)
		}
	}
	if commit == nil {
		commit = func(tx *sql.Tx) error { return tx.Commit() }
	}
	if err = commit(tx); err != nil {
		return r, &fault.Error{Code: "FUND_RESULT_UNKNOWN", Status: 503, Cause: err}
	}
	r.State = "COMMITTED"
	r.Status = status
	return r, nil
}
func (f *FundStore) post(ctx context.Context, tx *sql.Tx, e FundEvent, allocation walletdomain.Allocation) error {
	type posting struct {
		id, amount      int64
		direction, kind string
		owner           int64
	}
	target, kind := e.PublisherID, "REFUND"
	if fundStatus(e.Kind) == "SETTLED" {
		target, kind = e.RunnerID, "SETTLE"
	}
	var targetID int64
	if err := tx.QueryRowContext(ctx, "SELECT id FROM wallet_account WHERE owner_id=? AND owner_type='USER'", target).Scan(&targetID); err != nil {
		return err
	}
	legs := []posting{{1, e.Amount, "DEBIT", "ESCROW", -1}, {targetID, allocation.Recipient, "CREDIT", "USER", target}}
	if allocation.Commission > 0 {
		legs = append(legs, posting{2, allocation.Commission, "CREDIT", "COMMISSION", -2})
	}
	sort.Slice(legs, func(i, j int) bool { return legs[i].id < legs[j].id })
	for _, leg := range legs {
		var a lockedAccount
		if err := tx.QueryRowContext(ctx, "SELECT owner_id,owner_type,available,frozen,version FROM wallet_account WHERE id=? FOR UPDATE", leg.id).Scan(&a.owner, &a.kind, &a.available, &a.frozen, &a.version); err != nil {
			return err
		}
		if a.owner != leg.owner || a.kind != leg.kind || a.version == math.MaxInt64 {
			return fmt.Errorf("wallet identity/sequence invariant")
		}
		after, code := walletdomain.Apply(a.available, a.version, leg.amount, leg.direction == "DEBIT")
		if code != "" {
			if code == "BALANCE_LIMIT" {
				return fault.New(code, 409)
			}
			return fmt.Errorf("wallet invariant: %s", code)
		}
		if _, err := tx.ExecContext(ctx, "UPDATE wallet_account SET available=?,version=version+1 WHERE id=?", after, leg.id); err != nil {
			return err
		}
		ledgerID, err := f.IDs.Next(ctx)
		if err != nil {
			return err
		}
		biz := fmt.Sprintf("%s:%d", map[string]string{"SETTLE": "settle", "REFUND": "refund"}[kind], e.ErrandID)
		if _, err = tx.ExecContext(ctx, "INSERT INTO wallet_ledger(id,biz_no,account_id,user_id,direction,amount,balance_after,account_version,ref_type,ref_id) VALUES(?,?,?,?,?,?,?,?,?,?)", ledgerID, biz, leg.id, target, leg.direction, leg.amount, after, a.version+1, kind, e.ErrandID); err != nil {
			return err
		}
	}
	return nil
}

type FundStore struct {
	DB           *sql.DB
	IDs          *idgen.Generator
	beforeCommit func(*sql.Tx) error
	commit       func(*sql.Tx) error
}
type FundCommand = contracts.FundCommand
type FundEvent = contracts.FundEvent
type FundResult = contracts.FundResult

func (f *FundStore) Reject(ctx context.Context, key, code string) error {
	_, err := f.DB.ExecContext(ctx, "UPDATE fund_request SET state='ABORTED',error_code=? WHERE request_id=? AND state='PREPARED'", code, key)
	return err
}
