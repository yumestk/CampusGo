package errand

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/yumestk/CampusGo/internal/auth"
	"github.com/yumestk/CampusGo/internal/platform/fault"
)

// All transitions increasing ongoing work lock app_user first, then the task.
// LOCKED is excluded: several reservations may compete for the last ongoing slot.
func lockRunner(ctx context.Context, tx *sql.Tx, id, campus int64) (bool, error) {
	var actual int64
	var active bool
	var role string
	err := tx.QueryRowContext(ctx, "SELECT campus_id,active,role FROM app_user WHERE id=? FOR UPDATE", id).Scan(&actual, &active, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return actual == campus && active && (role == "STUDENT" || role == "ARBITRATOR"), err
}
func runnerEligible(ctx context.Context, tx *sql.Tx, id int64) (string, error) {
	var score, ongoing int
	err := tx.QueryRowContext(ctx, "SELECT COALESCE((SELECT score FROM credit_score WHERE user_id=?),60),(SELECT COUNT(*) FROM errand WHERE grabber_id=? AND status IN ('ACCEPTED','PICKED_UP'))", id, id).Scan(&score, &ongoing)
	if err != nil {
		return "", err
	}
	if score < 40 {
		return "CREDIT_TOO_LOW", nil
	}
	if ongoing >= 5 {
		return "TOO_MANY_ONGOING", nil
	}
	return "", nil
}
func (g *Grabber) Confirm(ctx context.Context, u auth.User, id int64, round int) (Card, error) {
	if id <= 0 || round < 0 || round > 2147483647 {
		return Card{}, fault.New("INVALID_REQUEST", 400)
	}
	tx, err := g.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return Card{}, fault.Dependency(err)
	}
	defer tx.Rollback()
	valid, err := lockRunner(ctx, tx, u.ID, u.CampusID)
	if err != nil {
		return Card{}, fault.Dependency(err)
	}
	if !valid {
		return Card{}, fault.New("FORBIDDEN", 403)
	}
	c, err := scan(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM errand WHERE id=? AND campus_id=? FOR UPDATE", id, u.CampusID))
	if errors.Is(err, sql.ErrNoRows) {
		return Card{}, fault.New("ERRAND_NOT_FOUND", 404)
	}
	if err != nil {
		return Card{}, fault.Dependency(err)
	}
	if c.Round != round {
		return Card{}, fault.New("STALE_VERSION", 409)
	}
	if c.GrabberID == nil || *c.GrabberID != u.ID {
		return Card{}, fault.New("NOT_CURRENT_GRABBER", 409)
	}
	if c.Status == "ACCEPTED" {
		decorate(&c, u)
		return c, nil
	}
	if c.Status != "LOCKED" {
		return Card{}, fault.New("INVALID_STATUS", 409)
	}
	reason, err := runnerEligible(ctx, tx, u.ID)
	if err != nil {
		return Card{}, fault.Dependency(err)
	}
	if reason != "" {
		return Card{}, fault.New(reason, 409)
	}
	result, err := tx.ExecContext(ctx, "UPDATE errand SET status='ACCEPTED',confirm_deadline=NULL,version=version+1 WHERE id=? AND status='LOCKED' AND round=? AND confirm_deadline>CURRENT_TIMESTAMP(3)", id, round)
	if err != nil {
		return Card{}, fault.Dependency(err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return Card{}, fault.Dependency(err)
	}
	if n != 1 {
		return Card{}, fault.New("CONFIRM_DEADLINE_PASSED", 409)
	}
	if err = transitionEffects(ctx, tx, c, "ACCEPTED", round, u.ID); err != nil {
		return Card{}, fault.Dependency(err)
	}
	c.Status = "ACCEPTED"
	c.ConfirmDeadline = nil
	c.Version++
	if err = tx.Commit(); err != nil {
		return Card{}, &fault.Error{Code: "CONFIRM_RESULT_UNKNOWN", Status: 503, Cause: err}
	}
	_ = g.syncProjection(ctx, id)
	_ = g.invalidateDetail(ctx, id)
	decorate(&c, u)
	return c, nil
}
func transitionEffects(ctx context.Context, tx *sql.Tx, c Card, status string, round int, actor int64) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO errand_status_log (campus_id,errand_id,from_status,to_status,round,operator_id) VALUES (?,?,?,?,?,?)", c.CampusID, c.ID, c.Status, status, round, actor)
	if err != nil {
		return err
	}
	if err = enqueueRepair(ctx, tx, c.CampusID, c.ID, fmt.Sprintf("projection:%d:%d", c.ID, c.Version+1), "PROJECTION", nil, 0); err != nil {
		return err
	}
	for _, delay := range []int{0, 2} {
		if err = enqueueRepair(ctx, tx, c.CampusID, c.ID, fmt.Sprintf("cache:%d:%d:%d", c.ID, c.Version+1, delay), "CACHE_INVALIDATE", nil, delay); err != nil {
			return err
		}
	}
	return nil
}
func enqueueTimeout(ctx context.Context, tx *sql.Tx, id int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO local_message (campus_id,errand_id,msg_key,topic,payload,deliver_at) SELECT campus_id,id,CONCAT('confirm-timeout:',id,':',round),'errand-confirm-timeout',JSON_OBJECT('errandId',CAST(id AS CHAR),'campusId',CAST(campus_id AS CHAR),'round',round,'version',CAST(version AS CHAR),'deadline',DATE_FORMAT(confirm_deadline,'%Y-%m-%dT%H:%i:%s.%fZ')),confirm_deadline FROM errand WHERE id=?`, id)
	return err
}
func (g *Grabber) timeoutSnapshot(ctx context.Context, id int64) (Card, bool, error) {
	c, err := scan(g.DB.QueryRowContext(ctx, "SELECT "+columns+" FROM errand WHERE id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, false, nil
	}
	if err != nil {
		return c, false, err
	}
	var due bool
	err = g.DB.QueryRowContext(ctx, "SELECT status='LOCKED' AND confirm_deadline<=CURRENT_TIMESTAMP(3) FROM errand WHERE id=?", id).Scan(&due)
	return c, due, err
}
func (g *Grabber) HandleTimeout(ctx context.Context, id int64, round int) (string, error) {
	return g.handleTimeout(ctx, id, round, nil)
}
func (g *Grabber) handleTimeout(ctx context.Context, id int64, round int, hook func(string)) (string, error) {
	c, due, err := g.timeoutSnapshot(ctx, id)
	if err != nil {
		return "", err
	}
	if !due || c.Status != "LOCKED" || c.Round != round {
		return "SKIPPED", nil
	}
	var cycle int
	err = g.DB.QueryRowContext(ctx, "SELECT COALESCE(MAX(round),0) FROM errand_status_log WHERE errand_id=? AND to_status='PUBLISHED'", id).Scan(&cycle)
	if err != nil {
		return "", err
	}
	if cycle > round {
		return "SKIPPED", nil
	}
	if round-cycle >= 5 {
		return g.timeoutStep(ctx, c, nil, hook)
	}
	for range 32 {
		candidate, e := g.peekCandidate(ctx, id)
		if e != nil || candidate == nil {
			return g.timeoutStep(ctx, c, nil, hook)
		} // Original policy: Redis outage returns to pool.
		if candidate.Round > round {
			return "SKIPPED", nil
		}
		if candidate.Round < cycle || candidate.RunnerID == c.PublisherID || (c.GrabberID != nil && candidate.RunnerID == *c.GrabberID) {
			if e = g.removeCandidate(ctx, id, *candidate); e != nil {
				return "", e
			}
			continue
		}
		outcome, e := g.timeoutStep(ctx, c, candidate, hook)
		if e != nil {
			return "", e
		}
		if outcome == "INELIGIBLE" {
			if e = g.removeCandidate(ctx, id, *candidate); e != nil {
				return "", e
			}
			continue
		}
		if outcome == "TRANSFERRED" {
			_ = g.removeCandidate(ctx, id, *candidate)
		}
		return outcome, nil
	}
	return "SKIPPED", nil // Batch budget is not proof of an empty queue. The SQL scan retries.
}
func (g *Grabber) timeoutStep(ctx context.Context, c Card, next *Candidate, hook func(string)) (string, error) {
	tx, err := g.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	runner := int64(0)
	if next != nil {
		runner = next.RunnerID
	} else if c.GrabberID != nil {
		runner = *c.GrabberID
	}
	valid, err := lockRunner(ctx, tx, runner, c.CampusID)
	if err != nil {
		return "", err
	}
	if next != nil {
		if !valid {
			return "INELIGIBLE", nil
		}
		reason, e := runnerEligible(ctx, tx, runner)
		if e != nil {
			return "", e
		}
		if reason != "" {
			return "INELIGIBLE", nil
		}
	}
	current, err := scan(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM errand WHERE id=? FOR UPDATE", c.ID))
	if err != nil {
		return "", err
	}
	if current.Version != c.Version || current.Round != c.Round || current.Status != "LOCKED" {
		return "SKIPPED", nil
	}
	// Database time is evaluated after locks. No candidate is consumed on an early trigger.
	var due bool
	if err = tx.QueryRowContext(ctx, "SELECT confirm_deadline<=CURRENT_TIMESTAMP(3) FROM errand WHERE id=?", c.ID).Scan(&due); err != nil {
		return "", err
	}
	if !due {
		return "SKIPPED", nil
	}
	if c.Round == 2147483647 {
		return "", fmt.Errorf("round exhausted")
	}
	status, outcome := "PUBLISHED", "REVERTED"
	if next != nil {
		status, outcome = "LOCKED", "TRANSFERRED"
		_, err = tx.ExecContext(ctx, "UPDATE errand SET grabber_id=?,round=round+1,version=version+1,locked_at=CURRENT_TIMESTAMP(3),confirm_deadline=TIMESTAMPADD(SECOND,300,CURRENT_TIMESTAMP(3)) WHERE id=?", runner, c.ID)
		if err == nil {
			var record int64
			record, err = g.IDs.Next(ctx)
			if err == nil {
				_, err = tx.ExecContext(ctx, "INSERT INTO grab_record (id,campus_id,errand_id,runner_id,seq,round,result) VALUES (?,?,?,?,1,?,'GRABBED')", record, c.CampusID, c.ID, runner, c.Round+1)
			}
		}
		if err == nil {
			err = enqueueTimeout(ctx, tx, c.ID)
		}
	} else {
		_, err = tx.ExecContext(ctx, "UPDATE errand SET status='PUBLISHED',grabber_id=NULL,slot_taken=0,round=round+1,version=version+1,locked_at=NULL,confirm_deadline=NULL WHERE id=?", c.ID)
		if err == nil && c.GrabberID != nil {
			// Stronger than the original best-effort credit side effect: unique event and
			// score change commit with the re-pool, so a crash cannot lose or double it.
			var result sql.Result
			result, err = tx.ExecContext(ctx, "INSERT INTO credit_event (user_id,errand_id,round,kind,delta) VALUES (?,?,?,'GRAB_TIMEOUT_REVERT',-5) ON DUPLICATE KEY UPDATE id=id", *c.GrabberID, c.ID, c.Round)
			if err == nil {
				var n int64
				n, err = result.RowsAffected()
				if err == nil && n == 1 {
					_, err = tx.ExecContext(ctx, "INSERT INTO credit_score (user_id,score,version) VALUES (?,55,1) ON DUPLICATE KEY UPDATE score=GREATEST(0,score-5),version=version+1", *c.GrabberID)
				}
			}
		}
	}
	if err != nil {
		return "", err
	}
	actor := runner
	if next == nil {
		actor = -1
	}
	if err = transitionEffects(ctx, tx, c, status, c.Round+1, actor); err != nil {
		return "", err
	}
	if hook != nil {
		hook("before_commit")
	}
	if err = tx.Commit(); err != nil {
		return "", err
	} // Ambiguous commit: leave candidate untouched; query SQL on retry.
	if hook != nil {
		hook("committed")
	}
	_ = g.syncProjection(ctx, c.ID)
	_ = g.invalidateDetail(ctx, c.ID)
	return outcome, nil
}
func (g *Grabber) ScanDue(ctx context.Context, limit int) error {
	if limit < 1 || limit > 100 {
		return fmt.Errorf("invalid scan limit")
	}
	rows, err := g.DB.QueryContext(ctx, "SELECT id,round FROM errand WHERE status='LOCKED' AND confirm_deadline<=CURRENT_TIMESTAMP(3) ORDER BY confirm_deadline,id LIMIT ?", limit)
	if err != nil {
		return err
	}
	type task struct {
		id    int64
		round int
	}
	tasks := []task{}
	for rows.Next() {
		var t task
		if err = rows.Scan(&t.id, &t.round); err != nil {
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
		if _, err = g.HandleTimeout(ctx, t.id, t.round); err != nil {
			return err
		}
	}
	return nil
}
