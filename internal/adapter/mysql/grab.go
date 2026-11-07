package mysql

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/yumestk/CampusGo/internal/auth"
	"github.com/yumestk/CampusGo/internal/contracts"
	taskdomain "github.com/yumestk/CampusGo/internal/domain/errand"
	"github.com/yumestk/CampusGo/internal/platform/fault"
	"github.com/yumestk/CampusGo/internal/platform/idgen"
)

type TaskStore struct {
	DB          *sql.DB
	Redis       *redis.Client
	IDs         *idgen.Generator
	Namespace   string // Database name; never mix live and verification projections.
	Limits      *GrabLimits
	grabHook    func(string)
	timeoutHook func(string)
}

const grabRequestColumns = "id,campus_id,runner_id,errand_id,base_version,request_key,reservation_token,state,error_code,fingerprint,round,lease_until"

func scanGrab(row scanner) (grabRequest, error) {
	var r grabRequest
	err := row.Scan(&r.ID, &r.CampusID, &r.RunnerID, &r.ErrandID, &r.BaseVersion, &r.Key, &r.Token, &r.State, &r.Code, &r.Fingerprint, &r.Round, &r.LeaseUntil)
	return r, err
}

func unknown(err error) error {
	return &fault.Error{Code: "GRAB_RESULT_UNKNOWN", Status: 503, Cause: err}
}

// Hooks are test-only and unreachable through HTTP/configuration. Subprocess tests
// kill the process at these boundaries, leaving real SQL/Redis state behind.

func (g *TaskStore) registerGrab(ctx context.Context, u auth.User, key string, c GrabCommand, hash []byte) (grabRequest, bool, error) {
	tx, err := g.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return grabRequest{}, false, fault.Dependency(err)
	}
	defer tx.Rollback()
	// An existing request is replayable even after its round has changed.
	existing, err := scanGrab(tx.QueryRowContext(ctx, "SELECT "+grabRequestColumns+" FROM grab_request WHERE campus_id=? AND runner_id=? AND request_key=?", u.CampusID, u.ID, key))
	if err == nil {
		if !bytes.Equal(hash, existing.Fingerprint) {
			return existing, false, fault.New("IDEMPOTENCY_CONFLICT", 409)
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return grabRequest{}, false, fault.Dependency(err)
	}
	var version int64
	err = tx.QueryRowContext(ctx, "SELECT version FROM errand WHERE id=? AND campus_id=?", c.ErrandID, u.CampusID).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return grabRequest{}, false, fault.New("ERRAND_NOT_FOUND", 404)
	}
	if err != nil {
		return grabRequest{}, false, fault.Dependency(err)
	}
	id, err := g.IDs.Next(ctx)
	if err != nil {
		return grabRequest{}, false, fault.Dependency(err)
	}
	inserted, err := tx.ExecContext(ctx, "INSERT INTO grab_request (id,campus_id,runner_id,request_key,errand_id,fingerprint,base_version,round,reservation_token,lease_until) VALUES (?,?,?,?,?,?,?,?,?,TIMESTAMPADD(SECOND,30,CURRENT_TIMESTAMP(3))) ON DUPLICATE KEY UPDATE request_key=request_key", id, u.CampusID, u.ID, key, c.ErrandID, hash, version, c.ExpectedRound, uuid.NewString())
	if err != nil {
		return grabRequest{}, false, fault.Dependency(err)
	}
	r, err := scanGrab(tx.QueryRowContext(ctx, "SELECT "+grabRequestColumns+" FROM grab_request WHERE campus_id=? AND runner_id=? AND request_key=? FOR UPDATE", u.CampusID, u.ID, key))
	if err != nil {
		return r, false, fault.Dependency(err)
	}
	if !bytes.Equal(hash, r.Fingerprint) {
		return r, false, fault.New("IDEMPOTENCY_CONFLICT", 409)
	}
	n, err := inserted.RowsAffected()
	if err != nil {
		return r, false, fault.Dependency(err)
	}
	if err = tx.Commit(); err != nil {
		return r, false, unknown(err)
	}
	return r, n == 1, nil
}

type rowQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (g *TaskStore) grabEligibility(ctx context.Context, q rowQuery, u auth.User, c GrabCommand, version int64) (string, error) {
	var campus, publisher, currentVersion int64
	var round, slot int
	var status string
	var grabber sql.NullInt64
	err := q.QueryRowContext(ctx, "SELECT campus_id,publisher_id,grabber_id,version,round,slot_total,status FROM errand WHERE id=?", c.ErrandID).Scan(&campus, &publisher, &grabber, &currentVersion, &round, &slot, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return "ERRAND_NOT_FOUND", nil
	}
	if err != nil {
		return "", err
	}
	task := taskdomain.Restore(taskdomain.Snapshot{CampusID: campus, PublisherID: publisher, RunnerID: grabber.Int64, Version: currentVersion, Round: round, Slots: slot, Status: status})
	if reason := task.Grab(taskActor(u), c.ExpectedRound, version); reason != "" {
		return reason, nil
	}
	var score, ongoing int
	err = q.QueryRowContext(ctx, "SELECT COALESCE((SELECT score FROM credit_score WHERE user_id=?),60),(SELECT COUNT(*) FROM errand WHERE grabber_id=? AND status IN ('ACCEPTED','PICKED_UP'))", u.ID, u.ID).Scan(&score, &ongoing)
	if err != nil {
		return "", err
	}
	return taskdomain.Eligible(score, ongoing), nil
}
func (g *TaskStore) commitGrab(ctx context.Context, u auth.User, r grabRequest, hook func(string)) (GrabResult, error) {
	tx, err := g.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return r.Result(), fault.Dependency(err)
	}
	defer tx.Rollback()
	current, now, err := lockGrab(ctx, tx, r.ID)
	if err != nil {
		return r.Result(), fault.Dependency(err)
	}
	if current.State != "PROCESSING" {
		return current.Result(), nil
	}
	if current.Token != r.Token {
		return current.Result(), nil
	}
	if !current.LeaseUntil.After(now) {
		return g.finishLocked(ctx, tx, current, "EXPIRED", "REQUEST_EXPIRED")
	}
	var campus int64
	var role string
	var active bool
	if err = tx.QueryRowContext(ctx, "SELECT campus_id,role,active FROM app_user WHERE id=? FOR SHARE", u.ID).Scan(&campus, &role, &active); err != nil {
		return r.Result(), fault.Dependency(err)
	}
	if !active || campus != u.CampusID || (role != "STUDENT" && role != "ARBITRATOR") {
		return g.finishLocked(ctx, tx, current, "REJECTED", "ERRAND_NOT_GRABBABLE")
	}
	reason, err := g.grabEligibility(ctx, tx, u, GrabCommand{ErrandID: r.ErrandID, ExpectedRound: r.Round}, r.BaseVersion)
	if err != nil {
		return r.Result(), fault.Dependency(err)
	}
	if reason != "" {
		return g.finishLocked(ctx, tx, current, "REJECTED", reason)
	}
	update, err := tx.ExecContext(ctx, "UPDATE errand SET status='LOCKED',grabber_id=?,slot_taken=1,version=version+1,locked_at=CURRENT_TIMESTAMP(3),confirm_deadline=TIMESTAMPADD(SECOND,300,CURRENT_TIMESTAMP(3)) WHERE id=? AND campus_id=? AND status='PUBLISHED' AND slot_taken=0 AND version=? AND round=?", u.ID, r.ErrandID, r.CampusID, r.BaseVersion, r.Round)
	if err != nil {
		return r.Result(), fault.Dependency(err)
	}
	n, err := update.RowsAffected()
	if err != nil {
		return r.Result(), fault.Dependency(err)
	}
	if n != 1 {
		return g.finishLocked(ctx, tx, current, "REJECTED", "GRAB_CONFLICT")
	}
	recordID, err := g.IDs.Next(ctx)
	if err != nil {
		return r.Result(), fault.Dependency(err)
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO grab_record (id,campus_id,errand_id,runner_id,seq,round,result,request_id) VALUES (?,?,?,?,1,?,'GRABBED',?)", recordID, r.CampusID, r.ErrandID, r.RunnerID, r.Round, r.ID); err != nil {
		return r.Result(), fault.Dependency(err)
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO errand_status_log (campus_id,errand_id,from_status,to_status,round,operator_id) VALUES (?,?,'PUBLISHED','LOCKED',?,?)", r.CampusID, r.ErrandID, r.Round, r.RunnerID); err != nil {
		return r.Result(), fault.Dependency(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO local_message (campus_id,errand_id,msg_key,topic,payload,deliver_at) SELECT campus_id,id,CONCAT('confirm-timeout:',id,':',round),'errand-confirm-timeout',JSON_OBJECT('errandId',CAST(id AS CHAR),'campusId',CAST(campus_id AS CHAR),'round',round,'version',CAST(version AS CHAR),'deadline',DATE_FORMAT(confirm_deadline,'%Y-%m-%dT%H:%i:%s.%fZ')),confirm_deadline FROM errand WHERE id=?`, r.ErrandID); err != nil {
		return r.Result(), fault.Dependency(err)
	}
	if err = enqueueRepair(ctx, tx, r.CampusID, r.ErrandID, fmt.Sprintf("projection:%d:%d", r.ErrandID, r.BaseVersion+1), "PROJECTION", nil, 0); err != nil {
		return r.Result(), fault.Dependency(err)
	}
	for _, delay := range []int{0, 2} {
		if err = enqueueRepair(ctx, tx, r.CampusID, r.ErrandID, fmt.Sprintf("cache:%d:%d:%d", r.ErrandID, r.BaseVersion+1, delay), "CACHE_INVALIDATE", nil, delay); err != nil {
			return r.Result(), fault.Dependency(err)
		}
	}
	if hook != nil {
		hook("before_commit")
	}
	return g.finishLocked(ctx, tx, current, "SUCCEEDED", "")
}
func lockGrab(ctx context.Context, tx *sql.Tx, id int64) (grabRequest, time.Time, error) {
	r, err := scanGrab(tx.QueryRowContext(ctx, "SELECT "+grabRequestColumns+" FROM grab_request WHERE id=? FOR UPDATE", id))
	if err != nil {
		return r, time.Time{}, err
	}
	var now time.Time
	err = tx.QueryRowContext(ctx, "SELECT CURRENT_TIMESTAMP(3)").Scan(&now)
	return r, now, err
}
func (g *TaskStore) finishLocked(ctx context.Context, tx *sql.Tx, r grabRequest, state, code string) (GrabResult, error) {
	// Recheck the database clock after all writes, not only when the row lock was acquired.
	where := ""
	if state != "EXPIRED" {
		where = " AND lease_until>CURRENT_TIMESTAMP(3)"
	}
	updated, err := tx.ExecContext(ctx, "UPDATE grab_request SET state=?,error_code=? WHERE id=? AND state='PROCESSING' AND reservation_token=?"+where, state, code, r.ID, r.Token)
	if err != nil {
		return r.Result(), fault.Dependency(err)
	}
	n, err := updated.RowsAffected()
	if err != nil {
		return r.Result(), fault.Dependency(err)
	}
	if n != 1 {
		return r.Result(), fault.New("DEPENDENCY_UNAVAILABLE", 503)
	} // rollback all partial business writes
	if state != "SUCCEEDED" {
		if err = enqueueRepair(ctx, tx, r.CampusID, r.ErrandID, fmt.Sprintf("revoke:%d:%s", r.ID, r.Token), "REVOKE", &r.ID, 0); err != nil {
			return r.Result(), fault.Dependency(err)
		}
	}
	if err = tx.Commit(); err != nil {
		return r.Result(), unknown(err)
	}
	r.State, r.Code = state, code
	if state == "REJECTED" && (code == "SLOT_FULL" || code == "GRAB_CONFLICT" || code == "STALE_VERSION") {
		_ = g.offerCandidate(ctx, r)
	}
	return r.Result(), nil
}
func (g *TaskStore) finishGrab(ctx context.Context, r grabRequest, state, code string) (GrabResult, error) {
	tx, err := g.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return r.Result(), fault.Dependency(err)
	}
	defer tx.Rollback()
	current, now, err := lockGrab(ctx, tx, r.ID)
	if err != nil {
		return r.Result(), fault.Dependency(err)
	}
	if current.State != "PROCESSING" || current.Token != r.Token {
		return current.Result(), nil
	}
	if state == "EXPIRED" && current.LeaseUntil.After(now) {
		return current.Result(), nil
	}
	if !current.LeaseUntil.After(now) {
		state, code = "EXPIRED", "REQUEST_EXPIRED"
	}
	return g.finishLocked(ctx, tx, current, state, code)
}
func (g *TaskStore) observe(ctx context.Context, r grabRequest) (GrabResult, error) {
	if r.State != "PROCESSING" {
		return r.Result(), nil
	}
	// Always use the primary's clock and serialize against an in-flight business transaction.
	var expired bool
	if err := g.DB.QueryRowContext(ctx, "SELECT lease_until<=CURRENT_TIMESTAMP(3) FROM grab_request WHERE id=?", r.ID).Scan(&expired); err != nil {
		return r.Result(), fault.Dependency(err)
	}
	if expired {
		return g.finishGrab(ctx, r, "EXPIRED", "REQUEST_EXPIRED")
	}
	return r.Result(), nil
}
func (g *TaskStore) GrabResult(ctx context.Context, u auth.User, key string, id int64) (GrabResult, error) {
	if !contracts.ValidRequestKey(key) || id <= 0 {
		return GrabResult{}, fault.New("INVALID_REQUEST", 400)
	}
	r, err := scanGrab(g.DB.QueryRowContext(ctx, "SELECT "+grabRequestColumns+" FROM grab_request WHERE campus_id=? AND runner_id=? AND request_key=?", u.CampusID, u.ID, key))
	if errors.Is(err, sql.ErrNoRows) {
		return GrabResult{RequestKey: key, ErrandID: id, State: "NOT_OBSERVED", RetryAfterMillis: 1000}, nil
	}
	if err != nil {
		return GrabResult{}, fault.Dependency(err)
	}
	if r.ErrandID != id {
		return GrabResult{}, fault.New("IDEMPOTENCY_CONFLICT", 409)
	}
	return g.observe(ctx, r)
}

type GrabCommand = contracts.GrabCommand

type GrabResult = contracts.GrabResult

type grabRequest = contracts.GrabRequest

func (g *TaskStore) Admit(ctx context.Context, user, task int64, query bool) (func(), error) {
	if g.Limits == nil {
		return nil, fault.New("DEPENDENCY_UNAVAILABLE", 503)
	}
	return g.Limits.admit(ctx, user, task, query)
}
func (g *TaskStore) RegisterGrab(ctx context.Context, u contracts.Actor, key string, c GrabCommand, hash []byte) (grabRequest, bool, error) {
	r, owner, err := g.registerGrab(ctx, u, key, c, hash)
	if err == nil && owner && g.grabHook != nil {
		g.grabHook("registered")
	}
	return r, owner, err
}
func (g *TaskStore) CheckGrab(ctx context.Context, u contracts.Actor, c GrabCommand, version int64) (string, error) {
	reason, err := g.grabEligibility(ctx, g.DB, u, c, version)
	return reason, fault.Dependency(err)
}
func (g *TaskStore) FinishGrab(ctx context.Context, r grabRequest, state, code string) (GrabResult, error) {
	return g.finishGrab(ctx, r, state, code)
}
func (g *TaskStore) ObserveGrab(ctx context.Context, r grabRequest) (GrabResult, error) {
	return g.observe(ctx, r)
}
func (g *TaskStore) CommitGrab(ctx context.Context, u contracts.Actor, r grabRequest) (GrabResult, error) {
	result, err := g.commitGrab(ctx, u, r, g.grabHook)
	if err == nil && g.grabHook != nil {
		g.grabHook("committed")
	}
	return result, err
}
func (g *TaskStore) SyncProjection(ctx context.Context, id int64) error {
	return g.syncProjection(ctx, id)
}
func (g *TaskStore) AcquireGrab(ctx context.Context, r grabRequest) (int64, error) {
	code, err := g.acquire(ctx, r)
	if err == nil && code == 0 && g.grabHook != nil {
		g.grabHook("reserved")
	}
	return code, err
}
func (g *TaskStore) PublishGrabProjection(ctx context.Context, id int64) {
	_ = g.syncProjection(ctx, id)
	_ = g.invalidateDetail(ctx, id)
	if g.grabHook != nil {
		g.grabHook("projected")
	}
}
