package wallet

import (
	"context"
	"database/sql"
)

type Check struct {
	Name       string `json:"name"`
	Violations int64  `json:"violations,string"`
}

// Reconcile observes one repeatable-read snapshot. DECIMAL arithmetic also supports totals above int64.
// This is the D2 set of invariants; settlement/refund postings must extend it in D5.
func (q Query) Reconcile(ctx context.Context) ([]Check, error) {
	tx, err := q.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	checks := []struct{ name, query string }{
		{"account_ledger_balance_and_sequence", `SELECT COUNT(*) FROM wallet_account a LEFT JOIN
 (SELECT account_id,SUM(CASE WHEN direction='CREDIT' THEN CAST(amount AS DECIMAL(65,0)) ELSE -CAST(amount AS DECIMAL(65,0)) END) balance,COUNT(*) n,MAX(account_version) v FROM wallet_ledger GROUP BY account_id) l ON l.account_id=a.id
 WHERE a.available<>COALESCE(l.balance,0) OR a.frozen<>0 OR a.version<>COALESCE(l.n,0) OR a.version<>COALESCE(l.v,0)`},
		{"ledger_balance_after_chain", `SELECT COUNT(*) FROM
 (SELECT balance_after,SUM(CASE WHEN direction='CREDIT' THEN CAST(amount AS DECIMAL(65,0)) ELSE -CAST(amount AS DECIMAL(65,0)) END) OVER (PARTITION BY account_id ORDER BY account_version ROWS UNBOUNDED PRECEDING) running FROM wallet_ledger) l WHERE balance_after<>running`},
		{"internal_transfer_two_equal_legs", `SELECT COUNT(*) FROM
 (SELECT biz_no,COUNT(*) n,SUM(CASE WHEN direction='CREDIT' THEN CAST(amount AS DECIMAL(65,0)) ELSE -CAST(amount AS DECIMAL(65,0)) END) net,COUNT(DISTINCT account_id) accounts FROM wallet_ledger WHERE ref_type='ESCROW' GROUP BY biz_no) l WHERE n<>2 OR net<>0 OR accounts<>2`},
		{"opening_capital_and_total", `SELECT IF(
 (SELECT COALESCE(SUM(CAST(available AS DECIMAL(65,0))+frozen),0) FROM wallet_account) <>
 (SELECT COALESCE(SUM(CAST(amount AS DECIMAL(65,0))),0) FROM wallet_ledger WHERE ref_type='OPENING'),1,0)`},
		{"opening_postings", `SELECT COUNT(*) FROM wallet_ledger l JOIN wallet_account a ON a.id=l.account_id WHERE l.ref_type='OPENING' AND (l.direction<>'CREDIT' OR a.owner_type<>'USER' OR l.user_id<>a.owner_id OR l.ref_id<>a.owner_id OR l.account_version<>1 OR l.biz_no<>CONCAT('opening:',a.owner_id))`},
		{"held_escrow_balance", `SELECT IF((SELECT available FROM wallet_account WHERE id=1 AND owner_type='ESCROW' AND owner_id=-1) <>
 (SELECT COALESCE(SUM(CAST(amount AS DECIMAL(65,0))),0) FROM escrow_order WHERE status='HELD'),1,0)`},
		{"system_accounts", `SELECT IF((SELECT COUNT(*) FROM wallet_account WHERE (id=1 AND owner_id=-1 AND owner_type='ESCROW') OR (id=2 AND owner_id=-2 AND owner_type='COMMISSION' AND available=0 AND frozen=0))=2,0,1)`},
		{"task_escrow_and_postings", `SELECT COUNT(*) FROM escrow_order e LEFT JOIN errand t ON t.id=e.errand_id
 LEFT JOIN wallet_account a ON a.owner_id=e.publisher_id AND a.owner_type='USER'
 LEFT JOIN wallet_ledger d ON d.biz_no=CONCAT('escrow:',e.errand_id) AND d.account_id=a.id AND d.direction='DEBIT'
 LEFT JOIN wallet_ledger c ON c.biz_no=CONCAT('escrow:',e.errand_id) AND c.account_id=1 AND c.direction='CREDIT'
 WHERE t.id IS NULL OR t.status='DRAFT' OR e.campus_id<>t.campus_id OR e.publisher_id<>t.publisher_id OR e.amount<>t.reward_amount OR e.status<>'HELD'
 OR d.id IS NULL OR c.id IS NULL OR d.amount<>e.amount OR c.amount<>e.amount OR d.ref_type<>'ESCROW' OR c.ref_type<>'ESCROW' OR d.ref_id<>e.errand_id OR c.ref_id<>e.errand_id OR d.user_id<>e.publisher_id OR c.user_id<>e.publisher_id`},
		{"unfunded_tasks", `SELECT COUNT(*) FROM errand t LEFT JOIN escrow_order e ON e.errand_id=t.id WHERE t.status<>'DRAFT' AND e.id IS NULL`},
		{"orphan_transfer_postings", `SELECT COUNT(*) FROM wallet_ledger l LEFT JOIN escrow_order e ON e.errand_id=l.ref_id WHERE l.ref_type='ESCROW' AND (e.id IS NULL OR l.biz_no<>CONCAT('escrow:',l.ref_id))`},
		{"publish_result_matches_funds", `SELECT COUNT(*) FROM publish_request r LEFT JOIN errand t ON t.id=r.errand_id LEFT JOIN escrow_order e ON e.errand_id=r.errand_id WHERE r.state='PROCESSING' OR (r.state='SUCCEEDED' AND (t.id IS NULL OR e.id IS NULL OR r.publisher_id<>t.publisher_id OR r.reward_amount<>e.amount))`},
		{"escrow_has_publish_result", `SELECT COUNT(*) FROM escrow_order e LEFT JOIN publish_request r ON r.errand_id=e.errand_id AND r.state='SUCCEEDED' WHERE r.errand_id IS NULL`},
	}
	out := make([]Check, 0, len(checks))
	for _, item := range checks {
		var n int64
		if err = tx.QueryRowContext(ctx, item.query).Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, Check{item.name, n})
	}
	return out, nil
}
