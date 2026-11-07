package mysql

import (
	"context"
	"database/sql"
	"errors"
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
