package errand

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	rmq "github.com/apache/rocketmq-clients/golang/v5"
	"github.com/apache/rocketmq-clients/golang/v5/credentials"
	v2 "github.com/apache/rocketmq-clients/golang/v5/protocol/v2"
	"github.com/google/uuid"
	"github.com/yumestk/CampusGo/internal/auth"
	"github.com/yumestk/CampusGo/internal/platform/database"
	"github.com/yumestk/CampusGo/internal/platform/idgen"
	"github.com/yumestk/CampusGo/internal/platform/rediscache"
	"github.com/yumestk/CampusGo/internal/wallet"
)

func TestCommissionBoundaries(t *testing.T) {
	for _, a := range []int64{1, 19, 20, 1001, math.MaxInt64} {
		want := a / 20
		if Commission(a) != want {
			t.Fatalf("commission %d", a)
		}
	}
}
func TestIntegrationD5(t *testing.T) {
	if os.Getenv("CAMPUSGO_D5_INTEGRATION") != "1" {
		t.Skip("run make verify-d5")
	}
	ctx := context.Background()
	db, err := database.Open(ctx, "campusgo_verification")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err = db.QueryRow("SELECT COUNT(*) FROM wallet_ledger WHERE ref_type<>'OPENING'").Scan(&n); err != nil || n != 0 {
		t.Fatal("D5 requires an isolated empty transfer fixture", n, err)
	}
	cache, err := rediscache.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	ids, _ := idgen.New(1016)
	g := &Grabber{DB: db, Redis: cache, IDs: ids, Namespace: "campusgo_verification"}
	p := &Publisher{DB: db, IDs: ids}
	manifest := func(kind string, id int64) {
		t.Helper()
		file, e := os.OpenFile(filepath.Join("..", "..", "work", "d5-fixtures.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if e != nil {
			t.Fatal(e)
		}
		defer file.Close()
		if e = json.NewEncoder(file).Encode(map[string]string{"kind": kind, "id": fmt.Sprint(id)}); e != nil {
			t.Fatal(e)
		}
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, e := db.Exec(q, args...); e != nil {
			t.Fatal(e)
		}
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if e := db.QueryRow(q, args...).Scan(&n); e != nil {
			t.Fatal(e)
		}
		return n
	}
	user := func(role string) auth.User {
		t.Helper()
		id, e := ids.Next(ctx)
		if e != nil {
			t.Fatal(e)
		}
		manifest("user", id)
		exec("INSERT INTO app_user(id,campus_id,display_name,role) VALUES(?,1,'D5验证用户',?)", id, role)
		tx, e := db.Begin()
		if e != nil {
			t.Fatal(e)
		}
		if e = wallet.SeedOpening(ctx, tx, id, 1000000); e != nil {
			tx.Rollback()
			t.Fatal(e)
		}
		if e = tx.Commit(); e != nil {
			t.Fatal(e)
		}
		return auth.User{ID: id, CampusID: 1, Role: role}
	}
	publisher, runner, arb, other := user("STUDENT"), user("STUDENT"), user("ARBITRATOR"), user("STUDENT")
	sender := func(context.Context, FundEvent) (func(bool) error, error) {
		return func(bool) error { return nil }, nil
	}
	f := &Funds{Grabber: g, Send: sender}
	newTask := func(amount int64) int64 {
		t.Helper()
		r, e := p.Publish(ctx, publisher, uuid.NewString(), PublishCommand{CampusID: 1, Type: "DELIVERY", Title: "D5验证任务", RewardCents: amount, SlotTotal: 1})
		if e != nil {
			t.Fatal(e)
		}
		manifest("errand", *r.ErrandID)
		return *r.ErrandID
	}
	accepted := func(amount int64) int64 {
		t.Helper()
		id := newTask(amount)
		if _, e := g.Grab(ctx, runner, uuid.NewString(), GrabCommand{id, 0}); e != nil {
			t.Fatal(e)
		}
		if _, e := g.Confirm(ctx, runner, id, 0); e != nil {
			t.Fatal(e)
		}
		return id
	}
	delivered := func(amount int64) int64 {
		t.Helper()
		id := accepted(amount)
		for _, a := range []string{"PICK_UP", "DELIVER"} {
			if _, e := g.Fulfill(ctx, runner, id, 0, a); e != nil {
				t.Fatal(e)
			}
		}
		return id
	}
	command := func(id int64, a string) FundCommand { return FundCommand{ErrandID: id, Round: 0, Action: a} }
	reconcile := func() {
		t.Helper()
		checks, e := (wallet.Query{DB: db}).Reconcile(ctx)
		if e != nil {
			t.Fatal(e)
		}
		for _, c := range checks {
			if c.Violations != 0 {
				t.Fatalf("reconcile %s=%d", c.Name, c.Violations)
			}
		}
	}
	event := func(key string) FundEvent {
		t.Helper()
		var body []byte
		if e := db.QueryRow("SELECT payload FROM fund_request WHERE request_id=?", key).Scan(&body); e != nil {
			t.Fatal(e)
		}
		var ev FundEvent
		if e := json.Unmarshal(body, &ev); e != nil {
			t.Fatal(e)
		}
		return ev
	}
	state := func(id int64) string {
		t.Helper()
		var s string
		if e := db.QueryRow("SELECT status FROM errand WHERE id=?", id).Scan(&s); e != nil {
			t.Fatal(e)
		}
		return s
	}
	t.Run("fulfillment_permissions_idempotency_and_delivery_time", func(t *testing.T) {
		id := accepted(1001)
		_, e := g.Fulfill(ctx, other, id, 0, "PICK_UP")
		requireCode(t, e, "FORBIDDEN")
		_, e = g.Fulfill(ctx, runner, id, 1, "PICK_UP")
		requireCode(t, e, "STALE_VERSION")
		_, e = g.Fulfill(ctx, runner, id, 0, "DELIVER")
		requireCode(t, e, "INVALID_STATUS")
		for _, a := range []string{"PICK_UP", "PICK_UP", "DELIVER", "DELIVER"} {
			if _, e = g.Fulfill(ctx, runner, id, 0, a); e != nil {
				t.Fatal(e)
			}
		}
		if count("SELECT COUNT(*) FROM errand_status_log WHERE errand_id=? AND to_status IN ('PICKED_UP','DELIVERED')", id) != 2 || count("SELECT COUNT(*) FROM local_message WHERE errand_id=? AND topic='errand-auto-settle'", id) != 1 {
			t.Fatal("duplicated fulfillment effects")
		}
		key := uuid.NewString()
		r, e := f.Execute(ctx, publisher, key, command(id, "SETTLE"))
		if e != nil || r.State != "COMMITTED" {
			t.Fatal(r, e)
		}
		r, e = f.Execute(ctx, publisher, key, command(id, "SETTLE"))
		if e != nil || r.Status != "SETTLED" {
			t.Fatal(r, e)
		}
		if count("SELECT COUNT(*) FROM wallet_ledger WHERE ref_id=? AND ref_type='SETTLE' AND account_id=2 AND amount=50", id) != 1 {
			t.Fatal("commission/recipient mismatch")
		}
		if f.Check(ctx, event(key)) != "COMMIT" {
			t.Fatal("durable check")
		}
		reconcile()
	})
	t.Run("refund_and_both_arbitration_directions", func(t *testing.T) {
		for _, action := range []string{"CANCEL", "ARBITRATE_RUNNER", "ARBITRATE_PUBLISHER"} {
			id := int64(0)
			actor := publisher
			if action == "CANCEL" {
				id = newTask(1001)
			} else {
				id = accepted(1001)
				if _, e := g.Fulfill(ctx, publisher, id, 0, "DISPUTE"); e != nil {
					t.Fatal(e)
				}
				actor = arb
			}
			_, e := f.Execute(ctx, other, uuid.NewString(), command(id, action))
			requireCode(t, e, "FORBIDDEN")
			key := uuid.NewString()
			r, e := f.Execute(ctx, actor, key, command(id, action))
			if e != nil || r.Status != fundStatus(action) {
				t.Fatal(r, e)
			}
			if action == "ARBITRATE_PUBLISHER" && count("SELECT COUNT(*) FROM credit_event WHERE errand_id=? AND kind='DISPUTE_LOSE' AND delta=-8", id) != 1 {
				t.Fatal("lost dispute credit")
			}
			if action != "ARBITRATE_RUNNER" && count("SELECT COUNT(*) FROM wallet_ledger WHERE ref_id=? AND ref_type='REFUND' AND user_id=? AND amount=1001", id, publisher.ID) != 2 {
				t.Fatal("refund recipient")
			}
			reconcile()
		}
	})
	t.Run("tiny_commission_zero_and_integer_rounding", func(t *testing.T) {
		for _, amount := range []int64{1, 19, 20, 99} {
			id := delivered(amount)
			if _, e := f.Execute(ctx, publisher, uuid.NewString(), command(id, "SETTLE")); e != nil {
				t.Fatal(e)
			}
			expected := 2
			if amount >= 20 {
				expected = 3
			}
			if count("SELECT COUNT(*) FROM wallet_ledger WHERE ref_id=? AND ref_type='SETTLE'", id) != expected {
				t.Fatal("zero commission posting")
			}
		}
		reconcile()
	})
	t.Run("same_key_concurrency_and_fingerprint", func(t *testing.T) {
		id := delivered(2001)
		key := uuid.NewString()
		var wg sync.WaitGroup
		errs := make(chan error, 12)
		for range 12 {
			wg.Add(1)
			go func() { defer wg.Done(); _, e := f.Execute(ctx, publisher, key, command(id, "SETTLE")); errs <- e }()
		}
		wg.Wait()
		close(errs)
		for e := range errs {
			if e != nil {
				t.Fatal(e)
			}
		}
		_, e := f.Execute(ctx, publisher, key, command(id, "CANCEL"))
		requireCode(t, e, "IDEMPOTENCY_CONFLICT")
		r, e := f.Result(ctx, other, key)
		if e != nil || r.State != "NOT_OBSERVED" {
			t.Fatal("result leaked")
		}
		if count("SELECT COUNT(*) FROM credit_event WHERE errand_id=? AND kind='SETTLE'", id) != 1 {
			t.Fatal("duplicate credit")
		}
		reconcile()
	})
	t.Run("distinct_key_settlements_and_shared_account_snapshots", func(t *testing.T) {
		taskIDs := []int64{}
		for range 10 {
			taskIDs = append(taskIDs, delivered(321))
		}
		var wg sync.WaitGroup
		errs := make(chan error, 20)
		for _, id := range taskIDs {
			for range 2 {
				wg.Add(1)
				go func(id int64) {
					defer wg.Done()
					_, e := f.Execute(ctx, publisher, uuid.NewString(), command(id, "SETTLE"))
					errs <- e
				}(id)
			}
		}
		wg.Wait()
		close(errs)
		for e := range errs {
			if e != nil && e.Error() != "INVALID_STATUS" {
				t.Fatal(e)
			}
		}
		for _, id := range taskIDs {
			if state(id) != "SETTLED" {
				t.Fatal("missing settlement")
			}
		}
		reconcile()
	})
	t.Run("dispute_races_manual_settlement", func(t *testing.T) {
		id := delivered(1001)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); g.Fulfill(ctx, runner, id, 0, "DISPUTE") }()
		go func() { defer wg.Done(); f.Execute(ctx, publisher, uuid.NewString(), command(id, "SETTLE")) }()
		wg.Wait()
		s := state(id)
		if s != "SETTLED" && s != "DISPUTED" {
			t.Fatal(s)
		}
		if s == "DISPUTED" {
			if _, e := f.Execute(ctx, arb, uuid.NewString(), command(id, "ARBITRATE_PUBLISHER")); e != nil {
				t.Fatal(e)
			}
		}
		reconcile()
	})
	t.Run("opposite_arbitration_race", func(t *testing.T) {
		id := accepted(1001)
		if _, e := g.Fulfill(ctx, publisher, id, 0, "DISPUTE"); e != nil {
			t.Fatal(e)
		}
		var wg sync.WaitGroup
		for _, a := range []string{"ARBITRATE_RUNNER", "ARBITRATE_PUBLISHER"} {
			wg.Add(1)
			go func(a string) { defer wg.Done(); f.Execute(ctx, arb, uuid.NewString(), command(id, a)) }(a)
		}
		wg.Wait()
		if state(id) != "SETTLED" && state(id) != "REFUNDED" {
			t.Fatal("missing arbitration")
		}
		reconcile()
	})
	t.Run("no_half_ack_no_SQL_and_retry_same_key", func(t *testing.T) {
		id := delivered(1001)
		key := uuid.NewString()
		broken := &Funds{Grabber: g, Send: func(context.Context, FundEvent) (func(bool) error, error) { return nil, io.EOF }}
		_, e := broken.Execute(ctx, publisher, key, command(id, "SETTLE"))
		requireCode(t, e, "FUND_RESULT_UNKNOWN")
		if state(id) != "DELIVERED" || count("SELECT COUNT(*) FROM wallet_ledger WHERE ref_id=? AND ref_type='SETTLE'", id) != 0 {
			t.Fatal("SQL ran without half ack")
		}
		if f.Check(ctx, event(key)) != "UNKNOWN" {
			t.Fatal("prepared not UNKNOWN")
		}
		if _, e = f.Execute(ctx, publisher, key, command(id, "SETTLE")); e != nil {
			t.Fatal(e)
		}
		reconcile()
	})
	t.Run("expiry_fences_late_executor", func(t *testing.T) {
		id := delivered(1001)
		key := uuid.NewString()
		broken := &Funds{Grabber: g, Send: func(context.Context, FundEvent) (func(bool) error, error) { return nil, io.EOF }}
		broken.Execute(ctx, publisher, key, command(id, "SETTLE"))
		exec("UPDATE fund_request SET expires_at=TIMESTAMPADD(SECOND,-1,CURRENT_TIMESTAMP(3)) WHERE request_id=?", key)
		if f.Check(ctx, event(key)) != "ROLLBACK" {
			t.Fatal("expired request not fenced")
		}
		_, e := f.Execute(ctx, publisher, key, command(id, "SETTLE"))
		requireCode(t, e, "FUND_EXPIRED")
		if state(id) != "DELIVERED" {
			t.Fatal("late execution")
		}
		if _, e = f.Execute(ctx, publisher, uuid.NewString(), command(id, "SETTLE")); e != nil {
			t.Fatal(e)
		}
		reconcile()
	})
	t.Run("credit_constraint_failure_rolls_back_funds_and_task", func(t *testing.T) {
		id := accepted(1001)
		if _, e := g.Fulfill(ctx, publisher, id, 0, "DISPUTE"); e != nil {
			t.Fatal(e)
		}
		exec(fmt.Sprintf("ALTER TABLE credit_event ADD CONSTRAINT ck_d5_injected CHECK (NOT (errand_id=%d AND kind='DISPUTE_LOSE'))", id))
		key := uuid.NewString()
		_, e := f.Execute(ctx, arb, key, command(id, "ARBITRATE_PUBLISHER"))
		exec("ALTER TABLE credit_event DROP CHECK ck_d5_injected")
		if e == nil || state(id) != "DISPUTED" || count("SELECT COUNT(*) FROM wallet_ledger WHERE ref_id=? AND ref_type='REFUND'", id) != 0 {
			t.Fatal("non-atomic credit failure", e)
		}
		if _, e = f.Execute(ctx, arb, key, command(id, "ARBITRATE_PUBLISHER")); e != nil {
			t.Fatal(e)
		}
		reconcile()
	})
	t.Run("commit_unknown_both_committed_and_not_sent", func(t *testing.T) {
		for _, committed := range []bool{true, false} {
			id := delivered(1001)
			key := uuid.NewString()
			_, e := f.execute(ctx, publisher, key, command(id, "SETTLE"), nil, func(tx *sql.Tx) error {
				if committed {
					if x := tx.Commit(); x != nil {
						return x
					}
				}
				return io.EOF
			})
			requireCode(t, e, "FUND_RESULT_UNKNOWN")
			want := "UNKNOWN"
			if committed {
				want = "COMMIT"
			}
			if f.Check(ctx, event(key)) != want {
				t.Fatal("wrong commit resolution")
			}
			if _, e = f.Execute(ctx, publisher, key, command(id, "SETTLE")); e != nil {
				t.Fatal(e)
			}
		}
		reconcile()
	})
	t.Run("inflight_checker_and_primary_unreadable_are_unknown", func(t *testing.T) {
		id := delivered(1001)
		key := uuid.NewString()
		entered, release := make(chan struct{}), make(chan struct{})
		done := make(chan error, 1)
		go func() {
			_, e := f.execute(ctx, publisher, key, command(id, "SETTLE"), func(*sql.Tx) error { close(entered); <-release; return nil }, nil)
			done <- e
		}()
		<-entered
		c, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
		e := event(key)
		resolution := f.Check(c, e)
		cancel()
		close(release)
		if x := <-done; x != nil {
			t.Fatal(x)
		}
		if resolution != "UNKNOWN" {
			t.Fatal(resolution)
		}
		closed, x := database.Open(ctx, "campusgo_verification")
		if x != nil {
			t.Fatal(x)
		}
		closed.Close()
		unavailable := &Funds{Grabber: &Grabber{DB: closed}}
		if unavailable.Check(ctx, e) != "UNKNOWN" {
			t.Fatal("DB failure rolled back")
		}
		reconcile()
	})
	t.Run("auto_settlement_deadline_early_duplicate_and_scan", func(t *testing.T) {
		id := delivered(1001)
		_, e := f.Execute(ctx, auth.User{ID: -1, CampusID: 1}, uuid.NewString(), command(id, "AUTO_SETTLE"))
		requireCode(t, e, "NOT_DUE")
		var body []byte
		if e = db.QueryRow("SELECT payload FROM local_message WHERE errand_id=? AND topic='errand-auto-settle'", id).Scan(&body); e != nil {
			t.Fatal(e)
		}
		if result, e := f.ConsumeAutoSettle(ctx, body); e != nil || result != "SKIPPED" {
			t.Fatal(result, e)
		}
		exec("UPDATE errand SET delivered_at=TIMESTAMPADD(SECOND,-86401,CURRENT_TIMESTAMP(3)) WHERE id=?", id)
		if e = g.ScanAutoSettle(ctx, f); e != nil {
			t.Fatal(e)
		}
		if state(id) != "SETTLED" {
			t.Fatal("scan didn't settle")
		}
		if e = g.ScanAutoSettle(ctx, f); e != nil {
			t.Fatal(e)
		}
		reconcile()
	})
	t.Run("recovery_receipt_duplicate_and_reasoned_dead_replay", func(t *testing.T) {
		id := delivered(1001)
		key := uuid.NewString()
		if _, e := f.Execute(ctx, publisher, key, command(id, "SETTLE")); e != nil {
			t.Fatal(e)
		}
		exec("UPDATE fund_request SET next_recovery_at=CURRENT_TIMESTAMP(3) WHERE request_id=?", key)
		tasks, e := f.claimRecovery(ctx)
		if e != nil || len(tasks) != 1 {
			t.Fatal(len(tasks), e)
		}
		exec("UPDATE fund_request SET recovery_attempts=10,recovery_until=TIMESTAMPADD(SECOND,-1,CURRENT_TIMESTAMP(3)) WHERE request_id=?", key)
		if _, e = f.claimRecovery(ctx); e != nil {
			t.Fatal(e)
		}
		if _, e = f.Replay(ctx, key, ""); e == nil {
			t.Fatal("missing replay reason")
		}
		ok, e := f.Replay(ctx, key, "D5故障恢复验收")
		if e != nil || !ok {
			t.Fatal(ok, e)
		}
		body, _ := json.Marshal(event(key))
		for range 2 {
			if result, e := f.Receive(ctx, body); e != nil || result != "RECEIVED" {
				t.Fatal(result, e)
			}
		}
		if count("SELECT COUNT(*) FROM fund_event_receipt WHERE request_id=?", key) != 1 {
			t.Fatal("duplicate receipt")
		}
		if f.Check(ctx, event(key)) != "COMMIT" {
			t.Fatal("receipt changed money")
		}
		reconcile()
	})
	t.Run("credit_30day_calibration_and_clamp", func(t *testing.T) {
		u := user("STUDENT")
		id := newTask(1)
		exec("INSERT INTO credit_event(user_id,errand_id,round,kind,delta,created_at) VALUES(?,?,0,'SETTLE',2,TIMESTAMPADD(DAY,-31,CURRENT_TIMESTAMP(3))),(?,?,1,'DISPUTE_LOSE',-8,CURRENT_TIMESTAMP(3))", u.ID, id, u.ID, id)
		if e := g.calibrateUser(ctx, u.ID); e != nil {
			t.Fatal(e)
		}
		if count("SELECT COUNT(*) FROM credit_score WHERE user_id=? AND score=52", u.ID) != 1 {
			t.Fatal("credit window")
		}
		exec("DELETE FROM credit_event WHERE errand_id=?", id)
		if _, e := f.Execute(ctx, publisher, uuid.NewString(), command(id, "CANCEL")); e != nil {
			t.Fatal(e)
		}
		reconcile()
	})

	t.Run("reconcile_locates_corrupt_terminal_posting", func(t *testing.T) {
		id := delivered(1001)
		if _, e := f.Execute(ctx, publisher, uuid.NewString(), command(id, "SETTLE")); e != nil {
			t.Fatal(e)
		}
		exec("UPDATE wallet_ledger SET user_id=? WHERE ref_id=? AND ref_type='SETTLE' AND account_id=2", other.ID, id)
		checks, e := (wallet.Query{DB: db}).Reconcile(ctx)
		if e != nil {
			t.Fatal(e)
		}
		found := false
		for _, c := range checks {
			if c.Name == "terminal_transfer_amount_and_recipient" && c.Violations == 1 && len(c.Examples) == 1 && c.Examples[0] == fmt.Sprint(id) {
				found = true
			}
		}
		exec("UPDATE wallet_ledger SET user_id=? WHERE ref_id=? AND ref_type='SETTLE' AND account_id=2", runner.ID, id)
		if !found {
			t.Fatal("corruption not located")
		}
		facts, e := (wallet.Query{DB: db}).Inspect(ctx, id)
		if e != nil || len(facts["ledger"]) != 5 {
			t.Fatal("inspection missing ledger", e)
		}
		reconcile()
	})

	t.Run("commission_capacity_rolls_back_all_legs_and_persists_rejection", func(t *testing.T) {
		id := delivered(1001)
		var before int64
		if e := db.QueryRow("SELECT available FROM wallet_account WHERE id=2").Scan(&before); e != nil {
			t.Fatal(e)
		}
		exec("UPDATE wallet_account SET available=? WHERE id=2", int64(math.MaxInt64))
		key := uuid.NewString()
		_, e := f.Execute(ctx, publisher, key, command(id, "SETTLE"))
		exec("UPDATE wallet_account SET available=? WHERE id=2", before)
		requireCode(t, e, "BALANCE_LIMIT")
		if state(id) != "DELIVERED" || count("SELECT COUNT(*) FROM wallet_ledger WHERE ref_id=? AND ref_type='SETTLE'", id) != 0 {
			t.Fatal("partial overflow posting")
		}
		r, e := f.Result(ctx, publisher, key)
		if e != nil || r.State != "ABORTED" || r.ErrorCode != "BALANCE_LIMIT" {
			t.Fatal(r, e)
		}
		if _, e = f.Execute(ctx, publisher, uuid.NewString(), command(id, "SETTLE")); e != nil {
			t.Fatal(e)
		}
		reconcile()
	})
	t.Run("concurrent_due_credit_calibration_runs_once", func(t *testing.T) {
		u := user("STUDENT")
		exec("INSERT INTO credit_score(user_id,score,version,last_calibrated_at) VALUES(?,100,1,TIMESTAMPADD(DAY,-2,CURRENT_TIMESTAMP(3)))", u.ID)
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		for range 2 {
			wg.Add(1)
			go func() { defer wg.Done(); errs <- g.CalibrateDueCredit(ctx) }()
		}
		wg.Wait()
		close(errs)
		for e := range errs {
			if e != nil {
				t.Fatal(e)
			}
		}
		if count("SELECT COUNT(*) FROM credit_score WHERE user_id=? AND score=60 AND version=2", u.ID) != 1 {
			t.Fatal("calibration not fenced")
		}
	})
	t.Run("real_mysql_commit_OK_packet_lost", func(t *testing.T) {
		id := delivered(1001)
		key := uuid.NewString()
		addr, dropped := commitAckProxyAt(t, 2)
		proxied := &Funds{Grabber: &Grabber{DB: databaseViaProxy(t, addr), IDs: ids}, Send: sender}
		_, e := proxied.Execute(ctx, publisher, key, command(id, "SETTLE"))
		requireCode(t, e, "FUND_RESULT_UNKNOWN")
		select {
		case <-dropped:
		case <-time.After(time.Second):
			t.Fatal("COMMIT OK not intercepted")
		}
		r, e := f.Result(ctx, publisher, key)
		if e != nil || r.State != "COMMITTED" {
			t.Fatal(r, e)
		}
		if _, e = f.Execute(ctx, publisher, key, command(id, "SETTLE")); e != nil {
			t.Fatal(e)
		}
		reconcile()
	})
	t.Run("SIGKILL_before_after_SQL_commit_and_recovery_claim", func(t *testing.T) {
		for _, stage := range []string{"before_commit", "after_commit", "recovery_claimed"} {
			id := delivered(1001)
			key := uuid.NewString()
			if stage == "recovery_claimed" {
				if _, e := f.Execute(ctx, publisher, key, command(id, "SETTLE")); e != nil {
					t.Fatal(e)
				}
				exec("UPDATE fund_request SET next_recovery_at=CURRENT_TIMESTAMP(3) WHERE request_id=?", key)
			}
			killD5Child(t, stage, id, publisher.ID, key)
			if stage == "before_commit" {
				if state(id) != "DELIVERED" {
					t.Fatal("partial crash commit")
				}
				if _, e := f.Execute(ctx, publisher, key, command(id, "SETTLE")); e != nil {
					t.Fatal(e)
				}
			} else if state(id) != "SETTLED" {
				t.Fatal("lost committed task")
			}
			if stage == "recovery_claimed" {
				exec("UPDATE fund_request SET recovery_until=TIMESTAMPADD(SECOND,-1,CURRENT_TIMESTAMP(3)) WHERE request_id=?", key)
				tasks, e := f.claimRecovery(ctx)
				if e != nil || len(tasks) != 1 || tasks[0].Attempts != 2 {
					t.Fatal(tasks, e)
				}
				exec("UPDATE fund_request SET recovery_state='PENDING',next_recovery_at=TIMESTAMPADD(HOUR,1,CURRENT_TIMESTAMP(3)),recovery_token=NULL,recovery_until=NULL WHERE request_id=?", key)
			}
			reconcile()
		}
	})
	if os.Getenv("CAMPUSGO_D5_MQ") == "1" {
		t.Run("real_native_transaction_recheck_republish_and_receipt", func(t *testing.T) { testD5MQ(t, ctx, db, f, publisher, delivered, event, reconcile) })
	}
	reconcile()
}
func testD5MQ(t *testing.T, ctx context.Context, db *sql.DB, f *Funds, publisher auth.User, delivered func(int64) int64, event func(string) FundEvent, reconcile func()) {
	// Real native transaction protocol. First omit EndTransaction deliberately;
	// the broker must ask the durable SQL checker and expose the committed event.
	var checks atomic.Int64
	producer, err := rmq.NewProducer(&rmq.Config{Endpoint: os.Getenv("CAMPUSGO_MQ_ENDPOINT"), Credentials: &credentials.SessionCredentials{}}, rmq.WithTopics("campusgo-funds"), rmq.WithTransactionChecker(&rmq.TransactionChecker{Check: func(m *rmq.MessageView) rmq.TransactionResolution {
		var e FundEvent
		if json.Unmarshal(m.GetBody(), &e) != nil {
			return rmq.UNKNOWN
		}
		c, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		checks.Add(1)
		switch f.Check(c, e) {
		case "COMMIT":
			return rmq.COMMIT
		case "ROLLBACK":
			return rmq.ROLLBACK
		}
		return rmq.UNKNOWN
	}}))
	if err != nil {
		t.Fatal(err)
	}
	if err = producer.Start(); err != nil {
		t.Fatal(err)
	}
	defer producer.GracefulStop()
	dropOriginal := true
	original := f.Send
	defer func() { f.Send = original }()
	f.Send = func(c context.Context, e FundEvent) (func(bool) error, error) {
		body, _ := json.Marshal(e)
		m := &rmq.Message{Topic: "campusgo-funds", Body: body}
		m.SetKeys("campusgo_verification:" + e.RequestID)
		m.SetTag("campusgo_verification")
		tx := producer.BeginTransaction()
		if _, err := producer.SendWithTransaction(c, m, tx); err != nil {
			return nil, err
		}
		return func(commit bool) error {
			if dropOriginal {
				return io.EOF
			}
			if commit {
				return tx.Commit()
			}
			return tx.RollBack()
		}, nil
	}
	id := delivered(1001)
	key := uuid.NewString()
	if _, err = f.Execute(ctx, publisher, key, FundCommand{ErrandID: id, Round: 0, Action: "SETTLE"}); err != nil {
		t.Fatal(err)
	}
	consumer, err := rmq.NewSimpleConsumer(&rmq.Config{Endpoint: os.Getenv("CAMPUSGO_MQ_ENDPOINT"), ConsumerGroup: "campusgo-funds-campusgo_verification", Credentials: &credentials.SessionCredentials{}}, rmq.WithSimpleAwaitDuration(6*time.Second), rmq.WithSimpleSubscriptionExpressions(map[string]*rmq.FilterExpression{"campusgo-funds": rmq.NewFilterExpression("campusgo_verification")}))
	if err != nil {
		t.Fatal(err)
	}
	if err = consumer.Start(); err != nil {
		t.Fatal(err)
	}
	defer consumer.GracefulStop()
	deadline := time.Now().Add(90 * time.Second)
	received := false
	for time.Now().Before(deadline) && !received {
		c, cancel := context.WithTimeout(ctx, 12*time.Second)
		messages, e := consumer.Receive(c, 8, 20*time.Second)
		cancel()
		if e != nil {
			var rpc *rmq.ErrRpcStatus
			if errors.As(e, &rpc) && rpc.Code == int32(v2.Code_MESSAGE_NOT_FOUND) {
				continue
			}
			t.Fatal(e)
		}
		for _, m := range messages {
			ev, e := decodeFund(m.GetBody())
			if e != nil {
				t.Fatal(e)
			}
			if ev.RequestID == key {
				if result, e := f.Receive(ctx, m.GetBody()); e != nil || result != "RECEIVED" {
					t.Fatal(result, e)
				}
				received = true
			}
			if e = consumer.Ack(ctx, m); e != nil {
				t.Fatal(e)
			}
		}
	}
	if !received {
		t.Fatal("native transaction checker did not recover delivery")
	}
	if f.Check(ctx, event(key)) != "COMMIT" || checks.Load() == 0 {
		t.Fatal("lost durable fact or no broker callback")
	}
	// Reconstruct a committed event after the original delivery opportunity is
	// unavailable. Republish via native transaction messages from immutable facts.
	dropOriginal = false
	id2 := delivered(1001)
	key2 := uuid.NewString()
	f.Send = original
	if _, e := f.Execute(ctx, publisher, key2, FundCommand{ErrandID: id2, Round: 0, Action: "SETTLE"}); e != nil {
		t.Fatal(e)
	}
	f.Send = func(c context.Context, e FundEvent) (func(bool) error, error) {
		body, _ := json.Marshal(e)
		m := &rmq.Message{Topic: "campusgo-funds", Body: body}
		m.SetKeys("campusgo_verification:" + e.RequestID)
		m.SetTag("campusgo_verification")
		tx := producer.BeginTransaction()
		if _, x := producer.SendWithTransaction(c, m, tx); x != nil {
			return nil, x
		}
		return func(commit bool) error {
			if commit {
				return tx.Commit()
			}
			return tx.RollBack()
		}, nil
	}
	if _, e := db.Exec("UPDATE fund_request SET next_recovery_at=CURRENT_TIMESTAMP(3) WHERE request_id=?", key2); e != nil {
		t.Fatal(e)
	}
	if e := f.Recover(ctx); e != nil {
		t.Fatal(e)
	}
	received = false
	deadline = time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) && !received {
		c, cancel := context.WithTimeout(ctx, 12*time.Second)
		messages, e := consumer.Receive(c, 8, 20*time.Second)
		cancel()
		if e != nil {
			var rpc *rmq.ErrRpcStatus
			if errors.As(e, &rpc) && rpc.Code == int32(v2.Code_MESSAGE_NOT_FOUND) {
				continue
			}
			t.Fatal(e)
		}
		for _, m := range messages {
			ev, e := decodeFund(m.GetBody())
			if e != nil {
				t.Fatal(e)
			}
			if ev.RequestID == key2 {
				if _, e = f.Receive(ctx, m.GetBody()); e != nil {
					t.Fatal(e)
				}
				received = true
			}
			if e = consumer.Ack(ctx, m); e != nil {
				t.Fatal(e)
			}
		}
	}
	if !received {
		t.Fatal("durable native republication did not deliver")
	}
	reconcile()
}

func TestD5CrashHelper(t *testing.T) {
	stage := os.Getenv("CAMPUSGO_D5_CRASH")
	if stage == "" {
		t.Skip("subprocess only")
	}
	ctx := context.Background()
	db, err := database.Open(ctx, "campusgo_verification")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ids, _ := idgen.New(1014)
	id, _ := strconv.ParseInt(os.Getenv("CAMPUSGO_D5_TASK"), 10, 64)
	actor, _ := strconv.ParseInt(os.Getenv("CAMPUSGO_D5_ACTOR"), 10, 64)
	f := &Funds{Grabber: &Grabber{DB: db, IDs: ids}, Send: func(context.Context, FundEvent) (func(bool) error, error) {
		return func(bool) error { return nil }, nil
	}}
	ready := func() { fmt.Println("D5_READY"); select {} }
	before := func(*sql.Tx) error {
		if stage == "before_commit" {
			ready()
		}
		return nil
	}
	commit := func(tx *sql.Tx) error {
		if e := tx.Commit(); e != nil {
			return e
		}
		if stage == "after_commit" {
			ready()
		}
		return nil
	}
	if stage == "recovery_claimed" {
		tasks, e := f.claimRecovery(ctx)
		if e != nil || len(tasks) != 1 {
			t.Fatal(tasks, e)
		}
		ready()
	}
	_, err = f.execute(ctx, auth.User{ID: actor, CampusID: 1, Role: "STUDENT"}, os.Getenv("CAMPUSGO_D5_KEY"), FundCommand{ErrandID: id, Round: 0, Action: "SETTLE"}, before, commit)
	t.Fatal("unreached checkpoint", err)
}
func killD5Child(t *testing.T, stage string, id, actor int64, key string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestD5CrashHelper$")
	cmd.Env = append(os.Environ(), "CAMPUSGO_D5_CRASH="+stage, "CAMPUSGO_D5_TASK="+fmt.Sprint(id), "CAMPUSGO_D5_ACTOR="+fmt.Sprint(actor), "CAMPUSGO_D5_KEY="+key)
	stdout, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	cmd.Stderr = os.Stderr
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	ready := make(chan bool, 1)
	go func() {
		s := bufio.NewScanner(stdout)
		for s.Scan() {
			if strings.Contains(s.Text(), "D5_READY") {
				ready <- true
				return
			}
		}
		ready <- false
	}()
	select {
	case ok := <-ready:
		if !ok {
			cmd.Wait()
			t.Fatal("child exited before checkpoint")
		}
	case <-time.After(15 * time.Second):
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatal("child checkpoint timed out")
	}
	if e = cmd.Process.Kill(); e != nil {
		t.Fatal(e)
	}
	if e = cmd.Wait(); e == nil {
		t.Fatal("child was not killed")
	}
}
