package errand

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

type TimeoutMessage struct {
	ID           int64     `json:"id,string"`
	CampusID     int64     `json:"campusId,string"`
	ErrandID     int64     `json:"errandId,string"`
	Key          string    `json:"key"`
	Payload      []byte    `json:"-"`
	DeliverAt    time.Time `json:"deliverAt"`
	State        string    `json:"state"`
	Attempts     int       `json:"attempts"`
	ReplayCount  int       `json:"replayCount"`
	Token        string    `json:"-"`
	LastError    string    `json:"lastError"`
	ReplayReason string    `json:"replayReason"`
}

const messageColumns = "id,campus_id,errand_id,msg_key,payload,deliver_at,state,retry_count,replay_count,COALESCE(worker_token,''),COALESCE(last_error,''),COALESCE(last_replay_reason,'')"

func scanMessage(row scanner) (TimeoutMessage, error) {
	var m TimeoutMessage
	err := row.Scan(&m.ID, &m.CampusID, &m.ErrandID, &m.Key, &m.Payload, &m.DeliverAt, &m.State, &m.Attempts, &m.ReplayCount, &m.Token, &m.LastError, &m.ReplayReason)
	return m, err
}
func (g *Grabber) ClaimMessages(ctx context.Context, limit int) ([]TimeoutMessage, error) {
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("invalid message limit")
	}
	tx, err := g.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT "+messageColumns+" FROM local_message WHERE topic='errand-confirm-timeout' AND ((state='PENDING' AND next_run_at<=CURRENT_TIMESTAMP(3)) OR (state='RUNNING' AND locked_until<=CURRENT_TIMESTAMP(3))) ORDER BY next_run_at,id LIMIT ? FOR UPDATE SKIP LOCKED", limit)
	if err != nil {
		return nil, err
	}
	tasks := []TimeoutMessage{}
	for rows.Next() {
		r, e := scanMessage(rows)
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
	claimed := []TimeoutMessage{}
	for _, r := range tasks {
		if r.Attempts >= 10 {
			_, err = tx.ExecContext(ctx, "UPDATE local_message SET state='DEAD',worker_token=NULL,locked_until=NULL,last_error='attempt budget exhausted after lease loss' WHERE id=?", r.ID)
		} else {
			r.Token = uuid.NewString()
			r.Attempts++
			r.State = "RUNNING"
			_, err = tx.ExecContext(ctx, "UPDATE local_message SET state='RUNNING',retry_count=retry_count+1,worker_token=?,locked_until=TIMESTAMPADD(SECOND,30,CURRENT_TIMESTAMP(3)) WHERE id=?", r.Token, r.ID)
			claimed = append(claimed, r)
		}
		if err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return claimed, nil
}
func (g *Grabber) CompleteMessage(ctx context.Context, m TimeoutMessage, cause error) (bool, error) {
	var result sql.Result
	var err error
	if cause == nil {
		result, err = g.DB.ExecContext(ctx, "UPDATE local_message SET state='DONE',worker_token=NULL,locked_until=NULL,last_error=NULL WHERE id=? AND state='RUNNING' AND worker_token=? AND locked_until>CURRENT_TIMESTAMP(3)", m.ID, m.Token)
	} else {
		result, err = g.DB.ExecContext(ctx, "UPDATE local_message SET state=IF(retry_count>=10,'DEAD','PENDING'),next_run_at=TIMESTAMPADD(SECOND,LEAST(POW(2,retry_count),300),CURRENT_TIMESTAMP(3)),worker_token=NULL,locked_until=NULL,last_error=? WHERE id=? AND state='RUNNING' AND worker_token=? AND locked_until>CURRENT_TIMESTAMP(3)", fmt.Sprintf("%T", cause), m.ID, m.Token)
	}
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}
func (g *Grabber) DispatchMessages(ctx context.Context, send func(context.Context, TimeoutMessage) error) error {
	messages, err := g.ClaimMessages(ctx, 10)
	if err != nil {
		return err
	}
	for _, m := range messages {
		sendCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		cause := send(sendCtx, m)
		cancel()
		if _, err = g.CompleteMessage(ctx, m, cause); err != nil {
			return err
		}
	}
	return nil
}
func (g *Grabber) DeadMessages(ctx context.Context) ([]TimeoutMessage, error) {
	rows, err := g.DB.QueryContext(ctx, "SELECT "+messageColumns+" FROM local_message WHERE state='DEAD' ORDER BY id LIMIT 100")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []TimeoutMessage{}
	for rows.Next() {
		r, e := scanMessage(rows)
		if e != nil {
			return nil, e
		}
		result = append(result, r)
	}
	return result, rows.Err()
}
func (g *Grabber) ReplayMessage(ctx context.Context, id int64, reason string) (bool, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 200 || strings.ContainsAny(reason, "\r\n") {
		return false, fmt.Errorf("replay requires 1-200 byte single-line reason")
	}
	r, err := g.DB.ExecContext(ctx, "UPDATE local_message SET state='PENDING',retry_count=0,replay_count=replay_count+1,last_replay_reason=?,next_run_at=CURRENT_TIMESTAMP(3),worker_token=NULL,locked_until=NULL WHERE id=? AND state='DEAD'", reason, id)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n == 1, err
}

// Decode strictly, then compare all supplied fields to durable facts. A valid
// delayed message is only a wake-up; SQL decides whether its round is still due.
func (g *Grabber) ConsumeTimeout(ctx context.Context, body []byte) (string, error) {
	var payload struct {
		ErrandID string `json:"errandId"`
		CampusID string `json:"campusId"`
		Round    int    `json:"round"`
		Version  string `json:"version"`
		Deadline string `json:"deadline"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if len(body) > 4096 || decoder.Decode(&payload) != nil {
		return "INVALID", nil
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return "INVALID", nil
	}
	id, e := strconv.ParseInt(payload.ErrandID, 10, 64)
	campus, e2 := strconv.ParseInt(payload.CampusID, 10, 64)
	version, e3 := strconv.ParseInt(payload.Version, 10, 64)
	deadline, e4 := time.Parse(time.RFC3339Nano, payload.Deadline)
	if e != nil || e2 != nil || e3 != nil || e4 != nil || id <= 0 || campus <= 0 || version < 0 || payload.Round < 0 {
		return "INVALID", nil
	}
	var valid bool
	if e = g.DB.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM errand WHERE id=? AND campus_id=? AND status='LOCKED' AND round=? AND version=? AND confirm_deadline=?)", id, campus, payload.Round, version, deadline.UTC()).Scan(&valid); e != nil {
		return "", e
	}
	if !valid {
		return "SKIPPED", nil
	}
	return g.HandleTimeout(ctx, id, payload.Round)
}
func (g *Grabber) RunTimeoutScan(ctx context.Context, onError func(error)) {
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		batch, cancel := context.WithTimeout(ctx, 8*time.Second)
		if err := g.ScanDue(batch, 50); err != nil {
			onError(err)
		}
		cancel()
	}
}
