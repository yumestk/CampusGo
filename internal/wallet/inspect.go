package wallet

import (
	"context"
	"database/sql"
	"fmt"
)

// Inspect returns raw durable facts for a task without changing accounts. Values
// travel as strings, including JSON payloads, so CLI output retains int64 precision.
func (q Query) Inspect(ctx context.Context, id int64) (map[string][]map[string]string, error) {
	if id <= 0 {
		return nil, fmt.Errorf("positive errand ID required")
	}
	tx, err := q.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	queries := map[string]string{
		"task":      "SELECT id,campus_id,publisher_id,grabber_id,status,round,version,reward_amount,delivered_at FROM errand WHERE id=?",
		"escrow":    "SELECT id,errand_id,publisher_id,amount,status FROM escrow_order WHERE errand_id=?",
		"ledger":    "SELECT id,biz_no,account_id,user_id,direction,amount,balance_after,account_version,ref_type FROM wallet_ledger WHERE ref_id=? AND ref_type<>'OPENING' ORDER BY account_id,account_version",
		"requests":  "SELECT request_id,actor_id,state,error_code,payload,expires_at,committed_at,recovery_state,recovery_attempts,replay_count,replay_reason FROM fund_request WHERE errand_id=?",
		"credit":    "SELECT user_id,kind,delta,round,created_at FROM credit_event WHERE errand_id=? ORDER BY id",
		"statusLog": "SELECT from_status,to_status,operator_id,round,created_at FROM errand_status_log WHERE errand_id=? ORDER BY id",
	}
	out := map[string][]map[string]string{}
	for name, query := range queries {
		rows, e := tx.QueryContext(ctx, query, id)
		if e != nil {
			return nil, e
		}
		columns, e := rows.Columns()
		if e != nil {
			rows.Close()
			return nil, e
		}
		items := []map[string]string{}
		for rows.Next() {
			values := make([]sql.NullString, len(columns))
			dest := make([]any, len(columns))
			for i := range values {
				dest[i] = &values[i]
			}
			if e = rows.Scan(dest...); e != nil {
				rows.Close()
				return nil, e
			}
			item := map[string]string{}
			for i, c := range columns {
				if values[i].Valid {
					item[c] = values[i].String
				}
			}
			items = append(items, item)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
		out[name] = items
	}
	return out, nil
}
