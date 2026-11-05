package errand

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/yumestk/CampusGo/internal/auth"
	"github.com/yumestk/CampusGo/internal/platform/fault"
)

// Fulfill uses the same runner-before-task lock order as confirmation. Transitions
// releasing ongoing quota serialize with concurrent confirmations of other tasks.
func (g *Grabber) Fulfill(ctx context.Context, u auth.User, id int64, round int, action string) (Card, error) {
	if id <= 0 || round < 0 || round > 2147483647 {
		return Card{}, fault.New("INVALID_REQUEST", 400)
	}
	if action != "PICK_UP" && action != "DELIVER" && action != "DISPUTE" && action != "CANCEL_DRAFT" {
		return Card{}, fault.New("INVALID_REQUEST", 400)
	}
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
	runner := c.GrabberID != nil && *c.GrabberID == u.ID
	target, from := "", ""
	switch action {
	case "PICK_UP":
		if !runner {
			return Card{}, fault.New("FORBIDDEN", 403)
		}
		from, target = "ACCEPTED", "PICKED_UP"
	case "DELIVER":
		if !runner {
			return Card{}, fault.New("FORBIDDEN", 403)
		}
		from, target = "PICKED_UP", "DELIVERED"
	case "DISPUTE":
		if !runner && c.PublisherID != u.ID {
			return Card{}, fault.New("FORBIDDEN", 403)
		}
		target = "DISPUTED"
		if c.Status != "ACCEPTED" && c.Status != "PICKED_UP" && c.Status != "DELIVERED" && c.Status != target {
			return Card{}, fault.New("INVALID_STATUS", 409)
		}
		from = c.Status
	case "CANCEL_DRAFT":
		if c.PublisherID != u.ID {
			return Card{}, fault.New("FORBIDDEN", 403)
		}
		from, target = "DRAFT", "CANCELLED"
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
	r, err := tx.ExecContext(ctx, "INSERT INTO credit_event(user_id,errand_id,round,kind,delta) VALUES (?,?,?,?,?) ON DUPLICATE KEY UPDATE id=id", user, id, round, kind, delta)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil || n == 0 {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO credit_score(user_id,score,version) VALUES (?,LEAST(100,GREATEST(0,60+?)),1) ON DUPLICATE KEY UPDATE score=LEAST(100,GREATEST(0,score+?)),version=version+1", user, delta, delta)
	return err
}
func (g *Grabber) calibrateUser(ctx context.Context, id int64) error {
	return g.recalibrate(ctx, id, false)
}
func (g *Grabber) recalibrate(ctx context.Context, id int64, dueOnly bool) error {
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
func (g *Grabber) ScanAutoSettle(ctx context.Context, f *Funds) error {
	rows, err := g.DB.QueryContext(ctx, "SELECT id,campus_id,round FROM errand WHERE status='DELIVERED' AND delivered_at<=TIMESTAMPADD(SECOND,-86400,CURRENT_TIMESTAMP(3)) ORDER BY delivered_at,id LIMIT 50")
	if err != nil {
		return err
	}
	type due struct {
		id, campus int64
		round      int
	}
	tasks := []due{}
	for rows.Next() {
		var t due
		if err = rows.Scan(&t.id, &t.campus, &t.round); err != nil {
			rows.Close()
			return err
		}
		tasks = append(tasks, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, t := range tasks {
		key := autoUUID(fmt.Sprintf("auto-settle:%d:%d:%d", t.id, t.round, time.Now().Unix()/60))
		_, e := f.Execute(ctx, auth.User{ID: -1, CampusID: t.campus}, key, FundCommand{ErrandID: t.id, Round: t.round, Action: "AUTO_SETTLE"})
		if e != nil {
			return e
		}
	}
	return nil
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

func (f *Funds) ConsumeAutoSettle(ctx context.Context, body []byte) (string, error) {
	var p struct {
		ErrandID string `json:"errandId"`
		CampusID string `json:"campusId"`
		Round    int    `json:"round"`
		Version  string `json:"version"`
		Deadline string `json:"deadline"`
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if len(body) > 4096 || d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF {
		return "INVALID", nil
	}
	id, e1 := strconv.ParseInt(p.ErrandID, 10, 64)
	campus, e2 := strconv.ParseInt(p.CampusID, 10, 64)
	version, e3 := strconv.ParseInt(p.Version, 10, 64)
	deadline, e4 := time.Parse(time.RFC3339Nano, p.Deadline)
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || id <= 0 || campus <= 0 || version < 0 || p.Round < 0 {
		return "INVALID", nil
	}
	var valid bool
	if err := f.Grabber.DB.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM errand WHERE id=? AND campus_id=? AND round=? AND version=? AND status='DELIVERED' AND TIMESTAMPADD(SECOND,86400,delivered_at)=? AND delivered_at<=TIMESTAMPADD(SECOND,-86400,CURRENT_TIMESTAMP(3)))", id, campus, p.Round, version, deadline.UTC()).Scan(&valid); err != nil {
		return "", err
	}
	if !valid {
		return "SKIPPED", nil
	}
	_, err := f.Execute(ctx, auth.User{ID: -1, CampusID: campus}, autoUUID(fmt.Sprintf("auto-settle:%d:%d:%d", id, p.Round, time.Now().Unix()/60)), FundCommand{ErrandID: id, Round: p.Round, Action: "AUTO_SETTLE"})
	return "TRIGGERED", err
}

// Persisted oldest-first scheduling prevents a large calibration set from
// starving users beyond one worker batch or across restarts.
func (g *Grabber) CalibrateDueCredit(ctx context.Context) error {
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
