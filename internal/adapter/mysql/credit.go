package mysql

import (
	"context"
	"database/sql"
	"fmt"

	creditdomain "github.com/yumestk/CampusGo/internal/domain/credit"
)

type CreditStore struct{ DB *sql.DB }

func applyCredit(ctx context.Context, tx *sql.Tx, user, id int64, round int, kind string, delta int) error {
	expected, ok := creditdomain.Delta(kind)
	if !ok || expected != delta {
		return fmt.Errorf("invalid credit event rule")
	}

	r, err := tx.ExecContext(ctx, "INSERT INTO credit_event(user_id,errand_id,round,kind,delta) VALUES (?,?,?,?,?) ON DUPLICATE KEY UPDATE id=id", user, id, round, kind, delta)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil || n == 0 {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO credit_score(user_id,score,version) VALUES (?,?,1) ON DUPLICATE KEY UPDATE score=LEAST(?,GREATEST(?,score+?)),version=version+1", user, creditdomain.Clamp(creditdomain.Base+delta), creditdomain.Maximum, creditdomain.Minimum, delta)
	return err
}
func (g *CreditStore) calibrateUser(ctx context.Context, id int64) error {
	return g.recalibrate(ctx, id, false)
}
func (g *CreditStore) recalibrate(ctx context.Context, id int64, dueOnly bool) error {
	tx, err := g.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var uid int64
	if err = tx.QueryRowContext(ctx, "SELECT id FROM app_user WHERE id=? FOR UPDATE", id).Scan(&uid); err != nil {
		return err
	}
	if dueOnly {
		var due bool
		if err = tx.QueryRowContext(ctx, "SELECT last_calibrated_at<=TIMESTAMPADD(DAY,-1,CURRENT_TIMESTAMP(3)) FROM credit_score WHERE user_id=?", id).Scan(&due); err != nil {
			return err
		}
		if !due {
			return nil
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO credit_score(user_id,score,version,last_calibrated_at) SELECT ?,LEAST(?,GREATEST(?,?+COALESCE(SUM(delta),0))),1,CURRENT_TIMESTAMP(3) FROM credit_event WHERE user_id=? AND created_at>=TIMESTAMPADD(DAY,?,CURRENT_TIMESTAMP(3)) ON DUPLICATE KEY UPDATE score=VALUES(score),version=version+1,last_calibrated_at=CURRENT_TIMESTAMP(3)`, id, creditdomain.Maximum, creditdomain.Minimum, creditdomain.Base, id, -creditdomain.WindowDays)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (g *CreditStore) CalibrateDueCredit(ctx context.Context) error {
	rows, err := g.DB.QueryContext(ctx, "SELECT user_id FROM credit_score WHERE last_calibrated_at<=TIMESTAMPADD(DAY,-1,CURRENT_TIMESTAMP(3)) ORDER BY last_calibrated_at,user_id LIMIT 100")
	if err != nil {
		return err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = g.recalibrate(ctx, id, true); err != nil {
			return err
		}
	}
	return nil
}
