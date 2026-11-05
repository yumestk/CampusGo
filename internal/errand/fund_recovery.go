package errand

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

func decodeFund(body []byte) (FundEvent, error) {
	var e FundEvent
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if len(body) > 4096 || d.Decode(&e) != nil || d.Decode(new(any)) != io.EOF || !ValidRequestKey(e.RequestID) || e.Amount <= 0 || e.CampusID <= 0 || e.ErrandID <= 0 || e.PublisherID <= 0 || e.Round < 0 || !validFundCommandID(e) {
		return e, fmt.Errorf("invalid funds event")
	}
	return e, nil
}
func validFundCommandID(e FundEvent) bool {
	switch e.Kind {
	case "CANCEL":
		return e.ActorID == e.PublisherID && e.Commission == 0
	case "ARBITRATE_PUBLISHER":
		return e.ActorID > 0 && e.RunnerID > 0 && e.Commission == 0
	case "SETTLE":
		return e.ActorID == e.PublisherID && e.RunnerID > 0 && e.Commission == Commission(e.Amount)
	case "AUTO_SETTLE":
		return e.ActorID == -1 && e.RunnerID > 0 && e.Commission == Commission(e.Amount)
	case "ARBITRATE_RUNNER":
		return e.ActorID > 0 && e.RunnerID > 0 && e.Commission == Commission(e.Amount)
	}
	return false
}

// Check locks the durable request to distinguish an expired non-execution from
// an in-flight transaction. A timeout/error/absent fact is UNKNOWN, never rollback.
// Once ABORTED, executeSQL's state fence prevents any delayed executor committing.
func (f *Funds) Check(ctx context.Context, event FundEvent) (resolution string) {
	resolution = "UNKNOWN"
	defer func() { f.record("check", resolution) }()
	tx, err := f.Grabber.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return "UNKNOWN"
	}
	defer tx.Rollback()
	var state string
	var body []byte
	var expired bool
	if err = tx.QueryRowContext(ctx, "SELECT state,payload FROM fund_request WHERE request_id=? FOR UPDATE", event.RequestID).Scan(&state, &body); err != nil {
		return "UNKNOWN"
	}
	stored, err := decodeFund(body)
	if err != nil || stored != event {
		return "UNKNOWN"
	}
	if state == "COMMITTED" {
		if valid, e := committedPostings(ctx, tx, event); e == nil && valid {
			return "COMMIT"
		}
		return "UNKNOWN"
	}
	if state == "ABORTED" {
		return "ROLLBACK"
	}
	if err = tx.QueryRowContext(ctx, "SELECT expires_at<=CURRENT_TIMESTAMP(3) FROM fund_request WHERE request_id=?", event.RequestID).Scan(&expired); err != nil {
		return "UNKNOWN"
	}
	if !expired {
		return "UNKNOWN"
	}
	if _, err = tx.ExecContext(ctx, "UPDATE fund_request SET state='ABORTED',error_code='FUND_EXPIRED' WHERE request_id=? AND state='PREPARED'", event.RequestID); err != nil {
		return "UNKNOWN"
	}
	if err = tx.Commit(); err != nil {
		return "UNKNOWN"
	}
	return "ROLLBACK"
}
func committedPostings(ctx context.Context, tx *sql.Tx, e FundEvent) (bool, error) {
	kind, biz, target, escrow := "REFUND", fmt.Sprintf("refund:%d", e.ErrandID), e.PublisherID, "REFUNDED"
	if fundStatus(e.Kind) == "SETTLED" {
		kind, biz, target, escrow = "SETTLE", fmt.Sprintf("settle:%d", e.ErrandID), e.RunnerID, "RELEASED"
	}
	expected := 2
	if e.Commission > 0 {
		expected++
	}
	var n, valid int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN ref_type=? AND ref_id=? AND user_id=? AND ((account_id=1 AND direction='DEBIT' AND amount=?) OR (account_id=2 AND direction='CREDIT' AND amount=? AND ?>0) OR (account_id=(SELECT id FROM wallet_account WHERE owner_type='USER' AND owner_id=?) AND direction='CREDIT' AND amount=?)) THEN 1 ELSE 0 END),0) FROM wallet_ledger WHERE biz_no=?`, kind, e.ErrandID, target, e.Amount, e.Commission, e.Commission, target, e.Amount-e.Commission, biz).Scan(&n, &valid)
	if err != nil || n != expected || valid != expected {
		return false, err
	}
	var facts bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM errand t JOIN escrow_order o ON o.errand_id=t.id WHERE t.id=? AND t.campus_id=? AND t.status=? AND t.publisher_id=? AND t.round=? AND t.reward_amount=? AND o.amount=? AND o.status=?)`, e.ErrandID, e.CampusID, fundStatus(e.Kind), e.PublisherID, e.Round, e.Amount, e.Amount, escrow).Scan(&facts)
	return facts, err
}

// Receipt and recipient notifications commit atomically. Delivery never transfers money.
func (f *Funds) Receive(ctx context.Context, body []byte) (string, error) {
	e, err := decodeFund(body)
	if err != nil {
		return "INVALID", nil
	}
	tx, err := f.Grabber.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var stored []byte
	var state string
	if err = tx.QueryRowContext(ctx, "SELECT payload,state FROM fund_request WHERE request_id=? FOR UPDATE", e.RequestID).Scan(&stored, &state); err != nil {
		return "", err
	}
	fact, err := decodeFund(stored)
	if err != nil {
		return "", err
	}
	if fact != e {
		return "INVALID", nil
	}
	if state != "COMMITTED" {
		return "", fmt.Errorf("visible event has no committed request")
	}
	valid, err := committedPostings(ctx, tx, e)
	if err != nil {
		return "", err
	}
	if !valid {
		return "", fmt.Errorf("committed funds invariant failed")
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO fund_event_receipt(request_id) VALUES(?) ON DUPLICATE KEY UPDATE request_id=request_id", e.RequestID); err != nil {
		return "", err
	}
	for user, content := range FundNotices(e) {
		if _, err = tx.ExecContext(ctx, "INSERT INTO notification(msg_key,user_id,errand_id,type,content) VALUES(?,?,?,?,?) ON DUPLICATE KEY UPDATE id=id", e.RequestID, user, e.ErrandID, e.Kind, content); err != nil {
			return "", err
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE fund_request SET recovery_state='DONE',recovery_token=NULL,recovery_until=NULL WHERE request_id=?", e.RequestID); err != nil {
		return "", err
	}
	return "RECEIVED", tx.Commit()
}

type FundRecovery struct {
	Event       FundEvent `json:"event"`
	Token       string    `json:"-"`
	Attempts    int       `json:"attempts"`
	State       string    `json:"state"`
	ReplayCount int       `json:"replayCount"`
	Reason      string    `json:"reason"`
	LastError   string    `json:"lastError"`
}

func (f *Funds) claimRecovery(ctx context.Context) ([]FundRecovery, error) {
	tx, err := f.Grabber.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT payload,recovery_attempts FROM fund_request WHERE state='COMMITTED' AND ((recovery_state='PENDING' AND next_recovery_at<=CURRENT_TIMESTAMP(3)) OR (recovery_state='RUNNING' AND recovery_until<=CURRENT_TIMESTAMP(3))) ORDER BY next_recovery_at,request_id LIMIT 3 FOR UPDATE SKIP LOCKED`)
	if err != nil {
		return nil, err
	}
	tasks := []FundRecovery{}
	for rows.Next() {
		var body []byte
		var t FundRecovery
		if err = rows.Scan(&body, &t.Attempts); err != nil {
			rows.Close()
			return nil, err
		}
		t.Event, err = decodeFund(body)
		if err != nil {
			rows.Close()
			return nil, err
		}
		tasks = append(tasks, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	claimed := []FundRecovery{}
	for _, t := range tasks {
		if t.Attempts >= 10 {
			_, err = tx.ExecContext(ctx, "UPDATE fund_request SET recovery_state='DEAD',recovery_token=NULL,recovery_until=NULL,last_recovery_error='BUDGET_EXHAUSTED' WHERE request_id=?", t.Event.RequestID)
		} else {
			t.Token = uuid.NewString()
			t.Attempts++
			_, err = tx.ExecContext(ctx, "UPDATE fund_request SET recovery_state='RUNNING',recovery_attempts=recovery_attempts+1,recovery_token=?,recovery_until=TIMESTAMPADD(SECOND,30,CURRENT_TIMESTAMP(3)) WHERE request_id=?", t.Token, t.Event.RequestID)
			claimed = append(claimed, t)
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
func (f *Funds) Recover(ctx context.Context) error {
	// Expiry also runs when MQ is down. The request lock prevents a late SQL commit.
	rows, err := f.Grabber.DB.QueryContext(ctx, "SELECT payload FROM fund_request WHERE state='PREPARED' AND expires_at<=CURRENT_TIMESTAMP(3) ORDER BY expires_at LIMIT 50")
	if err != nil {
		return err
	}
	events := []FundEvent{}
	for rows.Next() {
		var body []byte
		if err = rows.Scan(&body); err != nil {
			rows.Close()
			return err
		}
		e, x := decodeFund(body)
		if x != nil {
			rows.Close()
			return x
		}
		events = append(events, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, e := range events {
		c, cancel := context.WithTimeout(ctx, time.Second)
		f.Check(c, e)
		cancel()
	}
	if f.Send == nil {
		return nil
	}
	tasks, err := f.claimRecovery(ctx)
	if err != nil {
		return err
	}
	for _, t := range tasks {
		c, cancel := context.WithTimeout(ctx, 2*time.Second)
		var resolve func(bool) error
		var e error
		if f.Check(c, t.Event) != "COMMIT" {
			e = fmt.Errorf("funds facts unavailable or inconsistent")
		} else {
			resolve, e = f.Send(c, t.Event)
		}
		if e == nil {
			e = resolve(true)
		}
		cancel()
		// Confirmation only proves send/commit, not consumption. Republish the same
		// event after a bounded wait until a receipt exists, with a finite retry budget.
		_, err = f.Grabber.DB.ExecContext(ctx, `UPDATE fund_request SET recovery_state=IF(recovery_attempts>=10,'DEAD','PENDING'),next_recovery_at=TIMESTAMPADD(SECOND,LEAST(POW(2,recovery_attempts)*60,3600),CURRENT_TIMESTAMP(3)),recovery_token=NULL,recovery_until=NULL,last_recovery_error=? WHERE request_id=? AND recovery_state='RUNNING' AND recovery_token=? AND recovery_until>CURRENT_TIMESTAMP(3)`, func() string {
			if e != nil {
				return "RECOVERY_UNCONFIRMED"
			}
			return "AWAITING_RECEIPT"
		}(), t.Event.RequestID, t.Token)
		if err != nil {
			return err
		}
		if e != nil {
			f.record("recovery", "ERROR")
		} else {
			f.record("recovery", "SENT")
		}
	}
	return nil
}
func (f *Funds) Dead(ctx context.Context) ([]FundRecovery, error) {
	rows, err := f.Grabber.DB.QueryContext(ctx, "SELECT payload,recovery_attempts,recovery_state,replay_count,COALESCE(replay_reason,''),last_recovery_error FROM fund_request WHERE recovery_state='DEAD' ORDER BY request_id LIMIT 100")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FundRecovery{}
	for rows.Next() {
		var t FundRecovery
		var body []byte
		if err = rows.Scan(&body, &t.Attempts, &t.State, &t.ReplayCount, &t.Reason, &t.LastError); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(body, &t.Event); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
func (f *Funds) Replay(ctx context.Context, key, reason string) (bool, error) {
	reason = strings.TrimSpace(reason)
	if !ValidRequestKey(key) || len(reason) == 0 || len(reason) > 200 || strings.ContainsAny(reason, "\r\n") {
		return false, fmt.Errorf("valid request UUID and 1-200 byte single-line reason required")
	}
	r, err := f.Grabber.DB.ExecContext(ctx, "UPDATE fund_request SET recovery_state='PENDING',recovery_attempts=0,replay_count=replay_count+1,replay_reason=?,next_recovery_at=CURRENT_TIMESTAMP(3),recovery_token=NULL,recovery_until=NULL WHERE request_id=? AND state='COMMITTED' AND recovery_state='DEAD'", reason, key)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n == 1, err
}
func (f *Funds) Run(ctx context.Context, onError func(error)) {
	var wg sync.WaitGroup
	for _, job := range []struct {
		interval time.Duration
		fn       func(context.Context) error
	}{
		{time.Second, f.Recover},
		{time.Second, func(c context.Context) error { return f.Grabber.ScanAutoSettle(c, f) }},
		{time.Minute, f.Grabber.CalibrateDueCredit},
	} {
		wg.Add(1)
		go func() { defer wg.Done(); runEvery(ctx, job.interval, job.fn, onError) }()
	}
	wg.Wait()
}

// FundNotices describes actual recipient postings, including commission and
// arbitration outcome; a losing runner must never be told they received money.
func FundNotices(e FundEvent) map[int64]string {
	pub := fmt.Sprintf("任务已退款，%d 分已退回你的账户", e.Amount)
	out := map[int64]string{}
	if fundStatus(e.Kind) == "SETTLED" {
		pub = fmt.Sprintf("任务已结算，跑腿实收 %d 分，平台佣金 %d 分", e.Amount-e.Commission, e.Commission)
		out[e.RunnerID] = fmt.Sprintf("任务已结算，你实收 %d 分，平台佣金 %d 分", e.Amount-e.Commission, e.Commission)
	}
	if e.Kind == "ARBITRATE_PUBLISHER" {
		out[e.RunnerID] = "争议已裁决：款项退回发单人，本次未获得报酬"
	}
	if e.Kind == "ARBITRATE_PUBLISHER" || e.Kind == "ARBITRATE_RUNNER" {
		pub = "争议已裁决：" + pub
	}
	out[e.PublisherID] = pub
	return out
}
