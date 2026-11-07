package mysql

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"

	"github.com/yumestk/CampusGo/internal/auth"
	"github.com/yumestk/CampusGo/internal/platform/database"
	"github.com/yumestk/CampusGo/internal/platform/idgen"
	"github.com/yumestk/CampusGo/internal/platform/rediscache"
)

func TestIntegrationGrab(t *testing.T) {
	if os.Getenv("CAMPUSGO_D3_INTEGRATION") != "1" {
		t.Skip("run make verify-d3")
	}
	ctx := context.Background()
	db, err := database.Open(ctx, "campusgo_verification")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cache, err := rediscache.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cache.Close() })
	ids, _ := idgen.New(1019)
	namespace := "d3-" + uuid.NewString()
	g := &TaskStore{DB: db, Redis: cache, IDs: ids, Namespace: namespace}
	manifest := func(kind string, id int64) {
		t.Helper()
		f, e := os.OpenFile(filepath.Join("..", "..", "..", "work", "d3-fixtures.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if e != nil {
			t.Fatal(e)
		}
		defer f.Close()
		if e = json.NewEncoder(f).Encode(map[string]any{"kind": kind, "id": fmt.Sprint(id), "namespace": namespace}); e != nil {
			t.Fatal(e)
		}
	}
	owned := []int64{}
	tasks := []int64{}
	execSQL := func(query string, args ...any) {
		t.Helper()
		if _, e := db.ExecContext(ctx, query, args...); e != nil {
			t.Fatal(e)
		}
	}
	newID := func() int64 {
		t.Helper()
		id, e := ids.Next(ctx)
		if e != nil {
			t.Fatal(e)
		}
		return id
	}
	newUser := func(campus int64) auth.User {
		t.Helper()
		id := newID()
		manifest("user", id)
		execSQL("INSERT INTO app_user (id,campus_id,display_name) VALUES (?,?,'D3验证用户')", id, campus)
		owned = append(owned, id)
		return auth.User{ID: id, CampusID: campus, Role: "STUDENT"}
	}
	publisher := newUser(1)
	runner := newUser(1)
	newTask := func() int64 {
		t.Helper()
		id := newID()
		manifest("errand", id)
		execSQL("INSERT INTO errand (id,campus_id,publisher_id,type,title,reward_amount,slot_total,status,version) VALUES (?,1,?,'DELIVERY','D3验证任务',100,1,'PUBLISHED',1)", id, publisher.ID)
		tasks = append(tasks, id)
		return id
	}
	t.Cleanup(func() {
		// Only IDs created by this fixture are removed. No shared database/Redis flush.
		for _, id := range tasks {
			for _, q := range []string{"DELETE FROM grab_repair_task WHERE errand_id=?", "DELETE FROM local_message WHERE errand_id=?", "DELETE FROM grab_record WHERE errand_id=?", "DELETE FROM grab_request WHERE errand_id=?", "DELETE FROM errand_status_log WHERE errand_id=?", "DELETE FROM errand WHERE id=?"} {
				if _, e := db.Exec(q, id); e != nil {
					t.Error(e)
				}
			}
		}
		for _, id := range owned {
			db.Exec("DELETE FROM credit_score WHERE user_id=?", id)
			if _, e := db.Exec("DELETE FROM app_user WHERE id=?", id); e != nil {
				t.Error(e)
			}
		}
		var cursor uint64
		for {
			keys, next, e := cache.Scan(ctx, cursor, "campusgo:"+namespace+":*", 100).Result()
			if e != nil {
				t.Error(e)
				break
			}
			if len(keys) > 0 {
				cache.Del(ctx, keys...)
			}
			cursor = next
			if cursor == 0 {
				break
			}
		}
	})
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if e := db.QueryRow(query, args...).Scan(&n); e != nil {
			t.Fatal(e)
		}
		return n
	}
	expire := func(id int64) {
		execSQL("UPDATE grab_request SET lease_until=TIMESTAMPADD(SECOND,-1,CURRENT_TIMESTAMP(3)) WHERE errand_id=? AND state='PROCESSING'", id)
	}
	recoverAll := func() {
		t.Helper()
		for range 3 {
			execSQL("UPDATE grab_repair_task SET next_run_at=CURRENT_TIMESTAMP(3) WHERE state='PENDING' AND event_key LIKE '%' AND errand_id IN (SELECT id FROM errand WHERE publisher_id=?)", publisher.ID)
			if e := g.RecoverBatch(ctx, 100); e != nil {
				t.Fatal(e)
			}
		}
	}
	read := func(key string, id int64) GrabResult {
		t.Helper()
		r, e := g.Result(ctx, runner, key, id)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	taskStatus := func(id int64) string {
		t.Helper()
		var s string
		if e := db.QueryRow("SELECT status FROM errand WHERE id=?", id).Scan(&s); e != nil {
			t.Fatal(e)
		}
		return s
	}
	var walletBefore string
	if err = db.QueryRow("SELECT GROUP_CONCAT(CONCAT(id,':',available,':',frozen,':',version) ORDER BY id) FROM wallet_account").Scan(&walletBefore); err != nil {
		t.Fatal(err)
	}

	t.Run("pre_refactor_persisted_grab_request_resume", func(t *testing.T) {
		id := newTask()
		key := uuid.NewString()
		raw := strings.ReplaceAll(legacyTaskCommand(t, "grab"), "9007199254741001", fmt.Sprint(id))
		hash := sha256.Sum256([]byte(raw))
		requestID := newID()
		execSQL("INSERT INTO grab_request(id,campus_id,runner_id,request_key,errand_id,fingerprint,base_version,round,reservation_token,lease_until) VALUES(?,1,?,?,?,?,1,0,?,TIMESTAMPADD(SECOND,30,CURRENT_TIMESTAMP(3)))", requestID, runner.ID, key, id, hash[:], uuid.NewString())
		var c GrabCommand
		if e := json.Unmarshal([]byte(raw), &c); e != nil {
			t.Fatal(e)
		}
		r, e := g.Grab(ctx, runner, key, c)
		if e != nil || r.State != "PROCESSING" {
			t.Fatal("old in-flight request must stay provisional", r, e)
		}
		expire(id)
		recoverAll()
		r, e = g.Grab(ctx, runner, key, c)
		if e != nil || r.State != "EXPIRED" || count("SELECT COUNT(*) FROM grab_request WHERE id=?", requestID) != 1 || count("SELECT COUNT(*) FROM grab_record WHERE errand_id=?", id) != 0 {
			t.Fatal("old expired request replay was re-executed", r, e)
		}
	})
	t.Run("same_round_48_runners_one_SQL_winner", func(t *testing.T) {
		id := newTask()
		users := []auth.User{}
		for range 48 {
			users = append(users, newUser(1))
		}
		start := make(chan struct{})
		results := make(chan GrabResult, 48)
		failures := make(chan error, 48)
		var wg sync.WaitGroup
		for _, u := range users {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				r, e := g.Grab(ctx, u, uuid.NewString(), GrabCommand{ErrandID: id, ExpectedRound: 0})
				results <- r
				failures <- e
			}()
		}
		close(start)
		wg.Wait()
		close(results)
		close(failures)
		for e := range failures {
			if e != nil {
				requireCode(t, e, "DEPENDENCY_UNAVAILABLE")
			}
		}
		successes := 0
		for r := range results {
			if r.State == "SUCCEEDED" {
				successes++
			}
		}
		if successes != 1 || count("SELECT COUNT(*) FROM grab_record WHERE errand_id=? AND result='GRABBED'", id) != 1 || count("SELECT COUNT(*) FROM errand_status_log WHERE errand_id=? AND to_status='LOCKED'", id) != 1 {
			t.Fatalf("winners=%d", successes)
		}
		if count("SELECT COUNT(*) FROM local_message WHERE errand_id=?", id) != 1 || taskStatus(id) != "LOCKED" {
			t.Fatal("atomic lock/timeout missing")
		}
		expire(id)
		recoverAll()
		if count("SELECT COUNT(*) FROM grab_request WHERE errand_id=? AND state='PROCESSING'", id) != 0 {
			t.Fatal("requests did not converge")
		}
	})
	t.Run("same_request_24_concurrent_replays_and_fingerprint_conflict", func(t *testing.T) {
		id := newTask()
		key := uuid.NewString()
		var wg sync.WaitGroup
		errs := make(chan error, 24)
		for range 24 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, e := g.Grab(ctx, runner, key, GrabCommand{ErrandID: id, ExpectedRound: 0})
				errs <- e
			}()
		}
		wg.Wait()
		close(errs)
		for e := range errs {
			if e != nil {
				t.Error(e)
			}
		}
		r := read(key, id)
		if r.State != "SUCCEEDED" || count("SELECT COUNT(*) FROM grab_record WHERE errand_id=?", id) != 1 {
			t.Fatalf("result=%+v", r)
		}
		for _, c := range []GrabCommand{{ErrandID: id, ExpectedRound: 1}, {ErrandID: newTask(), ExpectedRound: 0}} {
			_, e := g.Grab(ctx, runner, key, c)
			requireCode(t, e, "IDEMPOTENCY_CONFLICT")
		}
		other := newUser(1)
		r, e := g.Result(ctx, other, key, id)
		if e != nil || r.State != "NOT_OBSERVED" {
			t.Fatal("cross-user result leak", r, e)
		}
	})
	t.Run("eligibility_round_roles_credit_and_inflight_excludes_LOCKED", func(t *testing.T) {
		cases := []struct {
			user  auth.User
			round int
			code  string
		}{{publisher, 0, "ERRAND_NOT_GRABBABLE"}, {runner, 1, "STALE_VERSION"}}
		for _, tc := range cases {
			id := newTask()
			r, e := g.Grab(ctx, tc.user, uuid.NewString(), GrabCommand{ErrandID: id, ExpectedRound: tc.round})
			if e != nil || r.State != "REJECTED" || r.ErrorCode != tc.code {
				t.Fatalf("%+v %v want %s", r, e, tc.code)
			}
		}
		id := newTask()
		_, e := g.Grab(ctx, newUser(2), uuid.NewString(), GrabCommand{ErrandID: id, ExpectedRound: 0})
		requireCode(t, e, "ERRAND_NOT_FOUND")
		execSQL("INSERT INTO credit_score (user_id,score) VALUES (?,39)", runner.ID)
		r, e := g.Grab(ctx, runner, uuid.NewString(), GrabCommand{ErrandID: newTask(), ExpectedRound: 0})
		if e != nil || r.ErrorCode != "CREDIT_TOO_LOW" {
			t.Fatal(r, e)
		}
		execSQL("UPDATE credit_score SET score=60 WHERE user_id=?", runner.ID)
		ongoing := []int64{}
		for range 5 {
			id := newTask()
			ongoing = append(ongoing, id)
			execSQL("UPDATE errand SET grabber_id=?,status='ACCEPTED',slot_taken=1 WHERE id=?", runner.ID, id)
		}
		r, e = g.Grab(ctx, runner, uuid.NewString(), GrabCommand{ErrandID: newTask(), ExpectedRound: 0})
		if e != nil || r.ErrorCode != "TOO_MANY_ONGOING" {
			t.Fatal(r, e)
		}
		for _, id := range ongoing {
			execSQL("UPDATE errand SET status='LOCKED' WHERE id=?", id)
		}
		r, e = g.Grab(ctx, runner, uuid.NewString(), GrabCommand{ErrandID: newTask(), ExpectedRound: 0})
		if e != nil || r.State != "SUCCEEDED" {
			t.Fatal("LOCKED incorrectly counted", r, e)
		}
	})
	t.Run("database_clock_and_token_fence_own_request_expiry", func(t *testing.T) {
		id := newTask()
		key := uuid.NewString()
		r, _, e := g.registerGrab(ctx, runner, key, GrabCommand{ErrandID: id, ExpectedRound: 0}, make([]byte, 32))
		if e != nil {
			t.Fatal(e)
		}
		result, e := g.finishGrab(ctx, r, "EXPIRED", "REQUEST_EXPIRED")
		if e != nil || result.State != "PROCESSING" {
			t.Fatal("Redis clock overrode SQL lease", result, e)
		}
		stale := r
		stale.Token = uuid.NewString()
		result, e = g.finishGrab(ctx, stale, "REJECTED", "GRAB_CONFLICT")
		if e != nil || result.State != "PROCESSING" {
			t.Fatal("old token terminated current owner", result, e)
		}
		result, e = g.commitGrab(ctx, runner, stale, nil)
		if e != nil || result.State != "PROCESSING" || taskStatus(id) != "PUBLISHED" {
			t.Fatal("stale token committed", result, e)
		}
		expire(id)
		recoverAll()
		arb := newUser(1)
		execSQL("UPDATE app_user SET role='ARBITRATOR' WHERE id=?", arb.ID)
		arb.Role = "ARBITRATOR"
		result, e = g.Grab(ctx, arb, uuid.NewString(), GrabCommand{ErrandID: newTask(), ExpectedRound: 0})
		if e != nil || result.State != "SUCCEEDED" {
			t.Fatal("added restriction absent in original system", result, e)
		}
	})
	t.Run("SQL_failure_rolls_back_task_record_log_timeout_and_result", func(t *testing.T) {
		id := newTask()
		key := uuid.NewString()
		execSQL(fmt.Sprintf("ALTER TABLE local_message ADD CONSTRAINT ck_d3_fail CHECK(errand_id<>%d)", id))
		_, e := g.Grab(ctx, runner, key, GrabCommand{ErrandID: id, ExpectedRound: 0})
		execSQL("ALTER TABLE local_message DROP CHECK ck_d3_fail")
		requireCode(t, e, "INTERNAL_ERROR")
		if taskStatus(id) != "PUBLISHED" || count("SELECT COUNT(*) FROM grab_record WHERE errand_id=?", id) != 0 || count("SELECT COUNT(*) FROM errand_status_log WHERE errand_id=?", id) != 0 || read(key, id).State != "PROCESSING" {
			t.Fatal("partial transaction survived")
		}
		expire(id)
		recoverAll()
		if read(key, id).State != "EXPIRED" {
			t.Fatal("failed request did not expire")
		}
	})
	t.Run("lease_expires_between_writes_and_COMMIT", func(t *testing.T) {
		id := newTask()
		key := uuid.NewString()
		_, e := g.grab(ctx, runner, key, GrabCommand{ErrandID: id, ExpectedRound: 0}, func(phase string) {
			if phase == "reserved" {
				execSQL("UPDATE grab_request SET lease_until=TIMESTAMPADD(MICROSECOND,100000,CURRENT_TIMESTAMP(3)) WHERE errand_id=?", id)
			}
			if phase == "before_commit" {
				time.Sleep(150 * time.Millisecond)
			}
		})
		requireCode(t, e, "DEPENDENCY_UNAVAILABLE")
		if taskStatus(id) != "PUBLISHED" || count("SELECT COUNT(*) FROM grab_record WHERE errand_id=?", id) != 0 {
			t.Fatal("expired owner committed")
		}
		recoverAll()
	})
	t.Run("real_driver_loses_business_COMMIT_ack_no_release", func(t *testing.T) {
		id := newTask()
		key := uuid.NewString()
		addr, dropped := commitAckProxyAt(t, 2)
		proxyDB := databaseViaProxy(t, addr)
		proxied := *g
		proxied.DB = proxyDB
		_, e := proxied.Grab(ctx, runner, key, GrabCommand{ErrandID: id, ExpectedRound: 0})
		requireCode(t, e, "GRAB_RESULT_UNKNOWN")
		select {
		case <-dropped:
		case <-time.After(time.Second):
			t.Fatal("business COMMIT not intercepted")
		}
		if read(key, id).State != "SUCCEEDED" {
			t.Fatal("committed result lost")
		}
		pending, e := cache.HGet(ctx, g.stateKey(id), "reservationToken").Result()
		if e != nil || pending == "" {
			t.Fatal("unknown commit reservation released", e)
		}
		recoverAll()
		if count("SELECT COUNT(*) FROM grab_record WHERE errand_id=?", id) != 1 {
			t.Fatal("duplicate SQL effect")
		}
		if _, e = g.Grab(ctx, runner, key, GrabCommand{ErrandID: id, ExpectedRound: 0}); e != nil {
			t.Fatal(e)
		}
		hash, e := cache.HGetAll(ctx, g.stateKey(id)).Result()
		if e != nil || hash["status"] != "LOCKED" || hash["reservationToken"] != "" {
			t.Fatal("projection not repaired", hash, e)
		}
	})
	t.Run("real_Redis_failure_retains_request_and_recovers", func(t *testing.T) {
		l, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		addr := l.Addr().String()
		l.Close()
		broken := *g
		broken.Redis = redis.NewClient(&redis.Options{Addr: addr, MaxRetries: -1, DialTimeout: 30 * time.Millisecond})
		defer broken.Redis.Close()
		id := newTask()
		key := uuid.NewString()
		_, e = broken.Grab(ctx, runner, key, GrabCommand{ErrandID: id, ExpectedRound: 0})
		requireCode(t, e, "DEPENDENCY_UNAVAILABLE")
		if taskStatus(id) != "PUBLISHED" || read(key, id).State != "PROCESSING" {
			t.Fatal("dependency failure fabricated final result")
		}
		expire(id)
		recoverAll()
		if read(key, id).State != "EXPIRED" {
			t.Fatal("dependency request stuck")
		}
	})
	t.Run("old_revoke_cannot_clear_new_pending_and_tombstone_blocks_resurrection", func(t *testing.T) {
		id := newTask()
		hash := make([]byte, 32)
		old, _, e := g.registerGrab(ctx, runner, uuid.NewString(), GrabCommand{ErrandID: id, ExpectedRound: 0}, hash)
		if e != nil {
			t.Fatal(e)
		}
		other, _, e := g.registerGrab(ctx, newUser(1), uuid.NewString(), GrabCommand{ErrandID: id, ExpectedRound: 0}, hash)
		if e != nil {
			t.Fatal(e)
		}
		if e = g.syncProjection(ctx, id); e != nil {
			t.Fatal(e)
		}
		if code, e := g.acquire(ctx, old); e != nil || code != 0 {
			t.Fatal(code, e)
		}
		if e = g.revoke(ctx, old); e != nil {
			t.Fatal(e)
		}
		if code, e := g.acquire(ctx, other); e != nil || code != 0 {
			t.Fatal(code, e)
		}
		if e = g.revoke(ctx, old); e != nil {
			t.Fatal(e)
		}
		token, e := cache.HGet(ctx, g.stateKey(id), "reservationToken").Result()
		if e != nil || token != other.Token {
			t.Fatal("new pending cleared")
		}
		if code, e := g.acquire(ctx, old); e != nil || code != 6 {
			t.Fatal("revoked request resurrected", code, e)
		}
		expire(id)
		recoverAll()
	})
	t.Run("string_versions_above_JS_precision_and_missing_projection_rebuild", func(t *testing.T) {
		id := newTask()
		execSQL("UPDATE errand SET version=9007199254740993 WHERE id=?", id)
		if e := g.syncProjection(ctx, id); e != nil {
			t.Fatal(e)
		}
		c, e := scan(db.QueryRow("SELECT "+columns+" FROM errand WHERE id=?", id))
		if e != nil {
			t.Fatal(e)
		}
		c.Version--
		code, e := g.script(ctx, "apply_projection", []string{g.stateKey(id)}, append(projectionArgs(c), "0")...)
		if e != nil || code != 2 {
			t.Fatal("version rounded", code, e)
		}
		r, e := g.Grab(ctx, runner, uuid.NewString(), GrabCommand{ErrandID: id, ExpectedRound: 0})
		if e != nil || r.State != "SUCCEEDED" {
			t.Fatal(r, e)
		}
		cache.Del(ctx, g.stateKey(id))
		if _, e = g.ReconcileBatch(ctx, 0, 100); e != nil {
			t.Fatal(e)
		}
		recoverAll()
		hash, e := cache.HGetAll(ctx, g.stateKey(id)).Result()
		if e != nil || hash["dbVersion"] != "9007199254740994" || hash["status"] != "LOCKED" {
			t.Fatal(hash, e)
		}
	})
	t.Run("two_workers_fencing_dead_letter_and_audited_replay", func(t *testing.T) {
		recoverAll()
		id := newTask()
		tx, e := db.Begin()
		if e != nil {
			t.Fatal(e)
		}
		if e = enqueueRepair(ctx, tx, 1, id, "test:"+uuid.NewString(), "PROJECTION", nil, 0); e != nil {
			t.Fatal(e)
		}
		if e = tx.Commit(); e != nil {
			t.Fatal(e)
		}
		a, e := g.ClaimRepairs(ctx, 100)
		if e != nil {
			t.Fatal(e)
		}
		b, e := g.ClaimRepairs(ctx, 100)
		if e != nil {
			t.Fatal(e)
		}
		seen := map[int64]bool{}
		for _, task := range a {
			seen[task.ID] = true
		}
		for _, task := range b {
			if seen[task.ID] {
				t.Fatal("double claim")
			}
		}
		var task RepairTask
		for _, r := range a {
			if r.ErrandID == id {
				task = r
			}
		}
		if task.ID == 0 {
			t.Fatal("test repair not claimed")
		}
		execSQL("UPDATE grab_repair_task SET locked_until=TIMESTAMPADD(SECOND,-1,CURRENT_TIMESTAMP(3)) WHERE id=?", task.ID)
		if ok, e := g.CompleteRepair(ctx, task, nil); e != nil || ok {
			t.Fatal("expired owner completed")
		}
		next, e := g.ClaimRepairs(ctx, 100)
		if e != nil {
			t.Fatal(e)
		}
		var current RepairTask
		for _, r := range next {
			if r.ID == task.ID {
				current = r
			}
		}
		if current.WorkerToken == "" || current.WorkerToken == task.WorkerToken {
			t.Fatal("lease did not change")
		}
		if ok, e := g.CompleteRepair(ctx, task, io.EOF); e != nil || ok {
			t.Fatal("old worker overwrote new owner")
		}
		if ok, e := g.CompleteRepair(ctx, current, io.EOF); e != nil || !ok {
			t.Fatal(ok, e)
		}
		execSQL("UPDATE grab_repair_task SET retry_count=9,next_run_at=CURRENT_TIMESTAMP(3) WHERE id=?", task.ID)
		last, e := g.ClaimRepairs(ctx, 100)
		if e != nil {
			t.Fatal(e)
		}
		for _, r := range last {
			if r.ID == task.ID {
				g.CompleteRepair(ctx, r, io.EOF)
			}
		}
		dead, e := g.DeadRepairs(ctx)
		if e != nil {
			t.Fatal(e)
		}
		found := false
		for _, r := range dead {
			if r.ID == task.ID {
				found = true
				if r.Attempts != 10 || r.LastError == "" {
					t.Fatal(r)
				}
			}
		}
		if !found {
			t.Fatal("dead task hidden")
		}
		if _, e = g.ReplayRepair(ctx, task.ID, ""); e == nil {
			t.Fatal("unaudited replay")
		}
		if ok, e := g.ReplayRepair(ctx, task.ID, "D3 verification: Redis restored"); e != nil || !ok {
			t.Fatal(ok, e)
		}
		recoverAll()
		if count("SELECT COUNT(*) FROM grab_repair_task WHERE id=? AND state='DONE' AND replay_count=1 AND last_replay_reason<>''", task.ID) != 1 {
			t.Fatal("replay not completed/audited")
		}
		for _, r := range append(a, b...) {
			if r.ID != task.ID {
				g.CompleteRepair(ctx, r, nil)
			}
		}
	})
	t.Run("cache_negative_bloom_stale_rebuild_and_SQL_authorization", func(t *testing.T) {
		id := newTask()
		registry := prometheus.NewRegistry()
		details := NewDetailCache(cache, namespace, registry)
		refreshCtx, stopRefresh := context.WithCancel(ctx)
		refreshDone := make(chan struct{})
		go func() { defer close(refreshDone); details.RunRefresh(refreshCtx, db) }()
		defer func() { stopRefresh(); <-refreshDone }()
		q := Query{DB: db, Cache: details}
		details.put(ctx, id, Card{}, true)
		cache.Del(ctx, details.bloomKey())
		c, e := q.Detail(ctx, runner, id)
		if e != nil || c.Status != "PUBLISHED" || len(c.AvailableActions) != 1 {
			t.Fatal("negative hint vetoed SQL", c, e)
		}
		if _, e = q.Detail(ctx, runner, id); e != nil {
			t.Fatal(e)
		}
		metrics, e := registry.Gather()
		if e != nil {
			t.Fatal(e)
		}
		counts := map[string]float64{}
		for _, family := range metrics {
			for _, m := range family.Metric {
				counts[m.Label[0].GetValue()] = m.Counter.GetValue()
			}
		}
		if counts["hit"] != 1 || counts["sql_authority_query"] != 2 || counts["sql_body_query"] != 1 {
			t.Fatal("misleading cache metrics", counts)
		}
		before, _ := cache.Get(ctx, detailKey(namespace, id, 0)).Result()
		_, e = q.Detail(ctx, auth.User{ID: runner.ID, CampusID: 2, Role: "STUDENT"}, id)
		requireCode(t, e, "ERRAND_NOT_FOUND")
		after, _ := cache.Get(ctx, detailKey(namespace, id, 0)).Result()
		if before != after {
			t.Fatal("cross-campus miss poisoned shared cache")
		}

		for shard := range 4 {
			raw, _ := cache.Get(ctx, detailKey(namespace, id, shard)).Bytes()
			var stored cachedDetail
			json.Unmarshal(raw, &stored)
			stored.Exp = 1
			raw, _ = json.Marshal(stored)
			cache.Set(ctx, detailKey(namespace, id, shard), raw, time.Minute)
		}
		lock := fmt.Sprintf("campusgo:%s:detail:rebuild:%d", namespace, id)
		cache.Set(ctx, lock, "another-owner", time.Minute)
		if _, e = q.Detail(ctx, runner, id); e != nil {
			t.Fatal(e)
		}
		cache.Del(ctx, lock)
		if _, e = q.Detail(ctx, runner, id); e != nil {
			t.Fatal(e)
		}
		deadline := time.Now().Add(2 * time.Second)
		for {
			raw, _ := cache.Get(ctx, detailKey(namespace, id, 0)).Bytes()
			var fresh cachedDetail
			json.Unmarshal(raw, &fresh)
			if fresh.Exp > time.Now().UnixMilli() {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("background cache rebuild did not finish")
			}
			time.Sleep(5 * time.Millisecond)
		}
		execSQL("UPDATE errand SET status='DRAFT',version=version+1 WHERE id=?", id)
		_, e = q.Detail(ctx, runner, id)
		requireCode(t, e, "FORBIDDEN")
		execSQL("UPDATE errand SET status='PUBLISHED',version=version+1 WHERE id=?", id)
		r, e := g.Grab(ctx, runner, uuid.NewString(), GrabCommand{ErrandID: id, ExpectedRound: 0})
		if e != nil || r.State != "SUCCEEDED" {
			t.Fatal(r, e)
		}
		c, e = q.Detail(ctx, runner, id)
		if e != nil || c.Status != "LOCKED" || len(c.AvailableActions) != 1 || c.AvailableActions[0] != "CONFIRM" || c.ConfirmDeadline == nil {
			t.Fatal("cache authorized stale grab", c, e)
		}
	})
	t.Run("concurrent_workers_claim_disjoint_and_inflight_query_is_provisional", func(t *testing.T) {
		recoverAll()
		for range 20 {
			id := newTask()
			tx, e := db.Begin()
			if e != nil {
				t.Fatal(e)
			}
			e = enqueueRepair(ctx, tx, 1, id, fmt.Sprintf("parallel:%d", id), "PROJECTION", nil, 0)
			if e != nil {
				t.Fatal(e)
			}
			if e = tx.Commit(); e != nil {
				t.Fatal(e)
			}
		}
		start := make(chan struct{})
		claimed := make(chan []RepairTask, 2)
		failures := make(chan error, 2)
		for range 2 {
			go func() { <-start; tasks, e := g.ClaimRepairs(ctx, 50); claimed <- tasks; failures <- e }()
		}
		close(start)
		seen := map[int64]bool{}
		for range 2 {
			for _, task := range <-claimed {
				if seen[task.ID] {
					t.Fatal("workers double claimed", task.ID)
				}
				seen[task.ID] = true
				if ok, e := g.CompleteRepair(ctx, task, nil); e != nil || !ok {
					t.Fatal(ok, e)
				}
			}
			if e := <-failures; e != nil {
				t.Fatal(e)
			}
		}
		if len(seen) != 20 {
			t.Fatal("lost worker claims", len(seen))
		}
		id := newTask()
		key := uuid.NewString()
		entered := make(chan struct{})
		release := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			_, e := g.grab(ctx, runner, key, GrabCommand{ErrandID: id, ExpectedRound: 0}, func(phase string) {
				if phase == "before_commit" {
					close(entered)
					<-release
				}
			})
			done <- e
		}()
		<-entered
		r, e := g.Result(ctx, runner, key, id)
		if e != nil || r.State != "PROCESSING" {
			close(release)
			t.Fatal("inflight result fabricated terminal", r, e)
		}
		r, e = g.Grab(ctx, runner, key, GrabCommand{ErrandID: id, ExpectedRound: 0})
		if e != nil || r.State != "PROCESSING" {
			close(release)
			t.Fatal("inflight replay executed again", r, e)
		}
		close(release)
		if e = <-done; e != nil {
			t.Fatal(e)
		}
		if read(key, id).State != "SUCCEEDED" {
			t.Fatal("commit did not converge")
		}
	})
	t.Run("worker_SIGKILL_after_claim_is_taken_over", func(t *testing.T) {
		recoverAll()
		id := newTask()
		tx, e := db.Begin()
		if e != nil {
			t.Fatal(e)
		}
		e = enqueueRepair(ctx, tx, 1, id, fmt.Sprintf("worker-crash:%d", id), "PROJECTION", nil, 0)
		if e != nil {
			t.Fatal(e)
		}
		if e = tx.Commit(); e != nil {
			t.Fatal(e)
		}
		killGrabChild(t, "worker_claimed", id, runner.ID, uuid.NewString(), namespace)
		if count("SELECT COUNT(*) FROM grab_repair_task WHERE errand_id=? AND state='RUNNING' AND retry_count=1", id) != 1 {
			t.Fatal("worker did not durably claim")
		}
		execSQL("UPDATE grab_repair_task SET locked_until=TIMESTAMPADD(SECOND,-1,CURRENT_TIMESTAMP(3)) WHERE errand_id=?", id)
		recoverAll()
		if count("SELECT COUNT(*) FROM grab_repair_task WHERE errand_id=? AND state='DONE' AND retry_count=2", id) != 1 {
			t.Fatal("worker crash not recovered")
		}
	})
	t.Run("SIGKILL_at_each_transaction_boundary", func(t *testing.T) {
		for _, phase := range []string{"registered", "reserved", "before_commit", "committed", "projected"} {
			t.Run(phase, func(t *testing.T) {
				id := newTask()
				key := uuid.NewString()
				killGrabChild(t, phase, id, runner.ID, key, namespace)
				expire(id)
				recoverAll()
				r := read(key, id)
				success := phase == "committed" || phase == "projected"
				want := "EXPIRED"
				records := 0
				if success {
					want = "SUCCEEDED"
					records = 1
				}
				if r.State != want || count("SELECT COUNT(*) FROM grab_record WHERE errand_id=?", id) != records || count("SELECT COUNT(*) FROM local_message WHERE errand_id=?", id) != records {
					t.Fatal("crash recovery mismatch", r, phase)
				}
				state, e := cache.HGetAll(ctx, g.stateKey(id)).Result()
				if e != nil || state["reservationToken"] != "" {
					t.Fatal("orphan pending", state, e)
				}
			})
		}
	})
	recoverAll()
	var walletAfter string
	if err = db.QueryRow("SELECT GROUP_CONCAT(CONCAT(id,':',available,':',frozen,':',version) ORDER BY id) FROM wallet_account").Scan(&walletAfter); err != nil || walletBefore != walletAfter {
		t.Fatal("grab changed D2 wallet balances", err)
	}
}

func killGrabChild(t *testing.T, phase string, id, runner int64, key, namespace string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestGrabCrashProcess$")
	cmd.Env = append(os.Environ(), "CAMPUSGO_D3_CRASH="+phase, "CAMPUSGO_D3_TASK="+fmt.Sprint(id), "CAMPUSGO_D3_RUNNER="+fmt.Sprint(runner), "CAMPUSGO_D3_KEY="+key, "CAMPUSGO_D3_NAMESPACE="+namespace)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ready := make(chan bool, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if strings.Contains(scanner.Text(), "D3_CRASH_READY") {
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
		t.Fatal("child checkpoint timeout")
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err = cmd.Wait(); err == nil {
		t.Fatal("child was not killed")
	}
}
func TestGrabCrashProcess(t *testing.T) {
	phase := os.Getenv("CAMPUSGO_D3_CRASH")
	if phase == "" {
		t.Skip("subprocess only")
	}
	ctx := context.Background()
	db, e := database.Open(ctx, "campusgo_verification")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	cache, e := rediscache.Open()
	if e != nil {
		t.Fatal(e)
	}
	defer cache.Close()
	ids, _ := idgen.New(1018)
	g := &TaskStore{DB: db, Redis: cache, IDs: ids, Namespace: os.Getenv("CAMPUSGO_D3_NAMESPACE")}
	id, _ := strconv.ParseInt(os.Getenv("CAMPUSGO_D3_TASK"), 10, 64)
	runner, _ := strconv.ParseInt(os.Getenv("CAMPUSGO_D3_RUNNER"), 10, 64)
	if phase == "worker_claimed" {
		tasks, e := g.ClaimRepairs(ctx, 100)
		if e != nil {
			t.Fatal(e)
		}
		for _, task := range tasks {
			if task.ErrandID == id {
				fmt.Println("D3_CRASH_READY")
				select {}
			}
		}
		t.Fatal("worker task not found")
	}
	_, e = g.grab(ctx, auth.User{ID: runner, CampusID: 1, Role: "STUDENT"}, os.Getenv("CAMPUSGO_D3_KEY"), GrabCommand{ErrandID: id, ExpectedRound: 0}, func(at string) {
		if at == phase {
			fmt.Println("D3_CRASH_READY")
			select {}
		}
	})
	if e != nil {
		t.Fatal(e)
	}
	t.Fatal("checkpoint never reached")
}
