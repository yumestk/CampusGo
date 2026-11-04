package wallet

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/yumestk/CampusGo/internal/auth"
	"github.com/yumestk/CampusGo/internal/platform/fault"
)

type Query struct{ DB *sql.DB }
type Balance struct {
	AvailableCents int64 `json:"availableCents,string"`
	FrozenCents    int64 `json:"frozenCents,string"`
	Version        int64 `json:"version,string"`
}
type Entry struct {
	ID                int64     `json:"id,string"`
	Time              time.Time `json:"time"`
	Direction         string    `json:"direction"`
	AmountCents       int64     `json:"amountCents,string"`
	BalanceAfterCents int64     `json:"balanceAfterCents,string"`
	AccountVersion    int64     `json:"accountVersion,string"`
	RefType           string    `json:"refType"`
	RefID             int64     `json:"refId,string"`
	BizNo             string    `json:"bizNo"`
}
type Page struct {
	Items      []Entry `json:"items"`
	NextCursor string  `json:"nextCursor"`
}
type Escrow struct {
	ErrandID    int64  `json:"errandId,string"`
	AmountCents int64  `json:"amountCents,string"`
	Status      string `json:"status"`
}

func (q Query) Balance(ctx context.Context, u auth.User) (Balance, error) {
	var b Balance
	err := q.DB.QueryRowContext(ctx, "SELECT available,frozen,version FROM wallet_account WHERE owner_id=? AND owner_type='USER'", u.ID).Scan(&b.AvailableCents, &b.FrozenCents, &b.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return b, fault.New("ACCOUNT_NOT_FOUND", 404)
	}
	return b, fault.Dependency(err)
}
func (q Query) Ledger(ctx context.Context, u auth.User, before int64, size int) (Page, error) {
	p := Page{Items: []Entry{}}
	if before < 0 || size < 1 || size > 100 {
		return p, fault.New("INVALID_REQUEST", 400)
	}
	// Account version is commit-ordered under the wallet row lock; Snowflake IDs need not be.
	rows, err := q.DB.QueryContext(ctx, "SELECT l.id,l.created_at,l.direction,l.amount,l.balance_after,l.account_version,l.ref_type,l.ref_id,l.biz_no FROM wallet_ledger l JOIN wallet_account a ON a.id=l.account_id WHERE a.owner_id=? AND a.owner_type='USER' AND (?=0 OR l.account_version<?) ORDER BY l.account_version DESC LIMIT ?", u.ID, before, before, size+1)
	if err != nil {
		return p, fault.Dependency(err)
	}
	defer rows.Close()
	for rows.Next() {
		var e Entry
		if err = rows.Scan(&e.ID, &e.Time, &e.Direction, &e.AmountCents, &e.BalanceAfterCents, &e.AccountVersion, &e.RefType, &e.RefID, &e.BizNo); err != nil {
			return p, fault.Dependency(err)
		}
		p.Items = append(p.Items, e)
	}
	if err = rows.Err(); err != nil {
		return p, fault.Dependency(err)
	}
	if len(p.Items) > size {
		p.Items = p.Items[:size]
		p.NextCursor = fmt.Sprint(p.Items[size-1].AccountVersion)
	}
	return p, nil
}
func (q Query) Escrow(ctx context.Context, u auth.User, id int64) (Escrow, error) {
	var e Escrow
	// A wallet receipt belongs to its publisher; arbitrators are scoped to their campus.
	err := q.DB.QueryRowContext(ctx, "SELECT errand_id,amount,status FROM escrow_order WHERE errand_id=? AND campus_id=? AND (publisher_id=? OR ?='ARBITRATOR')", id, u.CampusID, u.ID, u.Role).Scan(&e.ErrandID, &e.AmountCents, &e.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return e, fault.New("ESCROW_NOT_FOUND", 404)
	}
	return e, fault.Dependency(err)
}

// SeedOpening only funds a newly created local/demo account. Re-running it cannot replenish spent money.
// An OPENING credit represents external opening capital; internal transfers always have two equal legs.
func SeedOpening(ctx context.Context, tx *sql.Tx, userID, amount int64) error {
	if userID <= 2 || amount < 0 {
		return fmt.Errorf("invalid opening account")
	}
	version := int64(0)
	if amount > 0 {
		version = 1
	}
	result, err := tx.ExecContext(ctx, "INSERT INTO wallet_account (id,owner_id,owner_type,available,version) VALUES (?,?,'USER',?,?) ON DUPLICATE KEY UPDATE id=id", userID, userID, amount, version)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	var owner int64
	var kind string
	if err = tx.QueryRowContext(ctx, "SELECT owner_id,owner_type FROM wallet_account WHERE id=? FOR UPDATE", userID).Scan(&owner, &kind); err != nil {
		return err
	}
	if owner != userID || kind != "USER" {
		return fmt.Errorf("opening account identity conflict")
	}
	if n == 1 && amount > 0 {
		_, err = tx.ExecContext(ctx, "INSERT INTO wallet_ledger (id,biz_no,account_id,user_id,direction,amount,balance_after,account_version,ref_type,ref_id) VALUES (?,?,?,?,'CREDIT',?,?,1,'OPENING',?)", userID, fmt.Sprintf("opening:%d", userID), userID, userID, amount, amount, userID)
	}
	return err
}
