package errand

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	rmq "github.com/apache/rocketmq-clients/golang/v5"
	"github.com/apache/rocketmq-clients/golang/v5/credentials"
	v2 "github.com/apache/rocketmq-clients/golang/v5/protocol/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/yumestk/CampusGo/internal/auth"
	"github.com/yumestk/CampusGo/internal/platform/database"
	"github.com/yumestk/CampusGo/internal/platform/idgen"
	"github.com/yumestk/CampusGo/internal/platform/rediscache"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestIntegrationD4(t *testing.T) {
	if os.Getenv("CAMPUSGO_D4_INTEGRATION") != "1" {
		t.Skip("run make verify-d4")
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
	ids, _ := idgen.New(1018)
	namespace := "d4-" + uuid.NewString()
	g := &Grabber{DB: db, Redis: cache, IDs: ids, Namespace: namespace}
	manifest := func(kind string, id int64) {
		t.Helper()
		f, e := os.OpenFile(filepath.Join("..", "..", "work", "d4-fixtures.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
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
		execSQL("INSERT INTO app_user (id,campus_id,display_name) VALUES (?,?,'D4验证用户')", id, campus)
		owned = append(owned, id)
		return auth.User{ID: id, CampusID: campus, Role: "STUDENT"}
	}
	publisher := newUser(1)
	runner := newUser(1)
	newTask := func() int64 {
		t.Helper()
		id := newID()
		manifest("errand", id)
		execSQL("INSERT INTO errand (id,campus_id,publisher_id,type,title,reward_amount,slot_total,status,version) VALUES (?,1,?,'DELIVERY','D4验证任务',100,1,'PUBLISHED',1)", id, publisher.ID)
		tasks = append(tasks, id)
		return id
	}
	t.Cleanup(func() {
		// Only IDs created by this fixture are removed. No shared database/Redis flush.
		for _, id := range tasks {
			for _, q := range []string{"DELETE FROM credit_event WHERE errand_id=?", "DELETE FROM grab_repair_task WHERE errand_id=?", "DELETE FROM local_message WHERE errand_id=?", "DELETE FROM grab_record WHERE errand_id=?", "DELETE FROM grab_request WHERE errand_id=?", "DELETE FROM errand_status_log WHERE errand_id=?", "DELETE FROM errand WHERE id=?"} {
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
	locked := func(u auth.User) int64 {
		t.Helper()
		id := newTask()
		r, e := g.Grab(ctx, u, uuid.NewString(), GrabCommand{id, 0})
		if e != nil || r.State != "SUCCEEDED" {
			t.Fatal(r, e)
		}
		return id
	}
	due := func(id int64) {
		t.Helper()
		execSQL("UPDATE errand SET confirm_deadline=TIMESTAMPADD(SECOND,-1,CURRENT_TIMESTAMP(3)) WHERE id=?", id)
	}
	assertStatus := func(id int64, status string, round int, runnerID int64) {
		t.Helper()
		c, e := scan(db.QueryRow("SELECT "+columns+" FROM errand WHERE id=?", id))
		if e != nil {
			t.Fatal(e)
		}
		if c.Status != status || c.Round != round || (runnerID > 0 && (c.GrabberID == nil || *c.GrabberID != runnerID)) || (runnerID == 0 && c.GrabberID != nil) {
			t.Fatalf("unexpected card: %+v", c)
		}
	}
	offer := func(id int64, u auth.User, round int, score float64) Candidate {
		t.Helper()
		token := uuid.NewString()
		if e := offerCandidate.Run(ctx, cache, g.candidateKeys(id), fmt.Sprint(u.ID), score, round, token).Err(); e != nil {
			t.Fatal(e)
		}
		return Candidate{u.ID, round, token}
	}
	t.Run("confirmation_authority_round_deadline_and_replay", func(t *testing.T) {
		id := locked(runner)
		if _, e := g.Confirm(ctx, publisher, id, 0); e == nil {
			t.Fatal("publisher confirmed")
		}
		if _, e := g.Confirm(ctx, runner, id, 1); e == nil {
			t.Fatal("old round accepted")
		}
		other := runner
		other.CampusID = 2
		if _, e := g.Confirm(ctx, other, id, 0); e == nil {
			t.Fatal("cross campus confirmed")
		}
		c, e := g.Confirm(ctx, runner, id, 0)
		if e != nil || c.Status != "ACCEPTED" || c.ConfirmDeadline != nil {
			t.Fatal(c, e)
		}
		if _, e = g.Confirm(ctx, runner, id, 0); e != nil {
			t.Fatal(e)
		}
		if count("SELECT COUNT(*) FROM errand_status_log WHERE errand_id=? AND to_status='ACCEPTED'", id) != 1 {
			t.Fatal("duplicate effect")
		}
		late := locked(runner)
		due(late)
		if _, e = g.Confirm(ctx, runner, late, 0); e == nil {
			t.Fatal("expired confirmed")
		}
	})
	t.Run("cross_task_concurrent_quota_never_exceeds_five", func(t *testing.T) {
		u := newUser(1)
		ids := []int64{}
		for range 12 {
			ids = append(ids, locked(u))
		}
		var wg sync.WaitGroup
		for _, id := range ids {
			wg.Add(1)
			go func() { defer wg.Done(); _, _ = g.Confirm(ctx, u, id, 0) }()
		}
		wg.Wait()
		if n := count("SELECT COUNT(*) FROM errand WHERE grabber_id=? AND status IN ('ACCEPTED','PICKED_UP')", u.ID); n != 5 {
			t.Fatalf("ongoing=%d", n)
		}
	})

	t.Run("promotion_filters_identity_credit_quota_and_registration_is_best_effort", func(t *testing.T) {
		owner := newUser(1)
		id := locked(owner)
		otherCampus, inactive, low, full, next := newUser(2), newUser(1), newUser(1), newUser(1), newUser(1)
		execSQL("UPDATE app_user SET active=0 WHERE id=?", inactive.ID)
		execSQL("INSERT INTO credit_score(user_id,score) VALUES (?,39)", low.ID)
		for range 5 {
			task := newTask()
			execSQL("UPDATE errand SET status='ACCEPTED',grabber_id=?,slot_taken=1 WHERE id=?", full.ID, task)
		}
		for i, u := range []auth.User{publisher, owner, otherCampus, inactive, low, full, next} {
			offer(id, u, 0, float64(i))
		}
		due(id)
		out, e := g.HandleTimeout(ctx, id, 0)
		if e != nil || out != "TRANSFERRED" {
			t.Fatal(out, e)
		}
		assertStatus(id, "LOCKED", 1, next.ID)
		fresh := locked(newUser(1))
		candidate := newUser(1)
		r, e := g.Grab(ctx, candidate, uuid.NewString(), GrabCommand{fresh, 0})
		if e != nil || r.State != "REJECTED" {
			t.Fatal(r, e)
		}
		best, e := g.peekCandidate(ctx, fresh)
		if e != nil || best == nil || best.RunnerID != candidate.ID {
			t.Fatal("failed grab did not offer", best, e)
		}
		broken := *g
		broken.Redis = redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: time.Millisecond, MaxRetries: -1})
		defer broken.Redis.Close()
		r, e = broken.Grab(ctx, newUser(1), uuid.NewString(), GrabCommand{fresh, 0})
		if e != nil || r.State != "REJECTED" {
			t.Fatal("candidate outage changed durable rejection", r, e)
		}
	})
	t.Run("before_deadline_confirmation_wins_against_early_timeout", func(t *testing.T) {
		u := newUser(1)
		id := locked(u)
		offer(id, newUser(1), 0, 1)
		var wg sync.WaitGroup
		wg.Add(2)
		var confirmErr, timeoutErr error
		go func() { defer wg.Done(); _, confirmErr = g.Confirm(ctx, u, id, 0) }()
		go func() { defer wg.Done(); _, timeoutErr = g.HandleTimeout(ctx, id, 0) }()
		wg.Wait()
		if confirmErr != nil || timeoutErr != nil {
			t.Fatal(confirmErr, timeoutErr)
		}
		assertStatus(id, "ACCEPTED", 0, u.ID)
		if cache.ZCard(ctx, g.candidateKeys(id)[0]).Val() != 1 {
			t.Fatal("early timeout consumed candidate")
		}
	})
	t.Run("weighted_queue_same_round_and_exact_token_removal", func(t *testing.T) {
		id := locked(runner)
		a, b := newUser(1), newUser(1)
		old := offer(id, a, 0, 20)
		offer(id, b, 0, 10)
		if e := g.offerCandidate(ctx, grabRequest{RunnerID: b.ID, ErrandID: id, CampusID: 1, Round: 0}); e != nil {
			t.Fatal(e)
		}
		best, e := g.peekCandidate(ctx, id)
		if e != nil || best.RunnerID != b.ID {
			t.Fatal(best, e)
		}
		newer := offer(id, a, 1, 1)
		_ = g.removeCandidate(ctx, id, old)
		best, e = g.peekCandidate(ctx, id)
		if e != nil || *best != newer {
			t.Fatal("old token deleted new entry", best, e)
		}
	})
	t.Run("early_duplicate_old_round_and_transfer_atomicity", func(t *testing.T) {
		id := locked(runner)
		next := newUser(1)
		offer(id, next, 0, 1)
		outcome, e := g.HandleTimeout(ctx, id, 0)
		if e != nil || outcome != "SKIPPED" {
			t.Fatal(outcome, e)
		}
		if n := cache.ZCard(ctx, g.candidateKeys(id)[0]).Val(); n != 1 {
			t.Fatal("early consumed candidate")
		}
		due(id)
		outcome, e = g.HandleTimeout(ctx, id, 0)
		if e != nil || outcome != "TRANSFERRED" {
			t.Fatal(outcome, e)
		}
		assertStatus(id, "LOCKED", 1, next.ID)
		if count("SELECT COUNT(*) FROM local_message WHERE errand_id=?", id) != 2 || count("SELECT COUNT(*) FROM grab_record WHERE errand_id=?", id) != 2 {
			t.Fatal("missing next-round protection")
		}
		if _, e = g.HandleTimeout(ctx, id, 0); e != nil {
			t.Fatal(e)
		}
		if _, e = g.Confirm(ctx, runner, id, 0); e == nil {
			t.Fatal("old owner confirmed")
		}
		if _, e = g.Confirm(ctx, next, id, 1); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("confirm_vs_timeout_has_one_winner", func(t *testing.T) {
		for range 12 {
			id := locked(newUser(1))
			var u auth.User
			u.CampusID = 1
			u.Role = "STUDENT"
			if e := db.QueryRow("SELECT grabber_id FROM errand WHERE id=?", id).Scan(&u.ID); e != nil {
				t.Fatal(e)
			}
			// Expired before both start: confirmation must lose, including after row waits.
			due(id)
			next := newUser(1)
			offer(id, next, 0, 1)
			var wg sync.WaitGroup
			wg.Add(2)
			go func() { defer wg.Done(); _, _ = g.Confirm(ctx, u, id, 0) }()
			go func() { defer wg.Done(); _, _ = g.HandleTimeout(ctx, id, 0) }()
			wg.Wait()
			assertStatus(id, "LOCKED", 1, next.ID)
		}
	})
	t.Run("candidate_eligibility_rechecked_and_budget_not_empty", func(t *testing.T) {
		id := locked(runner)
		for i := 0; i < 33; i++ {
			u := newUser(1)
			execSQL("INSERT INTO credit_score(user_id,score) VALUES (?,10)", u.ID)
			offer(id, u, 0, float64(i))
		}
		next := newUser(1)
		offer(id, next, 0, 100)
		due(id)
		out, e := g.HandleTimeout(ctx, id, 0)
		if e != nil || out != "SKIPPED" {
			t.Fatal(out, e)
		}
		assertStatus(id, "LOCKED", 0, runner.ID)
		out, e = g.HandleTimeout(ctx, id, 0)
		if e != nil || out != "TRANSFERRED" {
			t.Fatal(out, e)
		}
		assertStatus(id, "LOCKED", 1, next.ID)
	})
	t.Run("repools_credit_once_new_cycle_and_old_candidates_filtered", func(t *testing.T) {
		u := newUser(1)
		id := locked(u)
		due(id)
		for range 3 {
			if _, e := g.HandleTimeout(ctx, id, 0); e != nil {
				t.Fatal(e)
			}
		}
		assertStatus(id, "PUBLISHED", 1, 0)
		if count("SELECT score FROM credit_score WHERE user_id=?", u.ID) != 55 || count("SELECT COUNT(*) FROM credit_event WHERE errand_id=?", id) != 1 {
			t.Fatal("credit effect incorrect")
		}
		old, next := newUser(1), newUser(1)
		offer(id, old, 0, 0)
		r, e := g.Grab(ctx, u, uuid.NewString(), GrabCommand{id, 1})
		if e != nil || r.State != "SUCCEEDED" {
			t.Fatal(r, e)
		}
		offer(id, next, 1, 1)
		due(id)
		if _, e = g.HandleTimeout(ctx, id, 1); e != nil {
			t.Fatal(e)
		}
		assertStatus(id, "LOCKED", 2, next.ID)
	})
	t.Run("five_transfers_per_cycle_then_repool", func(t *testing.T) {
		id := locked(newUser(1))
		for range 7 {
			offer(id, newUser(1), 0, float64(time.Now().UnixNano()))
		}
		for round := 0; round < 5; round++ {
			due(id)
			out, e := g.HandleTimeout(ctx, id, round)
			if e != nil || out != "TRANSFERRED" {
				t.Fatal(round, out, e)
			}
		}
		due(id)
		out, e := g.HandleTimeout(ctx, id, 5)
		if e != nil || out != "REVERTED" {
			t.Fatal(out, e)
		}
		assertStatus(id, "PUBLISHED", 6, 0)
		u := newUser(1)
		r, e := g.Grab(ctx, u, uuid.NewString(), GrabCommand{id, 6})
		if e != nil || r.State != "SUCCEEDED" {
			t.Fatal(r, e)
		}
		offer(id, newUser(1), 6, 0)
		due(id)
		out, e = g.HandleTimeout(ctx, id, 6)
		if e != nil || out != "TRANSFERRED" {
			t.Fatal("cycle budget not reset", out, e)
		}
	})
	t.Run("redis_down_repools_and_sql_scan_recovers_without_mq", func(t *testing.T) {
		id := locked(newUser(1))
		due(id)
		broken := *g
		broken.Redis = redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: time.Millisecond, MaxRetries: -1})
		defer broken.Redis.Close()
		if e := broken.ScanDue(ctx, 100); e != nil {
			t.Fatal(e)
		}
		assertStatus(id, "PUBLISHED", 1, 0)
		if e := g.RecoverBatch(ctx, 100); e != nil {
			t.Fatal(e)
		}
		if e := g.syncProjection(ctx, id); e != nil {
			t.Fatal(e)
		}
		if cache.HGet(ctx, g.stateKey(id), "status").Val() != "PUBLISHED" {
			t.Fatal("projection not recovered")
		}
	})
	t.Run("commit_ack_loss_recovered_without_double_transfer", func(t *testing.T) {
		id := locked(newUser(1))
		next := newUser(1)
		offer(id, next, 0, 1)
		due(id)
		addr, dropped := commitAckProxy(t)
		proxyDB := databaseViaProxy(t, addr)
		proxied := *g
		proxied.DB = proxyDB
		if _, e := proxied.HandleTimeout(ctx, id, 0); e == nil {
			t.Fatal("lost ACK not surfaced")
		}
		select {
		case <-dropped:
		case <-time.After(time.Second):
			t.Fatal("ACK not dropped")
		}
		assertStatus(id, "LOCKED", 1, next.ID)
		if _, e := g.HandleTimeout(ctx, id, 0); e != nil {
			t.Fatal(e)
		}
		if count("SELECT COUNT(*) FROM grab_record WHERE errand_id=?", id) != 2 {
			t.Fatal("duplicated transition")
		}
		// The candidate remains on ambiguous commit; new-round selection skips current owner.
		if cache.ZCard(ctx, g.candidateKeys(id)[0]).Val() != 1 {
			t.Fatal("candidate consumed before known commit")
		}
	})
	t.Run("confirm_commit_ack_loss_is_queryable_and_replayable", func(t *testing.T) {
		u := newUser(1)
		id := locked(u)
		addr, _ := commitAckProxy(t)
		proxied := *g
		proxied.DB = databaseViaProxy(t, addr)
		if _, e := proxied.Confirm(ctx, u, id, 0); e == nil {
			t.Fatal("expected unknown")
		}
		assertStatus(id, "ACCEPTED", 0, u.ID)
		if _, e := g.Confirm(ctx, u, id, 0); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("message_claims_fencing_dead_replay_and_lost_send_ack", func(t *testing.T) {
		// Isolate existing fixture messages so the two workers claim only this batch.
		execSQL("UPDATE local_message SET state='DONE' WHERE errand_id IN (SELECT id FROM errand WHERE publisher_id=?)", publisher.ID)
		for range 20 {
			locked(newUser(1))
		}
		var left, right []TimeoutMessage
		var e1, e2 error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); left, e1 = g.ClaimMessages(ctx, 10) }()
		go func() { defer wg.Done(); right, e2 = g.ClaimMessages(ctx, 10) }()
		wg.Wait()
		// SKIP LOCKED can return a partial batch while another claimant changes
		// the secondary index; completeness is eventual, ownership is disjoint.
		rest, restErr := g.ClaimMessages(ctx, 100)
		right = append(right, rest...)
		if e1 != nil || e2 != nil || restErr != nil || len(left)+len(right) != 20 {
			t.Fatal(len(left), len(right), e1, e2)
		}
		seen := map[int64]bool{}
		for _, m := range append(left, right...) {
			if seen[m.ID] {
				t.Fatal("duplicate claim")
			}
			seen[m.ID] = true
		}
		old := append(left, right...)[0]
		execSQL("UPDATE local_message SET locked_until=TIMESTAMPADD(SECOND,-1,CURRENT_TIMESTAMP(3)) WHERE id=?", old.ID)
		newer, e := g.ClaimMessages(ctx, 1)
		if e != nil || len(newer) != 1 {
			t.Fatal(newer, e)
		}
		if ok, e := g.CompleteMessage(ctx, old, nil); e != nil || ok {
			t.Fatal("stale completion", ok, e)
		}
		current := newer[0]
		execSQL("UPDATE local_message SET retry_count=10 WHERE id=?", current.ID)
		if ok, e := g.CompleteMessage(ctx, current, fmt.Errorf("send ACK lost")); e != nil || !ok {
			t.Fatal(ok, e)
		}
		dead, e := g.DeadMessages(ctx)
		if e != nil || len(dead) != 1 {
			t.Fatal(dead, e)
		}
		if _, e = g.ReplayMessage(ctx, current.ID, ""); e == nil {
			t.Fatal("missing reason")
		}
		if ok, e := g.ReplayMessage(ctx, current.ID, "dependency restored"); e != nil || !ok {
			t.Fatal(ok, e)
		}
		if count("SELECT replay_count FROM local_message WHERE id=?", current.ID) != 1 {
			t.Fatal("audit missing")
		}
	})
	t.Run("strict_message_validation_and_early_old_payload", func(t *testing.T) {
		id := locked(newUser(1))
		var body []byte
		if e := db.QueryRow("SELECT payload FROM local_message WHERE errand_id=?", id).Scan(&body); e != nil {
			t.Fatal(e)
		}
		if out, e := g.ConsumeTimeout(ctx, body); e != nil || out != "SKIPPED" {
			t.Fatal(out, e)
		}
		for _, bad := range []string{`{}`, `{"errandId":123}`, string(body) + " {}"} {
			if out, e := g.ConsumeTimeout(ctx, []byte(bad)); e != nil || out != "INVALID" {
				t.Fatal(out, e)
			}
		}
		due(id)
		if _, e := g.HandleTimeout(ctx, id, 0); e != nil {
			t.Fatal(e)
		}
		if out, e := g.ConsumeTimeout(ctx, body); e != nil || out != "SKIPPED" {
			t.Fatal(out, e)
		}
	})
	t.Run("business_failure_rolls_back_transfer_and_preserves_candidate", func(t *testing.T) {
		id := locked(newUser(1))
		next := newUser(1)
		offer(id, next, 0, 1)
		due(id)
		execSQL(fmt.Sprintf("ALTER TABLE local_message ADD CONSTRAINT ck_d4_rollback CHECK (errand_id<>%d OR JSON_EXTRACT(payload,'$.round')=0)", id))
		defer execSQL("ALTER TABLE local_message DROP CHECK ck_d4_rollback")
		if _, e := g.HandleTimeout(ctx, id, 0); e == nil {
			t.Fatal("injected SQL constraint did not fail")
		}
		if count("SELECT COUNT(*) FROM grab_record WHERE errand_id=?", id) != 1 || count("SELECT COUNT(*) FROM local_message WHERE errand_id=?", id) != 1 || cache.ZCard(ctx, g.candidateKeys(id)[0]).Val() != 1 {
			t.Fatal("partial transfer or lost candidate")
		}
		assertStatus(id, "LOCKED", 0, countRunner(t, g, id))
	})
	t.Run("real_mq_delay_duplicate_and_send_receipt_ambiguity", func(t *testing.T) {
		if os.Getenv("CAMPUSGO_D4_MQ") != "1" {
			t.Skip("enable real MQ via verify-d4")
		}
		execSQL("UPDATE local_message SET state='DONE' WHERE errand_id IN (SELECT id FROM errand WHERE publisher_id=?)", publisher.ID)
		id := locked(newUser(1))
		execSQL("UPDATE errand SET confirm_deadline=TIMESTAMPADD(SECOND,2,CURRENT_TIMESTAMP(3)) WHERE id=?", id)
		execSQL(`UPDATE local_message m JOIN errand e ON e.id=m.errand_id SET m.deliver_at=e.confirm_deadline,m.payload=JSON_SET(m.payload,'$.deadline',DATE_FORMAT(e.confirm_deadline,'%Y-%m-%dT%H:%i:%s.%fZ')) WHERE e.id=?`, id)
		producer, e := rmq.NewProducer(&rmq.Config{Endpoint: os.Getenv("CAMPUSGO_MQ_ENDPOINT"), Credentials: &credentials.SessionCredentials{}}, rmq.WithTopics("campusgo-confirm-timeout"))
		if e != nil {
			t.Fatal(e)
		}
		if e = producer.Start(); e != nil {
			t.Fatal(e)
		}
		defer producer.GracefulStop()
		consumer, e := rmq.NewSimpleConsumer(&rmq.Config{Endpoint: os.Getenv("CAMPUSGO_MQ_ENDPOINT"), ConsumerGroup: "campusgo-timeout-campusgo_verification", Credentials: &credentials.SessionCredentials{}}, rmq.WithSimpleAwaitDuration(6*time.Second), rmq.WithSimpleSubscriptionExpressions(map[string]*rmq.FilterExpression{"campusgo-confirm-timeout": rmq.NewFilterExpression(namespace)}))
		if e != nil {
			t.Fatal(e)
		}
		if e = consumer.Start(); e != nil {
			t.Fatal(e)
		}
		defer consumer.GracefulStop()
		send := func(c context.Context, m TimeoutMessage) error {
			msg := &rmq.Message{Topic: "campusgo-confirm-timeout", Body: m.Payload}
			msg.SetKeys(m.Key)
			msg.SetTag(namespace)
			at := m.DeliverAt
			if at.Before(time.Now().Add(time.Second)) {
				at = time.Now().Add(time.Second)
			}
			msg.SetDelayTimestamp(at)
			_, e := producer.Send(c, msg)
			return e
		}
		if e = g.DispatchMessages(ctx, func(c context.Context, m TimeoutMessage) error {
			if e := send(c, m); e != nil {
				return e
			}
			return fmt.Errorf("receipt lost after real broker accepted")
		}); e != nil {
			t.Fatal(e)
		}
		if count("SELECT COUNT(*) FROM local_message WHERE errand_id=? AND state='PENDING' AND retry_count=1", id) != 1 {
			t.Fatal("lost receipt not retryable")
		}
		execSQL("UPDATE local_message SET next_run_at=CURRENT_TIMESTAMP(3) WHERE errand_id=?", id)
		if e = g.DispatchMessages(ctx, send); e != nil {
			t.Fatal(e)
		}
		if count("SELECT COUNT(*) FROM local_message WHERE errand_id=? AND state='DONE' AND retry_count=2", id) != 1 {
			t.Fatal("resend not done")
		}
		// Eight broker queues may each spend one long-poll interval empty.
		deadline := time.Now().Add(90 * time.Second)
		seen := 0
		for seen < 2 && time.Now().Before(deadline) {
			batch, cancel := context.WithTimeout(ctx, 12*time.Second)
			messages, e := consumer.Receive(batch, 8, 10*time.Second)
			cancel()
			if e != nil {
				var status *rmq.ErrRpcStatus
				if errors.As(e, &status) && status.Code == int32(v2.Code_MESSAGE_NOT_FOUND) {
					continue
				}
				t.Fatal(e)
			}
			for _, message := range messages {
				out, e := g.ConsumeTimeout(ctx, message.GetBody())
				if e != nil {
					t.Fatal(e)
				}
				if out != "REVERTED" && out != "SKIPPED" {
					t.Fatal(out)
				}
				if e = consumer.Ack(ctx, message); e != nil {
					t.Fatal(e)
				}
				seen++
			}
		}
		if seen != 2 {
			t.Fatalf("received %d/2 broker messages", seen)
		}
		assertStatus(id, "PUBLISHED", 1, 0)
		if count("SELECT COUNT(*) FROM credit_event WHERE errand_id=?", id) != 1 {
			t.Fatal("duplicate credit")
		}
	})
	t.Run("SIGKILL_before_and_after_timeout_commit_and_message_claim", func(t *testing.T) {
		for _, stage := range []string{"before_commit", "committed", "message_claimed"} {
			id := locked(newUser(1))
			next := newUser(1)
			offer(id, next, 0, 1)
			due(id)
			if stage == "message_claimed" {
				execSQL("UPDATE local_message SET state='DONE' WHERE errand_id<>?", id)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestD4CrashHelper$", "-test.v")
			cmd.Env = append(os.Environ(), "CAMPUSGO_D4_CRASH="+stage, "CAMPUSGO_D4_TASK="+fmt.Sprint(id), "CAMPUSGO_D4_NAMESPACE="+namespace)
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
				scanner := bufio.NewScanner(stdout)
				for scanner.Scan() {
					if scanner.Text() == "D4_READY" {
						ready <- true
						return
					}
				}
				ready <- false
			}()
			select {
			case ok := <-ready:
				if !ok {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
					t.Fatal("helper exited")
				}
			case <-time.After(20 * time.Second):
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				t.Fatal("helper timed out")
			}
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			if stage == "message_claimed" {
				execSQL("UPDATE local_message SET locked_until=TIMESTAMPADD(SECOND,-1,CURRENT_TIMESTAMP(3)) WHERE errand_id=?", id)
				messages, e := g.ClaimMessages(ctx, 1)
				if e != nil || len(messages) != 1 || messages[0].Attempts != 2 {
					t.Fatal(messages, e)
				}
			} else {
				if stage == "before_commit" {
					assertStatus(id, "LOCKED", 0, countRunner(t, g, id))
					if _, e = g.HandleTimeout(ctx, id, 0); e != nil {
						t.Fatal(e)
					}
				}
				assertStatus(id, "LOCKED", 1, next.ID)
			}
		}
	})
}
func countRunner(t *testing.T, g *Grabber, id int64) int64 {
	t.Helper()
	var r int64
	if e := g.DB.QueryRow("SELECT grabber_id FROM errand WHERE id=?", id).Scan(&r); e != nil {
		t.Fatal(e)
	}
	return r
}
func TestD4CrashHelper(t *testing.T) {
	stage := os.Getenv("CAMPUSGO_D4_CRASH")
	if stage == "" {
		t.Skip("subprocess only")
	}
	ctx := context.Background()
	db, e := database.Open(ctx, "campusgo_verification")
	if e != nil {
		t.Fatal(e)
	}
	cache, e := rediscache.Open()
	if e != nil {
		t.Fatal(e)
	}
	ids, _ := idgen.New(1017)
	g := &Grabber{DB: db, Redis: cache, IDs: ids, Namespace: os.Getenv("CAMPUSGO_D4_NAMESPACE")}
	id, _ := strconv.ParseInt(os.Getenv("CAMPUSGO_D4_TASK"), 10, 64)
	ready := func() { fmt.Println("D4_READY"); select {} }
	if stage == "message_claimed" {
		messages, e := g.ClaimMessages(ctx, 1)
		if e != nil || len(messages) != 1 {
			t.Fatal(messages, e)
		}
		ready()
	}
	_, e = g.handleTimeout(ctx, id, 0, func(point string) {
		if point == stage {
			ready()
		}
	})
	t.Fatal("unreached kill point", e)
}
