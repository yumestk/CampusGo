package errand

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	taskdomain "github.com/yumestk/CampusGo/internal/domain/errand"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/yumestk/CampusGo/internal/auth"
	"github.com/yumestk/CampusGo/internal/platform/fault"
	"github.com/yumestk/CampusGo/internal/platform/idgen"
)

type Grabber struct {
	DB        *sql.DB
	Redis     *redis.Client
	IDs       *idgen.Generator
	Namespace string // Database name; never mix live and verification projections.
	Limits    *GrabLimits
}
type GrabCommand struct {
	ErrandID      int64 `json:"errandId,string"`
	ExpectedRound int   `json:"expectedRound"`
}
type GrabResult struct {
	RequestKey       string `json:"requestKey"`
	ErrandID         int64  `json:"errandId,string"`
	State            string `json:"state"`
	ErrorCode        string `json:"errorCode"`
	AppliedRound     int    `json:"appliedRound"`
	RetryAfterMillis int    `json:"retryAfterMillis"`
}
type grabRequest struct {
	ID, CampusID, RunnerID, ErrandID, BaseVersion int64
	Key, Token, State, Code                       string
	Fingerprint                                   []byte
	Round                                         int
	LeaseUntil                                    time.Time
}

const grabRequestColumns = "id,campus_id,runner_id,errand_id,base_version,request_key,reservation_token,state,error_code,fingerprint,round,lease_until"

func scanGrab(row scanner) (grabRequest, error) {
	var r grabRequest
	err := row.Scan(&r.ID, &r.CampusID, &r.RunnerID, &r.ErrandID, &r.BaseVersion, &r.Key, &r.Token, &r.State, &r.Code, &r.Fingerprint, &r.Round, &r.LeaseUntil)
	return r, err
}
func (r grabRequest) result() GrabResult {
	retry := 0
	if r.State == "PROCESSING" {
		retry = 1000
	}
	return GrabResult{r.Key, r.ErrandID, r.State, r.Code, r.Round, retry}
}
func unknown(err error) error {
	return &fault.Error{Code: "GRAB_RESULT_UNKNOWN", Status: 503, Cause: err}
}
func (g *Grabber) Grab(ctx context.Context, u auth.User, key string, c GrabCommand) (GrabResult, error) {
	if !ValidRequestKey(key) || c.ErrandID <= 0 || c.ExpectedRound < 0 || c.ExpectedRound > 2147483647 {
		return GrabResult{}, fault.New("INVALID_REQUEST", 400)
	}
	release, err := g.Limits.admit(ctx, u.ID, c.ErrandID, false)
	if err != nil {
		return GrabResult{}, err
	}
	defer release()
	return g.grab(ctx, u, key, c, nil)
}

// Hooks are test-only and unreachable through HTTP/configuration. Subprocess tests
// kill the process at these boundaries, leaving real SQL/Redis state behind.
func (g *Grabber) grab(ctx context.Context, u auth.User, key string, c GrabCommand, hook func(string)) (GrabResult, error) {
	if !ValidRequestKey(key) || c.ErrandID <= 0 || c.ExpectedRound < 0 || c.ExpectedRound > 2147483647 {
		return GrabResult{}, fault.New("INVALID_REQUEST", 400)
	}
	data, _ := json.Marshal(c)
	hash := sha256.Sum256(data)
	r, owner, err := g.registerGrab(ctx, u, key, c, hash[:])
	if err != nil {
		return GrabResult{}, err
	}
	if !owner {
		return g.observe(ctx, r)
	}
	if hook != nil {
		hook("registered")
	}
	// Business checks are authoritative SQL checks; Redis can only pre-admit execution.
	reason, err := g.grabEligibility(ctx, g.DB, u, c, r.BaseVersion)
	if err != nil {
		return r.result(), fault.Dependency(err)
	}
	if reason != "" {
		return g.finishGrab(ctx, r, "REJECTED", reason)
	}
	if err = g.syncProjection(ctx, c.ErrandID); err != nil {
		return r.result(), &fault.Error{Code: "DEPENDENCY_UNAVAILABLE", Status: 503, Cause: err}
	}
	acquired, err := g.acquire(ctx, r)
	if err != nil {
		return r.result(), &fault.Error{Code: "DEPENDENCY_UNAVAILABLE", Status: 503, Cause: err}
	}
	switch acquired {
	case 0:
	case 1, 4:
		return r.result(), nil
	case 2, 3:
		reason, e := g.grabEligibility(ctx, g.DB, u, c, r.BaseVersion)
		if e != nil {
			return r.result(), fault.Dependency(e)
		}
		if reason != "" {
			return g.finishGrab(ctx, r, "REJECTED", reason)
		}
		return r.result(), nil // A Redis-only busy state is provisional, never proof of a SQL loser.
	case 5, 6:
		return g.finishGrab(ctx, r, "EXPIRED", "REQUEST_EXPIRED")
	default:
		return r.result(), fault.New("INTERNAL_ERROR", 500)
	}
	if hook != nil {
		hook("reserved")
	}
	result, err := g.commitGrab(ctx, u, r, hook)
	if err != nil {
		return r.result(), err
	} // COMMIT ambiguity must never release the reservation.
	if hook != nil {
		hook("committed")
	}
	// Repair is already durable in the business transaction. These are latency optimizations.
	_ = g.syncProjection(ctx, c.ErrandID)
	_ = g.invalidateDetail(ctx, c.ErrandID)
	if hook != nil {
		hook("projected")
	}
	return result, nil
}
func (g *Grabber) registerGrab(ctx context.Context, u auth.User, key string, c GrabCommand, hash []byte) (grabRequest, bool, error) {
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

func (g *Grabber) grabEligibility(ctx context.Context, q rowQuery, u auth.User, c GrabCommand, version int64) (string, error) {
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
func (g *Grabber) commitGrab(ctx context.Context, u auth.User, r grabRequest, hook func(string)) (GrabResult, error) {
	tx, err := g.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return r.result(), fault.Dependency(err)
	}
	defer tx.Rollback()
	current, now, err := lockGrab(ctx, tx, r.ID)
	if err != nil {
		return r.result(), fault.Dependency(err)
	}
	if current.State != "PROCESSING" {
		return current.result(), nil
	}
	if current.Token != r.Token {
		return current.result(), nil
	}
	if !current.LeaseUntil.After(now) {
		return g.finishLocked(ctx, tx, current, "EXPIRED", "REQUEST_EXPIRED")
	}
	var campus int64
	var role string
	var active bool
	if err = tx.QueryRowContext(ctx, "SELECT campus_id,role,active FROM app_user WHERE id=? FOR SHARE", u.ID).Scan(&campus, &role, &active); err != nil {
		return r.result(), fault.Dependency(err)
	}
	if !active || campus != u.CampusID || (role != "STUDENT" && role != "ARBITRATOR") {
		return g.finishLocked(ctx, tx, current, "REJECTED", "ERRAND_NOT_GRABBABLE")
	}
	reason, err := g.grabEligibility(ctx, tx, u, GrabCommand{r.ErrandID, r.Round}, r.BaseVersion)
	if err != nil {
		return r.result(), fault.Dependency(err)
	}
	if reason != "" {
		return g.finishLocked(ctx, tx, current, "REJECTED", reason)
	}
	update, err := tx.ExecContext(ctx, "UPDATE errand SET status='LOCKED',grabber_id=?,slot_taken=1,version=version+1,locked_at=CURRENT_TIMESTAMP(3),confirm_deadline=TIMESTAMPADD(SECOND,300,CURRENT_TIMESTAMP(3)) WHERE id=? AND campus_id=? AND status='PUBLISHED' AND slot_taken=0 AND version=? AND round=?", u.ID, r.ErrandID, r.CampusID, r.BaseVersion, r.Round)
	if err != nil {
		return r.result(), fault.Dependency(err)
	}
	n, err := update.RowsAffected()
	if err != nil {
		return r.result(), fault.Dependency(err)
	}
	if n != 1 {
		return g.finishLocked(ctx, tx, current, "REJECTED", "GRAB_CONFLICT")
	}
	recordID, err := g.IDs.Next(ctx)
	if err != nil {
		return r.result(), fault.Dependency(err)
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO grab_record (id,campus_id,errand_id,runner_id,seq,round,result,request_id) VALUES (?,?,?,?,1,?,'GRABBED',?)", recordID, r.CampusID, r.ErrandID, r.RunnerID, r.Round, r.ID); err != nil {
		return r.result(), fault.Dependency(err)
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO errand_status_log (campus_id,errand_id,from_status,to_status,round,operator_id) VALUES (?,?,'PUBLISHED','LOCKED',?,?)", r.CampusID, r.ErrandID, r.Round, r.RunnerID); err != nil {
		return r.result(), fault.Dependency(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO local_message (campus_id,errand_id,msg_key,topic,payload,deliver_at) SELECT campus_id,id,CONCAT('confirm-timeout:',id,':',round),'errand-confirm-timeout',JSON_OBJECT('errandId',CAST(id AS CHAR),'campusId',CAST(campus_id AS CHAR),'round',round,'version',CAST(version AS CHAR),'deadline',DATE_FORMAT(confirm_deadline,'%Y-%m-%dT%H:%i:%s.%fZ')),confirm_deadline FROM errand WHERE id=?`, r.ErrandID); err != nil {
		return r.result(), fault.Dependency(err)
	}
	if err = enqueueRepair(ctx, tx, r.CampusID, r.ErrandID, fmt.Sprintf("projection:%d:%d", r.ErrandID, r.BaseVersion+1), "PROJECTION", nil, 0); err != nil {
		return r.result(), fault.Dependency(err)
	}
	for _, delay := range []int{0, 2} {
		if err = enqueueRepair(ctx, tx, r.CampusID, r.ErrandID, fmt.Sprintf("cache:%d:%d:%d", r.ErrandID, r.BaseVersion+1, delay), "CACHE_INVALIDATE", nil, delay); err != nil {
			return r.result(), fault.Dependency(err)
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
func (g *Grabber) finishLocked(ctx context.Context, tx *sql.Tx, r grabRequest, state, code string) (GrabResult, error) {
	// Recheck the database clock after all writes, not only when the row lock was acquired.
	where := ""
	if state != "EXPIRED" {
		where = " AND lease_until>CURRENT_TIMESTAMP(3)"
	}
	updated, err := tx.ExecContext(ctx, "UPDATE grab_request SET state=?,error_code=? WHERE id=? AND state='PROCESSING' AND reservation_token=?"+where, state, code, r.ID, r.Token)
	if err != nil {
		return r.result(), fault.Dependency(err)
	}
	n, err := updated.RowsAffected()
	if err != nil {
		return r.result(), fault.Dependency(err)
	}
	if n != 1 {
		return r.result(), fault.New("DEPENDENCY_UNAVAILABLE", 503)
	} // rollback all partial business writes
	if state != "SUCCEEDED" {
		if err = enqueueRepair(ctx, tx, r.CampusID, r.ErrandID, fmt.Sprintf("revoke:%d:%s", r.ID, r.Token), "REVOKE", &r.ID, 0); err != nil {
			return r.result(), fault.Dependency(err)
		}
	}
	if err = tx.Commit(); err != nil {
		return r.result(), unknown(err)
	}
	r.State, r.Code = state, code
	if state == "REJECTED" && (code == "SLOT_FULL" || code == "GRAB_CONFLICT" || code == "STALE_VERSION") {
		_ = g.offerCandidate(ctx, r)
	}
	return r.result(), nil
}
func (g *Grabber) finishGrab(ctx context.Context, r grabRequest, state, code string) (GrabResult, error) {
	tx, err := g.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return r.result(), fault.Dependency(err)
	}
	defer tx.Rollback()
	current, now, err := lockGrab(ctx, tx, r.ID)
	if err != nil {
		return r.result(), fault.Dependency(err)
	}
	if current.State != "PROCESSING" || current.Token != r.Token {
		return current.result(), nil
	}
	if state == "EXPIRED" && current.LeaseUntil.After(now) {
		return current.result(), nil
	}
	if !current.LeaseUntil.After(now) {
		state, code = "EXPIRED", "REQUEST_EXPIRED"
	}
	return g.finishLocked(ctx, tx, current, state, code)
}
func (g *Grabber) observe(ctx context.Context, r grabRequest) (GrabResult, error) {
	if r.State != "PROCESSING" {
		return r.result(), nil
	}
	// Always use the primary's clock and serialize against an in-flight business transaction.
	var expired bool
	if err := g.DB.QueryRowContext(ctx, "SELECT lease_until<=CURRENT_TIMESTAMP(3) FROM grab_request WHERE id=?", r.ID).Scan(&expired); err != nil {
		return r.result(), fault.Dependency(err)
	}
	if expired {
		return g.finishGrab(ctx, r, "EXPIRED", "REQUEST_EXPIRED")
	}
	return r.result(), nil
}
func (g *Grabber) Result(ctx context.Context, u auth.User, key string, id int64) (GrabResult, error) {
	if !ValidRequestKey(key) || id <= 0 {
		return GrabResult{}, fault.New("INVALID_REQUEST", 400)
	}
	release, err := g.Limits.admit(ctx, u.ID, id, true)
	if err != nil {
		return GrabResult{}, err
	}
	defer release()
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
