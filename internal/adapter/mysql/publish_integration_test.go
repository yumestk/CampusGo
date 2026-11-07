package mysql

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/yumestk/CampusGo/internal/auth"
	"github.com/yumestk/CampusGo/internal/platform/database"
	"github.com/yumestk/CampusGo/internal/platform/fault"
	"github.com/yumestk/CampusGo/internal/platform/idgen"
	"github.com/yumestk/CampusGo/internal/wallet"
)

func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	var f *fault.Error
	if !errors.As(err, &f) || f.Code != code {
		t.Fatalf("error=%v, want %s", err, code)
	}
}

func TestIntegrationPublishFunds(t *testing.T) {
	if os.Getenv("CAMPUSGO_D2_INTEGRATION") != "1" {
		t.Skip("run make verify-d2")
	}
	ctx := context.Background()
	db, err := database.Open(ctx, "campusgo_verification")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	var previous int
	if err = db.QueryRow("SELECT (SELECT COUNT(*) FROM escrow_order)+(SELECT COUNT(*) FROM publish_request)+(SELECT COUNT(*) FROM wallet_ledger WHERE ref_type='ESCROW')").Scan(&previous); err != nil || previous != 0 {
		t.Fatalf("D2 requires isolated verification DB without previous fund operations: count=%d err=%v", previous, err)
	}
	ids, _ := idgen.New(1021)
	p := &PublishStore{DB: db, IDs: ids}
	q := wallet.Query{DB: db}
	users := []int64{}
	t.Cleanup(func() {
		tx, e := db.Begin()
		if e != nil {
			t.Error(e)
			return
		}
		defer tx.Rollback()
		for _, id := range users {
			for _, query := range []string{"DELETE FROM publish_request WHERE publisher_id=?", "DELETE FROM escrow_order WHERE publisher_id=?", "DELETE FROM errand_status_log WHERE operator_id=?", "DELETE FROM errand WHERE publisher_id=?", "DELETE FROM wallet_ledger WHERE user_id=?", "DELETE FROM wallet_account WHERE owner_id=? AND owner_type='USER'", "DELETE FROM app_user WHERE id=?"} {
				if _, e = tx.Exec(query, id); e != nil {
					t.Error(e)
					return
				}
			}
		}
		var n int
		if e = tx.QueryRow("SELECT COUNT(*) FROM wallet_ledger WHERE account_id=1").Scan(&n); e != nil || n != 0 {
			t.Errorf("unexpected concurrent verification writes: count=%d err=%v", n, e)
			return
		}
		if _, e = tx.Exec("UPDATE wallet_account SET available=0,version=0 WHERE id=1"); e != nil {
			t.Error(e)
			return
		}
		if e = tx.Commit(); e != nil {
			t.Error(e)
		}
	})
	newUser := func(amount int64) auth.User {
		t.Helper()
		id, e := ids.Next(ctx)
		if e != nil {
			t.Fatal(e)
		}
		tx, e := db.Begin()
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback()
		if _, e = tx.Exec("INSERT INTO app_user (id,campus_id,display_name,role) VALUES (?,1,'D2资金验证','STUDENT')", id); e != nil {
			t.Fatal(e)
		}
		if e = wallet.SeedOpening(ctx, tx, id, amount); e != nil {
			t.Fatal(e)
		}
		if e = tx.Commit(); e != nil {
			t.Fatal(e)
		}
		users = append(users, id)
		return auth.User{ID: id, CampusID: 1, Role: "STUDENT"}
	}
	command := func(amount int64) PublishCommand {
		return PublishCommand{CampusID: 1, Type: "DELIVERY", Title: "D2资金验证:帮取快递", RewardCents: amount, SlotTotal: 1}
	}
	check := func() {
		t.Helper()
		checks, e := q.Reconcile(ctx)
		if e != nil {
			t.Fatal(e)
		}
		for _, c := range checks {
			if c.Violations != 0 {
				t.Errorf("reconcile %s: %d", c.Name, c.Violations)
			}
		}
	}
	balance := func(u auth.User) wallet.Balance {
		t.Helper()
		b, e := q.Balance(ctx, u)
		if e != nil {
			t.Fatal(e)
		}
		return b
	}

	t.Run("pre_refactor_persisted_publish_replay", func(t *testing.T) {
		u := newUser(10000)
		key := uuid.NewString()
		raw := legacyTaskCommand(t, "publish")
		var c PublishCommand
		if e := json.Unmarshal([]byte(raw), &c); e != nil {
			t.Fatal(e)
		}
		r, e := p.Publish(ctx, u, key, c)
		if e != nil {
			t.Fatal(e)
		}
		hash := sha256.Sum256([]byte(raw))
		if _, e = db.Exec("UPDATE publish_request SET fingerprint=? WHERE publisher_id=? AND request_key=?", hash[:], u.ID, key); e != nil {
			t.Fatal(e)
		}
		before := balance(u)
		replay, e := p.Publish(ctx, u, key, c)
		if e != nil || replay.ErrandID == nil || *replay.ErrandID != *r.ErrandID || balance(u) != before {
			t.Fatal("old committed publish replay changed effects", replay, e)
		}
		check()
	})
	t.Run("atomic_publish_receipt_and_seed_does_not_refill", func(t *testing.T) {
		u := newUser(10000)
		key := uuid.NewString()
		r, e := p.Publish(ctx, u, key, command(1234))
		if e != nil {
			t.Fatal(e)
		}
		if r.ErrandID == nil || r.FrozenCents != 1234 || r.Status != "PUBLISHED" {
			t.Fatalf("result=%+v", r)
		}
		b := balance(u)
		if b.AvailableCents != 8766 || b.FrozenCents != 0 || b.Version != 2 {
			t.Fatalf("balance=%+v", b)
		}
		page, e := q.Ledger(ctx, u, 0, 1)
		if e != nil || len(page.Items) != 1 || page.Items[0].BalanceAfterCents != 8766 || page.NextCursor != "2" {
			t.Fatalf("ledger=%+v err=%v", page, e)
		}
		older, e := q.Ledger(ctx, u, 2, 1)
		if e != nil || len(older.Items) != 1 || older.Items[0].RefType != "OPENING" || older.NextCursor != "" {
			t.Fatalf("older=%+v err=%v", older, e)
		}
		receipt, e := q.Escrow(ctx, u, *r.ErrandID)
		if e != nil || receipt.AmountCents != 1234 || receipt.Status != "HELD" {
			t.Fatalf("escrow=%+v err=%v", receipt, e)
		}
		stranger := u
		stranger.ID++
		_, e = q.Escrow(ctx, stranger, *r.ErrandID)
		requireCode(t, e, "ESCROW_NOT_FOUND")
		arb := auth.User{ID: 9001, CampusID: 1, Role: "ARBITRATOR"}
		if _, e = q.Escrow(ctx, arb, *r.ErrandID); e != nil {
			t.Fatal(e)
		}
		arb.CampusID = 2
		_, e = q.Escrow(ctx, arb, *r.ErrandID)
		requireCode(t, e, "ESCROW_NOT_FOUND")
		tx, e := db.Begin()
		if e != nil {
			t.Fatal(e)
		}
		if e = wallet.SeedOpening(ctx, tx, u.ID, 10000); e != nil {
			tx.Rollback()
			t.Fatal(e)
		}
		if e = tx.Commit(); e != nil {
			t.Fatal(e)
		}
		if balance(u) != b {
			t.Fatal("seed replenished spent money")
		}
		check()
	})
	t.Run("same_key_concurrency_and_fingerprint_conflict", func(t *testing.T) {
		u := newUser(10000)
		key := uuid.NewString()
		var wg sync.WaitGroup
		results := make(chan PublishResult, 20)
		errs := make(chan error, 20)
		for range 20 {
			wg.Add(1)
			go func() { defer wg.Done(); r, e := p.Publish(ctx, u, key, command(199)); results <- r; errs <- e }()
		}
		wg.Wait()
		close(results)
		close(errs)
		for e := range errs {
			if e != nil {
				t.Fatal(e)
			}
		}
		var first int64
		for r := range results {
			if r.ErrandID == nil {
				t.Fatal("missing ID")
			}
			if first == 0 {
				first = *r.ErrandID
			}
			if *r.ErrandID != first {
				t.Fatal("duplicate effect")
			}
		}
		if balance(u).AvailableCents != 9801 {
			t.Fatal("repeated debit")
		}
		_, e := p.Publish(ctx, u, key, command(200))
		requireCode(t, e, "IDEMPOTENCY_CONFLICT")
		other := u
		other.ID++
		notSeen, e := p.Result(ctx, other, key)
		if e != nil || notSeen.State != "NOT_OBSERVED" {
			t.Fatal("request leaked to different user")
		}
		check()
	})
	t.Run("distinct_keys_concurrent_balance_exhaustion", func(t *testing.T) {
		u := newUser(5000)
		var wg sync.WaitGroup
		errs := make(chan error, 20)
		for range 20 {
			wg.Add(1)
			go func() { defer wg.Done(); _, e := p.Publish(ctx, u, uuid.NewString(), command(1000)); errs <- e }()
		}
		wg.Wait()
		close(errs)
		success := 0
		for e := range errs {
			if e == nil {
				success++
			} else {
				requireCode(t, e, "INSUFFICIENT_BALANCE")
			}
		}
		if success != 5 || balance(u).AvailableCents != 0 {
			t.Fatalf("success=%d balance=%+v", success, balance(u))
		}
		check()
	})
	t.Run("rejections_and_boundaries_are_stable", func(t *testing.T) {
		u := newUser(0)
		key := uuid.NewString()
		_, e := p.Publish(ctx, u, key, command(1))
		requireCode(t, e, "INSUFFICIENT_BALANCE")
		r, e := p.Result(ctx, u, key)
		if e != nil || r.State != "REJECTED" || r.ErrorCode != "INSUFFICIENT_BALANCE" || r.FrozenCents != 0 {
			t.Fatalf("r=%+v err=%v", r, e)
		}
		for _, amount := range []int64{0, -1, math.MinInt64} {
			_, e = p.Publish(ctx, u, uuid.NewString(), command(amount))
			requireCode(t, e, "INVALID_REQUEST")
		}
		_, e = p.Publish(ctx, u, uuid.NewString(), command(math.MaxInt64))
		requireCode(t, e, "INSUFFICIENT_BALANCE")
		for _, c := range []PublishCommand{{CampusID: 1, Type: "INVALID", Title: "title", RewardCents: 1, SlotTotal: 1}, {CampusID: 1, Type: "DELIVERY", Title: "", RewardCents: 1, SlotTotal: 1}, {CampusID: 1, Type: "DELIVERY", Title: string([]byte{0xff}), RewardCents: 1, SlotTotal: 1}, {CampusID: 1, Type: "DELIVERY", Title: "a\nb", RewardCents: 1, SlotTotal: 1}, {CampusID: 1, Type: "DELIVERY", Title: "title", RewardCents: 1, SlotTotal: 2}} {
			_, e = p.Publish(ctx, u, uuid.NewString(), c)
			requireCode(t, e, "INVALID_REQUEST")
		}
		c := command(1)
		c.CampusID = 2
		_, e = p.Publish(ctx, u, uuid.NewString(), c)
		requireCode(t, e, "FORBIDDEN")
		u2 := newUser(100)
		var prior int64
		if e = db.QueryRow("SELECT available FROM wallet_account WHERE id=1").Scan(&prior); e != nil {
			t.Fatal(e)
		}
		if _, e = db.Exec("UPDATE wallet_account SET available=? WHERE id=1", math.MaxInt64); e != nil {
			t.Fatal(e)
		}
		limitKey := uuid.NewString()
		_, e = p.Publish(ctx, u2, limitKey, command(1))
		requireCode(t, e, "BALANCE_LIMIT")
		if _, e = db.Exec("UPDATE wallet_account SET available=? WHERE id=1", prior); e != nil {
			t.Fatal(e)
		}
		_, e = p.Publish(ctx, u2, limitKey, command(1))
		requireCode(t, e, "BALANCE_LIMIT")
		if balance(u2).AvailableCents != 100 {
			t.Fatal("persisted rejection changed")
		}
		check()
	})
	t.Run("SQL_failure_between_posting_legs_rolls_back", func(t *testing.T) {
		u := newUser(1000)
		key := uuid.NewString()
		// A verification-only constraint fails the CREDIT insert after the debit, first
		// ledger leg and escrow-account update have really executed inside the transaction.
		query := fmt.Sprintf("ALTER TABLE wallet_ledger ADD CONSTRAINT ck_d2_injected CHECK (NOT (user_id=%d AND direction='CREDIT' AND ref_type='ESCROW'))", u.ID)
		if _, e := db.Exec(query); e != nil {
			t.Fatal(e)
		}
		defer func() {
			if _, e := db.Exec("ALTER TABLE wallet_ledger DROP CHECK ck_d2_injected"); e != nil {
				t.Error(e)
			}
		}()
		_, e := p.Publish(ctx, u, key, command(123))
		requireCode(t, e, "INTERNAL_ERROR")
		if balance(u).AvailableCents != 1000 {
			t.Fatal("debit survived failure of the credit leg")
		}
		r, e := p.Result(ctx, u, key)
		if e != nil || r.State != "NOT_OBSERVED" {
			t.Fatalf("r=%+v err=%v", r, e)
		}
		var n int
		if e = db.QueryRow("SELECT COUNT(*) FROM wallet_ledger WHERE user_id=? AND ref_type='ESCROW'", u.ID).Scan(&n); e != nil || n != 0 {
			t.Fatalf("partial ledger count=%d err=%v", n, e)
		}
		check()
	})
	t.Run("missing_wallet_and_stale_identity_do_not_create_funds", func(t *testing.T) {
		u := newUser(0)
		if _, e := db.Exec("DELETE FROM wallet_account WHERE owner_id=? AND owner_type='USER'", u.ID); e != nil {
			t.Fatal(e)
		}
		key := uuid.NewString()
		_, e := p.Publish(ctx, u, key, command(1))
		requireCode(t, e, "ACCOUNT_NOT_FOUND")
		r, e := p.Result(ctx, u, key)
		if e != nil || r.State != "REJECTED" || r.ErrorCode != "ACCOUNT_NOT_FOUND" {
			t.Fatalf("r=%+v err=%v", r, e)
		}
		u = newUser(100)
		for _, change := range []string{"active=0", "campus_id=2"} {
			if _, e = db.Exec("UPDATE app_user SET "+change+" WHERE id=?", u.ID); e != nil {
				t.Fatal(e)
			}
			key = uuid.NewString()
			_, e = p.Publish(ctx, u, key, command(1))
			requireCode(t, e, "FORBIDDEN")
			if _, e = db.Exec("UPDATE app_user SET active=1,campus_id=1 WHERE id=?", u.ID); e != nil {
				t.Fatal(e)
			}
			if balance(u).AvailableCents != 100 {
				t.Fatal("stale identity changed funds")
			}
		}
		check()
	})
	t.Run("rollback_after_all_writes_and_retry", func(t *testing.T) {
		u := newUser(1000)
		key := uuid.NewString()
		_, e := p.publish(ctx, u, key, command(101), func(tx *sql.Tx) error {
			var n int
			if e := tx.QueryRow("SELECT COUNT(*) FROM escrow_order WHERE publisher_id=?", u.ID).Scan(&n); e != nil {
				return e
			}
			if n != 1 {
				return fmt.Errorf("hook did not reach real writes")
			}
			return errors.New("injected failure after writes")
		}, nil)
		requireCode(t, e, "INTERNAL_ERROR")
		if balance(u).AvailableCents != 1000 {
			t.Fatal("partial debit")
		}
		r, e := p.Result(ctx, u, key)
		if e != nil || r.State != "NOT_OBSERVED" {
			t.Fatalf("r=%+v err=%v", r, e)
		}
		var n int
		if e = db.QueryRow("SELECT COUNT(*) FROM errand WHERE publisher_id=?", u.ID).Scan(&n); e != nil || n != 0 {
			t.Fatalf("partial task count=%d err=%v", n, e)
		}
		if _, e = p.Publish(ctx, u, key, command(101)); e != nil {
			t.Fatal(e)
		}
		check()
	})
	t.Run("commit_ack_lost_and_not_sent_both_recover_same_key", func(t *testing.T) {
		for _, committed := range []bool{true, false} {
			u := newUser(1000)
			key := uuid.NewString()
			_, e := p.publish(ctx, u, key, command(111), nil, func(tx *sql.Tx) error {
				if committed {
					if e := tx.Commit(); e != nil {
						return e
					}
				}
				return io.EOF
			})
			requireCode(t, e, "PUBLISH_RESULT_UNKNOWN")
			r, e := p.Result(ctx, u, key)
			if e != nil {
				t.Fatal(e)
			}
			want := "NOT_OBSERVED"
			if committed {
				want = "SUCCEEDED"
			}
			if r.State != want {
				t.Fatalf("state=%s want %s", r.State, want)
			}
			replay, e := p.Publish(ctx, u, key, command(111))
			if e != nil {
				t.Fatal(e)
			}
			if committed && *replay.ErrandID != *r.ErrandID {
				t.Fatal("committed task duplicated")
			}
			if balance(u).AvailableCents != 889 {
				t.Fatal("incorrect debit after recovery")
			}
		}
		check()
	})
	t.Run("not_observed_during_inflight_tx_is_not_final_failure", func(t *testing.T) {
		u := newUser(1000)
		key := uuid.NewString()
		entered := make(chan struct{})
		release := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			_, e := p.publish(ctx, u, key, command(99), func(*sql.Tx) error { close(entered); <-release; return nil }, nil)
			done <- e
		}()
		<-entered
		r, e := p.Result(ctx, u, key)
		if e != nil || r.State != "NOT_OBSERVED" {
			close(release)
			t.Fatalf("r=%+v err=%v", r, e)
		}
		timed, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
		defer cancel()
		_, e = p.Publish(timed, u, key, command(99))
		if e == nil {
			close(release)
			t.Fatal("inflight duplicate did not wait")
		}
		close(release)
		if e = <-done; e != nil {
			t.Fatal(e)
		}
		if _, e = p.Publish(ctx, u, key, command(99)); e != nil {
			t.Fatal(e)
		}
		if balance(u).AvailableCents != 901 {
			t.Fatal("inflight retry duplicated")
		}
		check()
	})
	t.Run("real_driver_commit_OK_packet_lost", func(t *testing.T) {
		u := newUser(1000)
		key := uuid.NewString()
		addr, dropped := commitAckProxy(t)
		proxyDB := databaseViaProxy(t, addr)
		proxied := &PublishStore{DB: proxyDB, IDs: ids}
		_, e := proxied.Publish(ctx, u, key, command(125))
		requireCode(t, e, "PUBLISH_RESULT_UNKNOWN")
		select {
		case <-dropped:
		case <-time.After(time.Second):
			t.Fatal("COMMIT response was not intercepted")
		}
		r, e := p.Result(ctx, u, key)
		if e != nil || r.State != "SUCCEEDED" {
			t.Fatalf("r=%+v err=%v", r, e)
		}
		replay, e := p.Publish(ctx, u, key, command(125))
		if e != nil || *replay.ErrandID != *r.ErrandID || balance(u).AvailableCents != 875 {
			t.Fatalf("replay=%+v err=%v", replay, e)
		}
		check()
	})
	t.Run("reconciliation_detects_snapshot_and_ledger_chain_corruption", func(t *testing.T) {
		u := newUser(1000)
		r, e := p.Publish(ctx, u, uuid.NewString(), command(10))
		if e != nil {
			t.Fatal(e)
		}
		if _, e = db.Exec("UPDATE wallet_ledger SET balance_after=balance_after+1 WHERE ref_id=? AND direction='DEBIT'", *r.ErrandID); e != nil {
			t.Fatal(e)
		}
		checks, e := q.Reconcile(ctx)
		if e != nil {
			t.Fatal(e)
		}
		detected := false
		for _, c := range checks {
			if c.Name == "ledger_balance_after_chain" && c.Violations == 1 {
				detected = true
			}
		}
		if !detected {
			t.Fatal("corrupt ledger was not detected")
		}
		if _, e = db.Exec("UPDATE wallet_ledger SET balance_after=balance_after-1 WHERE ref_id=? AND direction='DEBIT'", *r.ErrandID); e != nil {
			t.Fatal(e)
		}
		check()
	})
	t.Run("exact_int64_capacity_and_decimal_global_totals", func(t *testing.T) {
		var held int64
		if e := db.QueryRow("SELECT available FROM wallet_account WHERE id=1").Scan(&held); e != nil {
			t.Fatal(e)
		}
		u := newUser(math.MaxInt64 - held)
		if _, e := p.Publish(ctx, u, uuid.NewString(), command(math.MaxInt64-held)); e != nil {
			t.Fatal(e)
		}
		if balance(u).AvailableCents != 0 {
			t.Fatal("int64 boundary lost precision")
		}
		if e := db.QueryRow("SELECT available FROM wallet_account WHERE id=1").Scan(&held); e != nil || held != math.MaxInt64 {
			t.Fatalf("held=%d err=%v", held, e)
		}
		check()
	})
}
