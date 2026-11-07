package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/yumestk/CampusGo/internal/auth"
	walletdomain "github.com/yumestk/CampusGo/internal/domain/wallet"
	"github.com/yumestk/CampusGo/internal/platform/fault"
	"math"
	"sort"
)

// Posting helpers never begin or commit. They preserve the caller's atomic
// business scope and the original ascending account lock order.
type lockedAccount struct {
	owner, available, frozen, version int64
	kind                              string
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
func (p *PublishStore) postEscrow(ctx context.Context, tx *sql.Tx, u auth.User, c PublishCommand, id, accountID int64, user, escrow lockedAccount) error {
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
	return nil
}
func setEscrowStatus(ctx context.Context, tx *sql.Tx, id int64, status string) error {
	_, err := tx.ExecContext(ctx, "UPDATE escrow_order SET status=? WHERE errand_id=?", status, id)
	return err
}
