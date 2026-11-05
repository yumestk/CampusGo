package httpapi

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/yumestk/CampusGo/internal/auth"
	"github.com/yumestk/CampusGo/internal/errand"
	"github.com/yumestk/CampusGo/internal/platform/database"
	"github.com/yumestk/CampusGo/internal/platform/idgen"
	"github.com/yumestk/CampusGo/internal/platform/rediscache"
	"github.com/yumestk/CampusGo/internal/platform/telemetry"
)

func TestIntegrationD3HTTP(t *testing.T) {
	if os.Getenv("CAMPUSGO_D3_INTEGRATION") != "1" {
		t.Skip("run make verify-d3")
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
	ids, _ := idgen.New(1017)
	id, e := ids.Next(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("INSERT INTO errand (id,campus_id,publisher_id,type,title,reward_amount,status,version) VALUES (?,1,1001,'DELIVERY','D3接口验证',100,'PUBLISHED',1)", id); e != nil {
		t.Fatal(e)
	}
	defer func() {
		for _, q := range []string{"DELETE FROM grab_repair_task WHERE errand_id=?", "DELETE FROM local_message WHERE errand_id=?", "DELETE FROM grab_record WHERE errand_id=?", "DELETE FROM grab_request WHERE errand_id=?", "DELETE FROM errand_status_log WHERE errand_id=?", "DELETE FROM errand WHERE id=?"} {
			if _, e := db.Exec(q, id); e != nil {
				t.Error(e)
			}
		}
	}()
	namespace := "d3-http-" + uuid.NewString()
	s := &auth.Service{DB: db, Redis: cache, Config: auth.Config{Environment: "test", Namespace: namespace, Demo: true, SessionTTL: time.Minute}}
	a, e := s.DemoLogin(ctx, 2001)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Logout(ctx, a.Token)
	b, e := s.DemoLogin(ctx, 2002)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Logout(ctx, b.Token)
	g := &errand.Grabber{DB: db, Redis: cache, IDs: ids, Namespace: namespace}
	defer func() {
		var cursor uint64
		for {
			keys, next, e := cache.Scan(ctx, cursor, "campusgo:"+namespace+":*", 100).Result()
			if e != nil {
				t.Error(e)
				return
			}
			if len(keys) > 0 {
				cache.Del(ctx, keys...)
			}
			cursor = next
			if next == 0 {
				return
			}
		}
	}()
	router := NewRouter(slog.New(slog.NewJSONHandler(io.Discard, nil)), telemetry.NewRegistry("api"))
	RegisterBusiness(router, s, errand.Query{DB: db}, &errand.Publisher{DB: db, IDs: ids}, g)
	send := func(method, path, body, token string, keys ...string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-User-Id", "9001")
		r.Header.Set("X-Campus-Id", "2")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		for _, key := range keys {
			r.Header.Add("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	path := fmt.Sprintf("/api/errands/%d/grab", id)
	key := uuid.NewString()
	resultPath := fmt.Sprintf("/api/errands/%d/grab-requests/%s", id, key)
	if w := send("POST", path, `{"expectedRound":0}`, "", key); w.Code != 401 {
		t.Fatal("forged identity accepted", w.Code)
	}
	for _, body := range []string{`{}`, `{"expectedRound":null}`, `{"expectedRound":-1}`, `{"expectedRound":2147483648}`, `{"expectedRound":1e3}`, `{"expectedRound":0,"runnerId":"9001"}`, `{"expectedRound":0} {}`} {
		if w := send("POST", path, body, a.Token, key); w.Code != 400 {
			t.Fatal("invalid body", body, w.Code, w.Body.String())
		}
	}
	for _, keys := range [][]string{nil, {"bad"}, {key, key}} {
		if w := send("POST", path, `{"expectedRound":0}`, a.Token, keys...); w.Code != 400 {
			t.Fatal("invalid key", w.Code)
		}
	}
	if w := send("GET", resultPath, "", a.Token); w.Code != 202 || !strings.Contains(w.Body.String(), "NOT_OBSERVED") || w.Header().Get("Retry-After") != "1" {
		t.Fatal(w.Code, w.Body.String())
	}
	rejectKey := uuid.NewString()
	if w := send("POST", path, `{"expectedRound":1}`, a.Token, rejectKey); w.Code != 409 || !strings.Contains(w.Body.String(), `"state":"REJECTED"`) || !strings.Contains(w.Body.String(), `"code":"STALE_VERSION"`) {
		t.Fatal("durable rejection protocol", w.Code, w.Body.String())
	}
	rejectPath := fmt.Sprintf("/api/errands/%d/grab-requests/%s", id, rejectKey)
	if w := send("GET", rejectPath, "", a.Token); w.Code != 409 || !strings.Contains(w.Body.String(), `"state":"REJECTED"`) {
		t.Fatal("durable rejection query", w.Code, w.Body.String())
	}
	for range 2 {
		if w := send("POST", path, `{"expectedRound":0}`, a.Token, key); w.Code != 200 || !strings.Contains(w.Body.String(), "SUCCEEDED") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if w := send("POST", path, `{"expectedRound":1}`, a.Token, key); w.Code != 409 || !strings.Contains(w.Body.String(), "IDEMPOTENCY_CONFLICT") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := send("GET", resultPath, "", b.Token); w.Code != 202 || !strings.Contains(w.Body.String(), "NOT_OBSERVED") {
		t.Fatal("other user read result", w.Code, w.Body.String())
	}
	if w := send("GET", resultPath, "", a.Token); w.Code != 200 || !strings.Contains(w.Body.String(), "SUCCEEDED") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := send("GET", fmt.Sprintf("/api/errands/%d", id), "", a.Token); w.Code != 200 || !strings.Contains(w.Body.String(), `"grabberId":"2001"`) || !strings.Contains(w.Body.String(), `"availableActions":["CONFIRM"]`) {
		t.Fatal("authoritative detail", w.Code, w.Body.String())
	}
}
