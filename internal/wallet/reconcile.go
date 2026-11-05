package wallet

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

type Check struct {
	Name       string   `json:"name"`
	Violations int64    `json:"violations,string"`
	Examples   []string `json:"examples,omitempty"`
}

// Reconcile observes one repeatable-read snapshot. DECIMAL arithmetic also supports totals above int64.
// Includes terminal transfers and their immutable transaction-message facts.
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
 (SELECT account_id,balance_after,SUM(CASE WHEN direction='CREDIT' THEN CAST(amount AS DECIMAL(65,0)) ELSE -CAST(amount AS DECIMAL(65,0)) END) OVER (PARTITION BY account_id ORDER BY account_version ROWS UNBOUNDED PRECEDING) running FROM wallet_ledger) l WHERE balance_after<>running`},
		{"internal_transfer_two_equal_legs", `SELECT COUNT(*) FROM
 (SELECT biz_no,COUNT(*) n,SUM(CASE WHEN direction='CREDIT' THEN CAST(amount AS DECIMAL(65,0)) ELSE -CAST(amount AS DECIMAL(65,0)) END) net,COUNT(DISTINCT account_id) accounts FROM wallet_ledger WHERE ref_type='ESCROW' GROUP BY biz_no) l WHERE n<>2 OR net<>0 OR accounts<>2`},
		{"opening_capital_and_total", `SELECT IF(
 (SELECT COALESCE(SUM(CAST(available AS DECIMAL(65,0))+frozen),0) FROM wallet_account) <>
 (SELECT COALESCE(SUM(CAST(amount AS DECIMAL(65,0))),0) FROM wallet_ledger WHERE ref_type='OPENING'),1,0)`},
		{"opening_postings", `SELECT COUNT(*) FROM wallet_ledger l JOIN wallet_account a ON a.id=l.account_id WHERE l.ref_type='OPENING' AND (l.direction<>'CREDIT' OR a.owner_type<>'USER' OR l.user_id<>a.owner_id OR l.ref_id<>a.owner_id OR l.account_version<>1 OR l.biz_no<>CONCAT('opening:',a.owner_id))`},
		{"held_escrow_balance", `SELECT IF((SELECT available FROM wallet_account WHERE id=1 AND owner_type='ESCROW' AND owner_id=-1) <>
 (SELECT COALESCE(SUM(CAST(amount AS DECIMAL(65,0))),0) FROM escrow_order WHERE status='HELD'),1,0)`},
		{"system_accounts", `SELECT IF((SELECT COUNT(*) FROM wallet_account WHERE (id=1 AND owner_id=-1 AND owner_type='ESCROW') OR (id=2 AND owner_id=-2 AND owner_type='COMMISSION' AND frozen=0))=2,0,1)`},
		{"task_escrow_and_postings", `SELECT COUNT(*) FROM escrow_order e LEFT JOIN errand t ON t.id=e.errand_id
 LEFT JOIN wallet_account a ON a.owner_id=e.publisher_id AND a.owner_type='USER'
 LEFT JOIN wallet_ledger d ON d.biz_no=CONCAT('escrow:',e.errand_id) AND d.account_id=a.id AND d.direction='DEBIT'
 LEFT JOIN wallet_ledger c ON c.biz_no=CONCAT('escrow:',e.errand_id) AND c.account_id=1 AND c.direction='CREDIT'
 WHERE t.id IS NULL OR t.status='DRAFT' OR e.campus_id<>t.campus_id OR e.publisher_id<>t.publisher_id OR e.amount<>t.reward_amount OR e.status<>CASE WHEN t.status='SETTLED' THEN 'RELEASED' WHEN t.status IN ('CANCELLED','REFUNDED') THEN 'REFUNDED' ELSE 'HELD' END
 OR d.id IS NULL OR c.id IS NULL OR d.amount<>e.amount OR c.amount<>e.amount OR d.ref_type<>'ESCROW' OR c.ref_type<>'ESCROW' OR d.ref_id<>e.errand_id OR c.ref_id<>e.errand_id OR d.user_id<>e.publisher_id OR c.user_id<>e.publisher_id`},
		{"unfunded_tasks", `SELECT COUNT(*) FROM errand t LEFT JOIN escrow_order e ON e.errand_id=t.id WHERE t.status NOT IN ('DRAFT','CANCELLED') AND e.id IS NULL`},
		{"orphan_transfer_postings", `SELECT COUNT(*) FROM wallet_ledger l LEFT JOIN escrow_order e ON e.errand_id=l.ref_id WHERE l.ref_type='ESCROW' AND (e.id IS NULL OR l.biz_no<>CONCAT('escrow:',l.ref_id))`},
		{"publish_result_matches_funds", `SELECT COUNT(*) FROM publish_request r LEFT JOIN errand t ON t.id=r.errand_id LEFT JOIN escrow_order e ON e.errand_id=r.errand_id WHERE r.state='PROCESSING' OR (r.state='SUCCEEDED' AND (t.id IS NULL OR e.id IS NULL OR r.publisher_id<>t.publisher_id OR r.reward_amount<>e.amount))`},
		{"escrow_has_publish_result", `SELECT COUNT(*) FROM escrow_order e LEFT JOIN publish_request r ON r.errand_id=e.errand_id AND r.state='SUCCEEDED' WHERE r.errand_id IS NULL`},

		{"terminal_transfer_amount_and_recipient", `SELECT COUNT(*) FROM escrow_order e JOIN errand t ON t.id=e.errand_id WHERE
 (e.status='RELEASED' AND ((SELECT COUNT(*) FROM wallet_ledger l WHERE l.biz_no=CONCAT('settle:',t.id))<>IF(FLOOR(CAST(e.amount AS DECIMAL(65,0))*500/10000)>0,3,2)
 OR (SELECT COUNT(*) FROM wallet_ledger l WHERE l.biz_no=CONCAT('settle:',t.id) AND l.ref_type='SETTLE' AND l.ref_id=t.id AND l.user_id=t.grabber_id AND ((l.account_id=1 AND l.direction='DEBIT' AND l.amount=e.amount) OR (l.account_id=2 AND l.direction='CREDIT' AND l.amount=FLOOR(CAST(e.amount AS DECIMAL(65,0))*500/10000)) OR (l.account_id=(SELECT id FROM wallet_account WHERE owner_id=t.grabber_id AND owner_type='USER') AND l.direction='CREDIT' AND l.amount=e.amount-FLOOR(CAST(e.amount AS DECIMAL(65,0))*500/10000))))<>IF(FLOOR(CAST(e.amount AS DECIMAL(65,0))*500/10000)>0,3,2)))
 OR (e.status='REFUNDED' AND ((SELECT COUNT(*) FROM wallet_ledger l WHERE l.biz_no=CONCAT('refund:',t.id))<>2 OR (SELECT COUNT(*) FROM wallet_ledger l WHERE l.biz_no=CONCAT('refund:',t.id) AND l.ref_type='REFUND' AND l.ref_id=t.id AND l.user_id=t.publisher_id AND l.amount=e.amount AND ((l.account_id=1 AND l.direction='DEBIT') OR (l.account_id=(SELECT id FROM wallet_account WHERE owner_id=t.publisher_id AND owner_type='USER') AND l.direction='CREDIT')))<>2))`},
		{"terminal_transfer_conservation", `SELECT COUNT(*) FROM (SELECT biz_no,SUM(CASE WHEN direction='CREDIT' THEN CAST(amount AS DECIMAL(65,0)) ELSE -CAST(amount AS DECIMAL(65,0)) END) net FROM wallet_ledger WHERE ref_type IN ('SETTLE','REFUND') GROUP BY biz_no) l WHERE net<>0`},
		{"terminal_transfer_request", `SELECT COUNT(*) FROM escrow_order e WHERE (e.status IN ('RELEASED','REFUNDED') AND (SELECT COUNT(*) FROM fund_request r WHERE r.errand_id=e.errand_id AND r.state='COMMITTED')<>1) OR (e.status='HELD' AND EXISTS(SELECT 1 FROM fund_request r WHERE r.errand_id=e.errand_id AND r.state='COMMITTED'))`},
		{"orphan_terminal_postings", `SELECT COUNT(*) FROM wallet_ledger l LEFT JOIN escrow_order e ON e.errand_id=l.ref_id WHERE l.ref_type IN ('SETTLE','REFUND') AND (e.id IS NULL OR (l.ref_type='SETTLE' AND (e.status<>'RELEASED' OR l.biz_no<>CONCAT('settle:',l.ref_id))) OR (l.ref_type='REFUND' AND (e.status<>'REFUNDED' OR l.biz_no<>CONCAT('refund:',l.ref_id))))`},
		{"terminal_credit", `SELECT COUNT(*) FROM fund_request r JOIN errand t ON t.id=r.errand_id WHERE r.state='COMMITTED' AND ((t.status='SETTLED' AND (SELECT COUNT(*) FROM credit_event c WHERE c.errand_id=t.id AND c.round=t.round AND c.kind='SETTLE' AND c.user_id=t.grabber_id AND c.delta=2)<>1) OR (JSON_UNQUOTE(JSON_EXTRACT(r.payload,'$.kind'))='ARBITRATE_PUBLISHER' AND (SELECT COUNT(*) FROM credit_event c WHERE c.errand_id=t.id AND c.round=t.round AND c.kind='DISPUTE_LOSE' AND c.user_id=t.grabber_id AND c.delta=-8)<>1))`},
		{"fund_request_terminal_state", `SELECT COUNT(*) FROM fund_request r JOIN errand t ON t.id=r.errand_id WHERE r.state='COMMITTED' AND (t.status<>CASE JSON_UNQUOTE(JSON_EXTRACT(r.payload,'$.kind')) WHEN 'CANCEL' THEN 'CANCELLED' WHEN 'ARBITRATE_PUBLISHER' THEN 'REFUNDED' ELSE 'SETTLED' END OR CAST(JSON_UNQUOTE(JSON_EXTRACT(r.payload,'$.amountCents')) AS DECIMAL(65,0))<>t.reward_amount OR r.committed_at IS NULL)`},
	}
	out := make([]Check, 0, len(checks))
	for _, item := range checks {
		var n int64
		if err = tx.QueryRowContext(ctx, item.query).Scan(&n); err != nil {
			return nil, err
		}
		check := Check{Name: item.name, Violations: n}
		if n > 0 {
			check.Examples, err = reconcileExamples(ctx, tx, item.name, item.query)
			if err != nil {
				return nil, err
			}
		}
		out = append(out, check)
	}
	return out, nil
}

func reconcileExamples(ctx context.Context, tx *sql.Tx, name, query string) ([]string, error) {
	identifiers := map[string]string{
		"account_ledger_balance_and_sequence": "a.id", "ledger_balance_after_chain": "l.account_id",
		"internal_transfer_two_equal_legs": "l.biz_no", "opening_postings": "l.id",
		"task_escrow_and_postings": "e.errand_id", "unfunded_tasks": "t.id", "orphan_transfer_postings": "l.ref_id",
		"publish_result_matches_funds": "CONCAT(r.publisher_id,':',r.request_key)", "escrow_has_publish_result": "e.errand_id",
		"terminal_transfer_amount_and_recipient": "e.errand_id", "terminal_transfer_conservation": "l.biz_no", "terminal_transfer_request": "e.errand_id",
		"orphan_terminal_postings": "l.ref_id", "terminal_credit": "r.errand_id", "fund_request_terminal_state": "r.request_id",
	}
	var q string
	if id, ok := identifiers[name]; ok {
		q = strings.Replace(query, "SELECT COUNT(*) FROM", "SELECT CAST("+id+" AS CHAR) FROM", 1) + " LIMIT 100"
	} else {
		switch name {
		case "opening_capital_and_total":
			q = `SELECT CONCAT('account-total=',(SELECT COALESCE(SUM(CAST(available AS DECIMAL(65,0))+frozen),0) FROM wallet_account),';opening-total=',(SELECT COALESCE(SUM(CAST(amount AS DECIMAL(65,0))),0) FROM wallet_ledger WHERE ref_type='OPENING'))`
		case "held_escrow_balance":
			q = `SELECT CONCAT('escrow-account=',(SELECT available FROM wallet_account WHERE id=1),';held-total=',(SELECT COALESCE(SUM(CAST(amount AS DECIMAL(65,0))),0) FROM escrow_order WHERE status='HELD'))`
		case "system_accounts":
			q = `SELECT CONCAT(id,':',owner_id,':',owner_type,':',available,':',frozen) FROM wallet_account WHERE id IN (1,2) OR owner_type IN ('ESCROW','COMMISSION') LIMIT 100`
		default:
			return nil, fmt.Errorf("missing reconciliation diagnostic: %s", name)
		}
	}
	rows, err := tx.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var example sql.NullString
		if err = rows.Scan(&example); err != nil {
			return nil, err
		}
		if example.Valid {
			out = append(out, example.String)
		}
	}
	return out, rows.Err()
}
