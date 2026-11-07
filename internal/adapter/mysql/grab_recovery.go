package mysql

import (
	"github.com/yumestk/CampusGo/internal/contracts"

	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

func enqueueRepair(ctx context.Context, tx *sql.Tx, campus, id int64, event, kind string, request *int64, delay int) error {
	// DONE may be re-opened when a later SQL/Redis consistency scan finds drift.
	// DEAD remains visible until an operator supplies a reason for replay.
	_, err := tx.ExecContext(ctx, `INSERT INTO grab_repair_task (campus_id,errand_id,event_key,kind,request_id,next_run_at) VALUES (?,?,?,?,?,TIMESTAMPADD(SECOND,?,CURRENT_TIMESTAMP(3))) ON DUPLICATE KEY UPDATE retry_count=IF(state='DONE',0,retry_count),next_run_at=IF(state='DONE',VALUES(next_run_at),next_run_at),state=IF(state='DONE','PENDING',state)`, campus, id, event, kind, request, delay)
	return err
}

const repairColumns = "id,campus_id,errand_id,event_key,kind,request_id,state,retry_count,replay_count,COALESCE(worker_token,''),COALESCE(last_error,''),COALESCE(last_replay_reason,'')"

func scanRepair(row scanner) (RepairTask, error) {
	var r RepairTask
	err := row.Scan(&r.ID, &r.CampusID, &r.ErrandID, &r.EventKey, &r.Kind, &r.RequestID, &r.State, &r.Attempts, &r.ReplayCount, &r.WorkerToken, &r.LastError, &r.ReplayReason)
	return r, err
}
func (g *TaskStore) ClaimRepairs(ctx context.Context, limit int) ([]RepairTask, error) {
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("repair limit must be in [1,100]")
	}
	tx, err := g.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT "+repairColumns+" FROM grab_repair_task WHERE (state='PENDING' AND next_run_at<=CURRENT_TIMESTAMP(3)) OR (state='RUNNING' AND locked_until<=CURRENT_TIMESTAMP(3)) ORDER BY next_run_at,id LIMIT ? FOR UPDATE SKIP LOCKED", limit)
	if err != nil {
		return nil, err
	}
	tasks := []RepairTask{}
	for rows.Next() {
		r, e := scanRepair(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		tasks = append(tasks, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	claimed := []RepairTask{}
	for _, r := range tasks {
		if r.Attempts >= 10 {
			if _, err = tx.ExecContext(ctx, "UPDATE grab_repair_task SET state='DEAD',worker_token=NULL,locked_until=NULL,last_error='attempt budget exhausted after worker lease loss' WHERE id=?", r.ID); err != nil {
				return nil, err
			}
			continue
		}
		r.WorkerToken = uuid.NewString()
		r.Attempts++
		r.State = "RUNNING"
		if _, err = tx.ExecContext(ctx, "UPDATE grab_repair_task SET state='RUNNING',worker_token=?,locked_until=TIMESTAMPADD(SECOND,30,CURRENT_TIMESTAMP(3)),retry_count=retry_count+1 WHERE id=?", r.WorkerToken, r.ID); err != nil {
			return nil, err
		}
		claimed = append(claimed, r)
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return claimed, nil
}
func (g *TaskStore) CompleteRepair(ctx context.Context, r RepairTask, cause error) (bool, error) {
	var result sql.Result
	var err error
	if cause == nil {
		result, err = g.DB.ExecContext(ctx, "UPDATE grab_repair_task SET state='DONE',worker_token=NULL,locked_until=NULL,last_error=NULL WHERE id=? AND state='RUNNING' AND worker_token=? AND locked_until>CURRENT_TIMESTAMP(3)", r.ID, r.WorkerToken)
	} else {
		// Store a bounded error class, not a Redis URL, SQL statement or credential.
		safe := fmt.Sprintf("%T", cause)
		result, err = g.DB.ExecContext(ctx, "UPDATE grab_repair_task SET state=IF(retry_count>=10,'DEAD','PENDING'),next_run_at=TIMESTAMPADD(SECOND,LEAST(POW(2,retry_count),300),CURRENT_TIMESTAMP(3)),worker_token=NULL,locked_until=NULL,last_error=? WHERE id=? AND state='RUNNING' AND worker_token=? AND locked_until>CURRENT_TIMESTAMP(3)", safe, r.ID, r.WorkerToken)
	}
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}
func (g *TaskStore) ApplyRepair(ctx context.Context, r RepairTask) error {
	switch r.Kind {
	case "PROJECTION":
		return g.syncProjection(ctx, r.ErrandID)
	case "CACHE_INVALIDATE":
		return g.invalidateDetail(ctx, r.ErrandID)
	case "REVOKE":
		if r.RequestID == nil {
			return fmt.Errorf("revoke has no request")
		}
		request, err := scanGrab(g.DB.QueryRowContext(ctx, "SELECT "+grabRequestColumns+" FROM grab_request WHERE id=? AND campus_id=? AND errand_id=?", *r.RequestID, r.CampusID, r.ErrandID))
		if err != nil {
			return err
		}
		if request.State == "PROCESSING" {
			return fmt.Errorf("refuse to revoke processing request")
		}
		if err = g.syncProjection(ctx, r.ErrandID); err != nil {
			return err
		}
		return g.revoke(ctx, request) // exact request, token, version and round; never committed state
	default:
		return fmt.Errorf("unknown repair kind")
	}
}

// Keyset scan covers Redis eviction, a full Redis restart, and missing repair
// registrations left by an old application. Each worker may scan independently;
// SQL event uniqueness and fenced projection scripts make duplicates harmless.
func (g *TaskStore) ReconcileBatch(ctx context.Context, after int64, limit int) (int64, error) {
	if after < 0 || limit < 1 || limit > 100 {
		return after, fmt.Errorf("invalid reconciliation cursor or limit")
	}
	rows, err := g.DB.QueryContext(ctx, "SELECT "+columns+" FROM errand WHERE id>? ORDER BY id LIMIT ?", after, limit)
	if err != nil {
		return after, err
	}
	cards := []Card{}
	for rows.Next() {
		c, e := scan(rows)
		if e != nil {
			rows.Close()
			return after, e
		}
		cards = append(cards, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return after, err
	}
	next := int64(0)
	for _, c := range cards {
		next = c.ID
		hash, e := g.Redis.HGetAll(ctx, g.stateKey(c.ID)).Result()
		if e != nil {
			return after, e
		}
		expected := projectionArgs(c)
		fields := []string{"dbVersion", "round", "status", "slotTaken", "ownerRunnerId"}
		drift := hash["mode"] != "READY"
		for i, f := range fields {
			drift = drift || hash[f] != fmt.Sprint(expected[i])
		}
		// A terminal request can leave a same-version pending after a crash before acquire returns.
		if token := hash["reservationToken"]; token != "" {
			request, e := scanGrab(g.DB.QueryRowContext(ctx, "SELECT "+grabRequestColumns+" FROM grab_request WHERE id=? AND errand_id=?", hash["requestDbId"], c.ID))
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return after, e
			}
			if e == nil && request.Token == token && request.State != "PROCESSING" {
				tx, e := g.DB.BeginTx(ctx, nil)
				if e != nil {
					return after, e
				}
				e = enqueueRepair(ctx, tx, c.CampusID, c.ID, fmt.Sprintf("revoke:%d:%s", request.ID, token), "REVOKE", &request.ID, 0)
				if e == nil {
					e = tx.Commit()
				} else {
					tx.Rollback()
				}
				if e != nil {
					return after, e
				}
			}
		}
		if drift {
			tx, e := g.DB.BeginTx(ctx, nil)
			if e != nil {
				return after, e
			}
			e = enqueueRepair(ctx, tx, c.CampusID, c.ID, fmt.Sprintf("projection:%d:%d", c.ID, c.Version), "PROJECTION", nil, 0)
			if e == nil {
				e = tx.Commit()
			} else {
				tx.Rollback()
			}
			if e != nil {
				return after, e
			}
		}
		// Cache is version validated on each read. Reconciliation also removes old bodies.
		if err = g.reconcileDetail(ctx, c); err != nil {
			return after, err
		}
	}
	if len(cards) < limit {
		next = 0
	}
	return next, nil
}
func (g *TaskStore) DeadRepairs(ctx context.Context) ([]RepairTask, error) {
	rows, err := g.DB.QueryContext(ctx, "SELECT "+repairColumns+" FROM grab_repair_task WHERE state='DEAD' ORDER BY id LIMIT 100")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := []RepairTask{}
	for rows.Next() {
		r, e := scanRepair(rows)
		if e != nil {
			return nil, e
		}
		tasks = append(tasks, r)
	}
	return tasks, rows.Err()
}
func (g *TaskStore) ReplayRepair(ctx context.Context, id int64, reason string) (bool, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 200 || strings.ContainsAny(reason, "\n\r") {
		return false, fmt.Errorf("replay requires a 1-200 byte single-line reason")
	}
	r, err := g.DB.ExecContext(ctx, "UPDATE grab_repair_task SET state='PENDING',retry_count=0,replay_count=replay_count+1,last_replay_reason=?,next_run_at=CURRENT_TIMESTAMP(3),worker_token=NULL,locked_until=NULL WHERE id=? AND state='DEAD'", reason, id)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n == 1, err
}

type RepairTask = contracts.RepairTask

func (g *TaskStore) ExpiredGrabs(ctx context.Context, limit int) ([]grabRequest, error) {
	rows, err := g.DB.QueryContext(ctx, "SELECT "+grabRequestColumns+" FROM grab_request WHERE state='PROCESSING' AND lease_until<=CURRENT_TIMESTAMP(3) ORDER BY lease_until,id LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	expired := []grabRequest{}
	for rows.Next() {
		r, e := scanGrab(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		expired = append(expired, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}

	return expired, nil
}
