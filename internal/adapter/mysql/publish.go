package mysql

import (
	"github.com/yumestk/CampusGo/internal/contracts"

	"bytes"
	"context"

	"database/sql"

	"errors"
	"fmt"

	"math"

	"github.com/google/uuid"
	"github.com/yumestk/CampusGo/internal/auth"
	"github.com/yumestk/CampusGo/internal/platform/fault"
	"github.com/yumestk/CampusGo/internal/platform/idgen"
)

type PublishStore struct {
	DB           *sql.DB
	IDs          *idgen.Generator
	beforeCommit func(*sql.Tx) error
	commit       func(*sql.Tx) error
}

func ValidRequestKey(key string) bool {
	parsed, err := uuid.Parse(key)
	return err == nil && parsed != uuid.Nil && parsed.String() == key
}

func scanResult(row *sql.Row, key string) (PublishResult, []byte, error) {
	r := PublishResult{RequestKey: key}
	var fingerprint []byte
	var id sql.NullInt64
	err := row.Scan(&fingerprint, &r.State, &id, &r.FrozenCents, &r.ErrorCode)
	if id.Valid {
		r.ErrandID = &id.Int64
		r.Status = "PUBLISHED"
	}
	if r.State != "SUCCEEDED" {
		r.FrozenCents = 0
	}
	return r, fingerprint, err
}

const resultColumns = "fingerprint,state,errand_id,reward_amount,error_code"

// A missing committed row is not proof of rollback: an earlier COMMIT may still be in flight.
func (p *PublishStore) Result(ctx context.Context, u auth.User, key string) (PublishResult, error) {
	if !ValidRequestKey(key) {
		return PublishResult{}, fault.New("INVALID_REQUEST", 400)
	}
	r, _, err := scanResult(p.DB.QueryRowContext(ctx, "SELECT "+resultColumns+" FROM publish_request WHERE publisher_id=? AND request_key=?", u.ID, key), key)
	if errors.Is(err, sql.ErrNoRows) {
		return PublishResult{RequestKey: key, State: "NOT_OBSERVED"}, nil
	}
	if err != nil {
		return PublishResult{}, fault.Dependency(err)
	}
	if r.State != "SUCCEEDED" && r.State != "REJECTED" {
		return PublishResult{}, fault.New("INTERNAL_ERROR", 500)
	}
	return r, nil
}

// Test hooks are package private: exercise rollback after writes and loss of an actual commit acknowledgment.
// They are never configurable from HTTP, environment variables, or the production constructor.
func (p *PublishStore) ApplyPublish(ctx context.Context, u auth.User, key string, c PublishCommand, fingerprint []byte) (PublishResult, error) {
	beforeCommit, commit := p.beforeCommit, p.commit
	tx, err := p.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return PublishResult{}, fault.Dependency(err)
	}
	defer tx.Rollback()
	// The no-op upsert takes an exclusive unique-key lock immediately. A plain INSERT
	// followed by SELECT FOR UPDATE upgrades duplicate-key shared locks and can deadlock replays.
	inserted, err := tx.ExecContext(ctx, "INSERT INTO publish_request (publisher_id,request_key,fingerprint,state,reward_amount) VALUES (?,?,?,'PROCESSING',?) ON DUPLICATE KEY UPDATE request_key=request_key", u.ID, key, fingerprint, c.RewardCents)
	if err != nil {
		return PublishResult{}, fault.Dependency(err)
	}
	storedResult, stored, err := scanResult(tx.QueryRowContext(ctx, "SELECT "+resultColumns+" FROM publish_request WHERE publisher_id=? AND request_key=? FOR UPDATE", u.ID, key), key)
	if err != nil {
		return PublishResult{}, fault.Dependency(err)
	}
	if !bytes.Equal(stored, fingerprint) {
		return PublishResult{}, fault.New("IDEMPOTENCY_CONFLICT", 409)
	}
	if storedResult.State == "SUCCEEDED" || storedResult.State == "REJECTED" {
		// Read-only replay needs no COMMIT; rollback releases the duplicate-key lock.
		return publishOutcome(storedResult)
	}
	// database.Open uses MySQL's default clientFoundRows=false: only a new row affects one row.
	n, err := inserted.RowsAffected()
	if err != nil {
		return PublishResult{}, fault.Dependency(err)
	}
	if storedResult.State != "PROCESSING" || n != 1 {
		return PublishResult{}, fault.New("INTERNAL_ERROR", 500)
	}
	var campus int64
	var active bool
	if err = tx.QueryRowContext(ctx, "SELECT campus_id,active FROM app_user WHERE id=? FOR SHARE", u.ID).Scan(&campus, &active); err != nil {
		return PublishResult{}, fault.Dependency(err)
	}
	if !active || campus != u.CampusID {
		return PublishResult{}, fault.New("FORBIDDEN", 403)
	}
	r := PublishResult{RequestKey: key, State: "REJECTED"}
	var accountID int64
	err = tx.QueryRowContext(ctx, "SELECT id FROM wallet_account WHERE owner_id=? AND owner_type='USER'", u.ID).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		r.ErrorCode = "ACCOUNT_NOT_FOUND"
	} else if err != nil {
		return PublishResult{}, fault.Dependency(err)
	}
	if r.ErrorCode == "" {
		// The original system escrow ID is 1. Explicit ordered row locks also define D5's account lock order.
		ids := []int64{1, accountID}
		if accountID < 1 {
			return PublishResult{}, fault.New("INTERNAL_ERROR", 500)
		}
		if ids[0] > ids[1] {
			ids[0], ids[1] = ids[1], ids[0]
		}
		accounts := map[int64]lockedAccount{}
		for _, id := range ids {
			var a lockedAccount
			if err = tx.QueryRowContext(ctx, "SELECT owner_id,owner_type,available,frozen,version FROM wallet_account WHERE id=? FOR UPDATE", id).Scan(&a.owner, &a.kind, &a.available, &a.frozen, &a.version); err != nil {
				return PublishResult{}, fault.Dependency(err)
			}
			accounts[id] = a
		}
		user, escrow := accounts[accountID], accounts[1]
		if user.owner != u.ID || user.kind != "USER" || escrow.owner != -1 || escrow.kind != "ESCROW" {
			return PublishResult{}, fault.New("INTERNAL_ERROR", 500)
		}
		switch {
		case user.available < c.RewardCents:
			r.ErrorCode = "INSUFFICIENT_BALANCE"
		case escrow.available > math.MaxInt64-c.RewardCents || user.version == math.MaxInt64 || escrow.version == math.MaxInt64:
			r.ErrorCode = "BALANCE_LIMIT"
		default:
			id, e := p.IDs.Next(ctx)
			if e != nil {
				return PublishResult{}, fault.Dependency(e)
			}
			r.State, r.ErrandID, r.Status, r.FrozenCents = "SUCCEEDED", &id, "PUBLISHED", c.RewardCents
			if err = p.transfer(ctx, tx, u, c, id, accountID, user, escrow); err != nil {
				return PublishResult{}, fault.Dependency(err)
			}
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE publish_request SET state=?,errand_id=?,error_code=? WHERE publisher_id=? AND request_key=?", r.State, r.ErrandID, r.ErrorCode, u.ID, key); err != nil {
		return PublishResult{}, fault.Dependency(err)
	}
	if beforeCommit != nil {
		if err = beforeCommit(tx); err != nil {
			return PublishResult{}, fault.Dependency(err)
		}
	}
	if commit == nil {
		commit = func(t *sql.Tx) error { return t.Commit() }
	}
	if err = commit(tx); err != nil {
		return PublishResult{}, &fault.Error{Code: "PUBLISH_RESULT_UNKNOWN", Status: 503, Cause: err}
	}
	return publishOutcome(r)
}

type lockedAccount struct {
	owner, available, frozen, version int64
	kind                              string
}

func publishOutcome(r PublishResult) (PublishResult, error) {
	if r.State == "REJECTED" {
		return r, fault.New(r.ErrorCode, 409)
	}
	return r, nil
}
func (p *PublishStore) transfer(ctx context.Context, tx *sql.Tx, u auth.User, c PublishCommand, id, accountID int64, user, escrow lockedAccount) error {
	if _, err := tx.ExecContext(ctx, "INSERT INTO errand (id,campus_id,publisher_id,type,title,reward_amount,slot_total,status) VALUES (?,?,?,?,?,?,1,'DRAFT')", id, u.CampusID, u.ID, c.Type, c.Title, c.RewardCents); err != nil {
		return err
	}
	biz := fmt.Sprintf("escrow:%d", id)
	for _, leg := range []struct {
		account, after, version int64
		direction               string
	}{{accountID, user.available - c.RewardCents, user.version + 1, "DEBIT"}, {1, escrow.available + c.RewardCents, escrow.version + 1, "CREDIT"}} {
		result, err := tx.ExecContext(ctx, "UPDATE wallet_account SET available=?,version=? WHERE id=? AND version=?", leg.after, leg.version, leg.account, leg.version-1)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("locked wallet update lost")
		}
		ledgerID, err := p.IDs.Next(ctx)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO wallet_ledger (id,biz_no,account_id,user_id,direction,amount,balance_after,account_version,ref_type,ref_id) VALUES (?,?,?,?,?,?,?,?,'ESCROW',?)", ledgerID, biz, leg.account, u.ID, leg.direction, c.RewardCents, leg.after, leg.version, id); err != nil {
			return err
		}
	}
	escrowID, err := p.IDs.Next(ctx)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO escrow_order (id,campus_id,errand_id,publisher_id,amount,status) VALUES (?,?,?,?,?,'HELD')", escrowID, u.CampusID, id, u.ID, c.RewardCents); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "UPDATE errand SET status='PUBLISHED',version=version+1 WHERE id=? AND status='DRAFT' AND version=0", id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("publish transition lost")
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO errand_status_log (campus_id,errand_id,from_status,to_status,round,operator_id) VALUES (?,?,'DRAFT','PUBLISHED',0,?)", u.CampusID, id, u.ID)
	return err
}

type PublishCommand = contracts.PublishCommand

type PublishResult = contracts.PublishResult
