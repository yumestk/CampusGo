package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	mysqlstore "github.com/yumestk/CampusGo/internal/adapter/mysql"
	"github.com/yumestk/CampusGo/internal/auth"
	"github.com/yumestk/CampusGo/internal/platform/database"
	"github.com/yumestk/CampusGo/internal/platform/idgen"
	"github.com/yumestk/CampusGo/internal/platform/rediscache"
	"github.com/yumestk/CampusGo/internal/platform/telemetry"
)

// Explicit expected behavior, cross-checked against the Java resolver/domain.
// Renamed PICKUP, draft cancellation and arbitration directions are intentional
// API adaptations; the user permissions and business outcomes stay equivalent.
func TestIntegrationD8Business(t *testing.T) {
	if os.Getenv("CAMPUSGO_D8_BUSINESS") != "1" {
		t.Skip("run make verify-d8-business")
	}
	ctx := context.Background()
	db, e := database.Open(ctx, "campusgo_verification")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	r, e := rediscache.Open()
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { r.Close() })
	ids, _ := idgen.New(1022)
	namespace := "d8-" + uuid.NewString()
	g := &mysqlstore.TaskStore{DB: db, Redis: r, IDs: ids, Namespace: namespace}
	exec := func(t *testing.T, q string, args ...any) {
		t.Helper()
		if _, e := db.ExecContext(ctx, q, args...); e != nil {
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
	taskIDs := []int64{}
	refs := []int64{}
	rankUsers := []int64{newID(), newID(), newID()}
	t.Cleanup(func() {
		for _, id := range refs {
			if _, e := db.Exec("DELETE FROM credit_event WHERE errand_id=?", id); e != nil {
				t.Error(e)
			}
		}
		for _, id := range taskIDs {
			if _, e := db.Exec("DELETE FROM grab_repair_task WHERE errand_id=?", id); e != nil {
				t.Error(e)
			}
			if _, e := db.Exec("DELETE FROM grab_request WHERE errand_id=?", id); e != nil {
				t.Error(e)
			}
			if _, e := db.Exec("DELETE FROM errand WHERE id=? AND title='D8只读业务矩阵'", id); e != nil {
				t.Error(e)
			}
		}
		for _, id := range rankUsers {
			db.Exec("DELETE FROM credit_score WHERE user_id=?", id)
			if _, e := db.Exec("DELETE FROM app_user WHERE id=? AND display_name='D8榜单验证'", id); e != nil {
				t.Error(e)
			}
		}
		r.Del(ctx, "campusgo:"+namespace+":credit:rank:1", "campusgo:"+namespace+":credit:rank:2")
	})
	for i, id := range rankUsers {
		campus := 1
		if i == 2 {
			campus = 2
		}
		exec(t, "INSERT INTO app_user(id,campus_id,display_name) VALUES(?,?,'D8榜单验证')", id, campus)
		exec(t, "INSERT INTO credit_score(user_id,score) VALUES(?,100)", id)
	}
	for i := range 55 {
		id := newID()
		refs = append(refs, id)
		exec(t, "INSERT INTO credit_event(user_id,errand_id,round,kind,delta,created_at) VALUES(1001,?,0,'SETTLE',2,TIMESTAMPADD(SECOND,?,CURRENT_TIMESTAMP(3)))", id, i-100)
	}
	oldRef := newID()
	refs = append(refs, oldRef)
	exec(t, "INSERT INTO credit_event(user_id,errand_id,round,kind,delta,created_at) VALUES(1001,?,0,'DISPUTE_LOSE',-8,TIMESTAMPADD(DAY,-31,CURRENT_TIMESTAMP(3)))", oldRef)
	otherRef := newID()
	refs = append(refs, otherRef)
	exec(t, "INSERT INTO credit_event(user_id,errand_id,round,kind,delta) VALUES(2002,?,0,'DISPUTE_LOSE',-8)", otherRef)
	type expectation struct {
		status                     string
		owner, runner, viewer, arb []string
	}
	matrix := []expectation{
		{"DRAFT", []string{"CANCEL_DRAFT"}, nil, nil, nil},
		{"PUBLISHED", []string{"CANCEL"}, []string{"GRAB"}, []string{"GRAB"}, []string{"GRAB"}},
		{"LOCKED", nil, []string{"CONFIRM"}, []string{"GRAB"}, []string{"GRAB"}},
		{"ACCEPTED", []string{"DISPUTE"}, []string{"PICK_UP", "DISPUTE"}, nil, nil},
		{"PICKED_UP", []string{"DISPUTE"}, []string{"DELIVER", "DISPUTE"}, nil, nil},
		{"DELIVERED", []string{"DISPUTE", "SETTLE"}, []string{"DISPUTE"}, nil, nil},
		{"DISPUTED", nil, nil, nil, []string{"ARBITRATE_RUNNER", "ARBITRATE_PUBLISHER"}},
		{"SETTLED", nil, nil, nil, nil}, {"CANCELLED", nil, nil, nil, nil}, {"REFUNDED", nil, nil, nil, nil}, {"CLOSED", nil, nil, nil, nil},
	}
	for _, mode := range []string{"session", "jwt"} {
		t.Run(mode, func(t *testing.T) {
			s := &auth.Service{DB: db, Redis: r, Config: auth.Config{Environment: "test", Namespace: namespace + mode, Demo: true, Mode: mode, SessionTTL: time.Hour, JWTSecret: "d8-business-signing-secret-at-least-32-bytes", RefreshTTL: time.Hour}}
			router := NewRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), telemetry.NewRegistry("api"))
			registerTestBusiness(router, s, mysqlstore.Query{DB: db}, &mysqlstore.PublishStore{DB: db, IDs: ids}, g)
			tokens := map[int64]string{}
			for _, id := range []int64{1001, 2001, 2002, 9001} {
				session, e := s.DemoLogin(ctx, id)
				if e != nil {
					t.Fatal(e)
				}
				tokens[id] = session.Token
				defer s.Logout(ctx, session.Token)
			}
			get := func(path, token string) *httptest.ResponseRecorder {
				t.Helper()
				req := httptest.NewRequest("GET", path, nil)
				req.Header.Set("X-User-Id", "9001")
				if token != "" {
					req.Header.Set("Authorization", "Bearer "+token)
				}
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
				return w
			}
			for _, tc := range matrix {
				t.Run(tc.status, func(t *testing.T) {
					id := newID()
					taskIDs = append(taskIDs, id)
					exec(t, "INSERT INTO errand(id,campus_id,publisher_id,grabber_id,type,title,reward_amount,status) VALUES(?,1,1001,2001,'DELIVERY','D8只读业务矩阵',100,?)", id, tc.status)
					for _, actor := range []struct {
						id      int64
						actions []string
					}{{1001, tc.owner}, {2001, tc.runner}, {2002, tc.viewer}, {9001, tc.arb}} {
						w := get(fmt.Sprintf("/api/errands/%d", id), tokens[actor.id])
						want := 200
						if tc.status == "DRAFT" && actor.id != 1001 && actor.id != 9001 {
							want = 403
						}
						if w.Code != want {
							t.Fatalf("actor=%d status=%d body=%s", actor.id, w.Code, w.Body.String())
						}
						if want != 200 {
							continue
						}
						var body struct {
							Data mysqlstore.Card `json:"data"`
						}
						if e := json.Unmarshal(w.Body.Bytes(), &body); e != nil {
							t.Fatal(e)
						}
						actual := body.Data.AvailableActions
						slices.Sort(actual)
						expected := append([]string{}, actor.actions...)
						slices.Sort(expected)
						if !reflect.DeepEqual(actual, expected) {
							t.Fatalf("actor=%d actions=%v want=%v", actor.id, actual, expected)
						}
					}
				})
			}

			t.Run("query_cursor_offset_role_and_campus_contract", func(t *testing.T) {
				pageOf := func(path string) mysqlstore.Page {
					t.Helper()
					w := get(path, tokens[1001])
					var body struct {
						Data json.RawMessage `json:"data"`
					}
					if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
						t.Fatal(w.Code, w.Body.String())
					}
					var page mysqlstore.Page
					var e error
					if len(body.Data) > 0 && body.Data[0] == '[' {
						e = json.Unmarshal(body.Data, &page.Items)
					} else {
						e = json.Unmarshal(body.Data, &page)
					}
					if e != nil {
						t.Fatal(e, w.Body.String())
					}
					return page
				}
				first := pageOf("/api/errands/mine?role=PUBLISHED_BY_ME&size=2&cursor=")
				if len(first.Items) != 2 || first.NextCursor == "" {
					t.Fatal("missing cursor", first)
				}
				next := pageOf("/api/errands/mine?role=PUBLISHED_BY_ME&size=2&cursor=" + first.NextCursor)
				offset := pageOf("/api/errands/mine?role=PUBLISHED_BY_ME&size=2&page=1")
				if !reflect.DeepEqual(next.Items, offset.Items) || next.Items[0].ID == first.Items[0].ID {
					t.Fatal("cursor and offset diverged")
				}
				for _, card := range append(first.Items, next.Items...) {
					w := get(fmt.Sprintf("/api/errands/%d", card.ID), tokens[1001])
					var body struct {
						Data mysqlstore.Card `json:"data"`
					}
					if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || !reflect.DeepEqual(card.AvailableActions, body.Data.AvailableActions) {
						t.Fatal("list/detail actions drifted")
					}
				}
				for _, tc := range []struct {
					path   string
					status int
				}{{"/api/errands?campusId=2", 403}, {"/api/errands?status=bogus", 400}, {"/api/errands?size=101", 400}, {"/api/errands?cursor=bogus", 400}, {"/api/errands/mine?role=ARBITRATOR", 400}} {
					if w := get(tc.path, tokens[1001]); w.Code != tc.status {
						t.Fatal(tc.path, w.Code, w.Body.String())
					}
				}
			})
			t.Run("production_admission_precedes_request_registration", func(t *testing.T) {
				g.Limits = mysqlstore.NewGrabLimits(r, namespace+mode)
				defer func() { g.Limits = nil }()
				id := newID()
				taskIDs = append(taskIDs, id)
				exec(t, "INSERT INTO errand(id,campus_id,publisher_id,type,title,reward_amount,status) VALUES(?,1,1001,'DELIVERY','D8只读业务矩阵',100,'PUBLISHED')", id)
				for i := range 3 {
					key := uuid.NewString()
					req := httptest.NewRequest("POST", fmt.Sprintf("/api/errands/%d/grab", id), strings.NewReader(`{"expectedRound":0}`))
					req.Header.Set("Authorization", "Bearer "+tokens[1001])
					req.Header.Set("Idempotency-Key", key)
					w := httptest.NewRecorder()
					router.ServeHTTP(w, req)
					if i < 2 {
						if w.Code != 409 {
							t.Fatal("owner rejection", w.Code, w.Body.String())
						}
						continue
					}
					if w.Code != 429 || w.Header().Get("Retry-After") == "" || !strings.Contains(w.Body.String(), "GRAB_RATE_LIMITED") {
						t.Fatal("admission response", w.Code, w.Body.String())
					}
					var count int
					if e := db.QueryRow("SELECT COUNT(*) FROM grab_request WHERE request_key=?", key).Scan(&count); e != nil || count != 0 {
						t.Fatal("rejected admission wrote request", count, e)
					}
				}
				exec(t, "DELETE FROM grab_repair_task WHERE errand_id=?", id)
				exec(t, "DELETE FROM grab_request WHERE errand_id=?", id)
				r.Del(ctx, fmt.Sprintf("campusgo:%s:rl:{runner:1001}:all", namespace+mode), fmt.Sprintf("campusgo:%s:rl:{runner:1001}:errand:%d", namespace+mode, id))
			})
			t.Run("credit_window_limit_order_and_identity", func(t *testing.T) {
				for _, path := range []string{"/api/credit", "/api/credit/self"} {
					w := get(path, tokens[1001])
					var body struct {
						Data mysqlstore.CreditProfile `json:"data"`
					}
					if json.Unmarshal(w.Body.Bytes(), &body) != nil || w.Code != 200 {
						t.Fatal(w.Code, w.Body.String())
					}
					p := body.Data
					if p.WindowDays != 30 || len(p.Events) != 50 || p.Events[0].RefID != refs[54] || p.Events[49].RefID != refs[5] || p.Events[0].Description != "完成结算" {
						t.Fatalf("wrong window/order: %+v", p)
					}
					if body.Data.Version < 0 {
						t.Fatal("invalid version")
					}
				}
				w := get("/api/credit/self?userId=1001", tokens[2002])
				var body struct {
					Data mysqlstore.CreditProfile `json:"data"`
				}
				if json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Data.Events) != 1 || body.Data.Events[0].RefID != otherRef {
					t.Fatal("credit identity leaked", w.Body.String())
				}
			})
			t.Run("campus_ranking_sql_and_redis_projection", func(t *testing.T) {
				key := "campusgo:" + namespace + ":credit:rank:1"
				r.Del(ctx, key)
				for range 2 {
					w := get("/api/credit/ranking?limit=2", tokens[1001])
					var body struct {
						Data []mysqlstore.CreditRank `json:"data"`
					}
					if json.Unmarshal(w.Body.Bytes(), &body) != nil || w.Code != 200 || len(body.Data) != 2 {
						t.Fatal(w.Code, w.Body.String())
					}
					if body.Data[0].UserID != rankUsers[1] || body.Data[1].UserID != rankUsers[0] {
						t.Fatal("ranking order/campus", body.Data)
					}
				}
				members, e := r.ZRevRange(ctx, key, 0, -1).Result()
				if e != nil || !slices.Contains(members, strconv.FormatInt(rankUsers[0], 10)) || slices.Contains(members, strconv.FormatInt(rankUsers[2], 10)) {
					t.Fatal("not a campus ZSET", members, e)
				}
				ttl, e := r.TTL(ctx, key).Result()
				if e != nil || ttl <= 0 || ttl > 30*time.Second {
					t.Fatal("unbounded projection", ttl, e)
				}
				for _, tc := range []struct {
					path   string
					status int
				}{{"/api/credit/ranking?campusId=2", 403}, {"/api/credit/ranking?limit=0", 400}, {"/api/credit/ranking?limit=101", 400}, {"/api/credit/ranking?limit=no", 400}, {"/api/credit/ranking?campusId=9223372036854775808", 400}} {
					if w := get(tc.path, tokens[1001]); w.Code != tc.status {
						t.Fatal(tc.path, w.Code)
					}
				}
				if w := get("/api/credit/self", ""); w.Code != 401 {
					t.Fatal("forged identity accepted", w.Code)
				}
				if w := get("/api/credit/ranking", ""); w.Code != 401 {
					t.Fatal("ranking unauthenticated", w.Code)
				}
			})
		})
	}
}
