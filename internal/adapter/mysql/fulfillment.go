package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	creditdomain "github.com/yumestk/CampusGo/internal/domain/credit"
	"time"

	"github.com/yumestk/CampusGo/internal/auth"
	"github.com/yumestk/CampusGo/internal/platform/fault"
)

// Fulfill uses the same runner-before-task lock order as confirmation. Transitions
// releasing ongoing quota serialize with concurrent confirmations of other tasks.
func (g *TaskStore) ApplyFulfill(ctx context.Context, u auth.User, id int64, round int, action string) (Card, error) {
	preview, err := scan(g.DB.QueryRowContext(ctx, "SELECT "+columns+" FROM errand WHERE id=? AND campus_id=?", id, u.CampusID))
	if errors.Is(err, sql.ErrNoRows) {
		return Card{}, fault.New("ERRAND_NOT_FOUND", 404)
	}
	if err != nil {
		return Card{}, fault.Dependency(err)
	}
	tx, err := g.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return Card{}, fault.Dependency(err)
	}
	defer tx.Rollback()
	// Identity and quota locks are always acquired in user-ID order.
	userIDs := []int64{u.ID}
	if preview.GrabberID != nil && *preview.GrabberID != u.ID {
		userIDs = append(userIDs, *preview.GrabberID)
		if userIDs[0] > userIDs[1] {
			userIDs[0], userIDs[1] = userIDs[1], userIDs[0]
		}
	}
	for _, uid := range userIDs {
		valid, e := lockRunner(ctx, tx, uid, u.CampusID)
		if e != nil {
			return Card{}, fault.Dependency(e)
		}
		if uid == u.ID && !valid {
			return Card{}, fault.New("FORBIDDEN", 403)
		}
	}
	c, err := scan(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM errand WHERE id=? AND campus_id=? FOR UPDATE", id, u.CampusID))
	if err != nil {
		return Card{}, fault.Dependency(err)
	}
	if c.Round != round {
		return Card{}, fault.New("STALE_VERSION", 409)
	}
	from, target, code := taskModel(c).Fulfill(taskActor(u), action)
	if code != "" {
		status := 409
		if code == "FORBIDDEN" {
			status = 403
		}
		return Card{}, fault.New(code, status)
	}
	if c.Status == target {
		decorate(&c, u)
		return c, nil
	}
	if c.Status != from {
		return Card{}, fault.New("INVALID_STATUS", 409)
	}
	if action == "CANCEL_DRAFT" {
		var n int
		if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM escrow_order WHERE errand_id=?", id).Scan(&n); err != nil {
			return Card{}, fault.Dependency(err)
		}
		if n != 0 {
			return Card{}, fault.New("INVALID_STATUS", 409)
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE errand SET status=?,version=version+1,delivered_at=IF(?='DELIVERED',CURRENT_TIMESTAMP(3),delivered_at) WHERE id=?", target, target, id); err != nil {
		return Card{}, fault.Dependency(err)
	}
	if err = transitionEffects(ctx, tx, c, target, round, u.ID); err != nil {
		return Card{}, fault.Dependency(err)
	}
	if target == "DELIVERED" {
		_, err = tx.ExecContext(ctx, `INSERT INTO local_message(campus_id,errand_id,msg_key,topic,payload,deliver_at) SELECT campus_id,id,CONCAT('auto-settle:',id,':',round),'errand-auto-settle',JSON_OBJECT('errandId',CAST(id AS CHAR),'campusId',CAST(campus_id AS CHAR),'round',round,'version',CAST(version AS CHAR),'deadline',DATE_FORMAT(TIMESTAMPADD(SECOND,86400,delivered_at),'%Y-%m-%dT%H:%i:%s.%fZ')),TIMESTAMPADD(SECOND,86400,delivered_at) FROM errand WHERE id=?`, id)
		if err != nil {
			return Card{}, fault.Dependency(err)
		}
	}
	fresh, e := scan(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM errand WHERE id=?", id))
	if e != nil {
		return Card{}, fault.Dependency(e)
	}
	if err = tx.Commit(); err != nil {
		return Card{}, &fault.Error{Code: "ACTION_RESULT_UNKNOWN", Status: 503, Cause: err}
	}
	decorate(&fresh, u)
	return fresh, nil
}

// applyCredit is called while the runner's app_user row is locked. Event and
// score share the business transaction; daily calibration has the same lock.
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
func (g *TaskStore) calibrateUser(ctx context.Context, id int64) error {
	return g.recalibrate(ctx, id, false)
}
func (g *TaskStore) recalibrate(ctx context.Context, id int64, dueOnly bool) error {
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
	_, err = tx.ExecContext(ctx, `INSERT INTO credit_score(user_id,score,version,last_calibrated_at) SELECT ?,LEAST(100,GREATEST(0,60+COALESCE(SUM(delta),0))),1,CURRENT_TIMESTAMP(3) FROM credit_event WHERE user_id=? AND created_at>=TIMESTAMPADD(DAY,-30,CURRENT_TIMESTAMP(3)) ON DUPLICATE KEY UPDATE score=VALUES(score),version=version+1,last_calibrated_at=CURRENT_TIMESTAMP(3)`, id, id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func runEvery(ctx context.Context, d time.Duration, fn func(context.Context) error, onError func(error)) {
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

// Persisted oldest-first scheduling prevents a large calibration set from
// starving users beyond one worker batch or across restarts.
func (g *TaskStore) CalibrateDueCredit(ctx context.Context) error {
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
