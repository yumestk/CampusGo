package httpapi

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	errand "github.com/yumestk/CampusGo/internal/adapter/mysql"
	"github.com/yumestk/CampusGo/internal/auth"
	"github.com/yumestk/CampusGo/internal/platform/database"
	"github.com/yumestk/CampusGo/internal/platform/idgen"
	"github.com/yumestk/CampusGo/internal/platform/rediscache"
	"github.com/yumestk/CampusGo/internal/platform/telemetry"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestIntegrationD5HTTP(t *testing.T) {
	if os.Getenv("CAMPUSGO_D5_INTEGRATION") != "1" {
		t.Skip("run make verify-d5")
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
	ids, _ := idgen.New(1015)
	id, _ := ids.Next(ctx)
	if _, e = db.Exec("INSERT INTO errand(id,campus_id,publisher_id,type,title,reward_amount,status) VALUES(?,1,1001,'DELIVERY','D5接口验证',100,'PUBLISHED')", id); e != nil {
		t.Fatal(e)
	}
	defer func() {
		for _, q := range []string{"DELETE FROM fund_request WHERE errand_id=?", "DELETE FROM errand WHERE id=?"} {
			if _, e := db.Exec(q, id); e != nil {
				t.Error(e)
			}
		}
	}()
	s := &auth.Service{DB: db, Redis: cache, Config: auth.Config{Environment: "test", Namespace: "d5-http-" + uuid.NewString(), Demo: true, SessionTTL: time.Minute}}
	publisher, e := s.DemoLogin(ctx, 1001)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Logout(ctx, publisher.Token)
	runner, e := s.DemoLogin(ctx, 2001)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Logout(ctx, runner.Token)
	g := &errand.TaskStore{DB: db, Redis: cache, IDs: ids, Namespace: "campusgo_verification"}
	router := NewRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), telemetry.NewRegistry("api"))
	registerTestBusiness(router, s, errand.Query{DB: db}, &errand.PublishStore{DB: db, IDs: ids}, g)
	path := fmt.Sprintf("/api/errands/%d/actions", id)
	send := func(method, path, body, token string, keys ...string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-User-Id", "9001")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		for _, k := range keys {
			r.Header.Add("Idempotency-Key", k)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	body := `{"action":"CANCEL","expectedRound":0}`
	if w := send("POST", path, body, ""); w.Code != 401 {
		t.Fatal("forged auth accepted", w.Code)
	}
	for _, b := range []string{`{}`, `{"action":"CANCEL","expectedRound":null}`, `{"action":"CANCEL","expectedRound":-1}`, `{"action":"CANCEL","expectedRound":2147483648}`, `{"action":"AUTO_SETTLE","expectedRound":0}`, `{"action":"CANCEL","expectedRound":0,"actorId":"-1"}`, `{"action":"CANCEL","expectedRound":0} {}`} {
		if w := send("POST", path, b, publisher.Token, uuid.NewString()); w.Code != 400 {
			t.Fatal(b, w.Code, w.Body.String())
		}
	}
	for _, keys := range [][]string{nil, {"bad"}, {uuid.NewString(), uuid.NewString()}} {
		if w := send("POST", path, body, publisher.Token, keys...); w.Code != 400 {
			t.Fatal("invalid key", w.Code)
		}
	}
	if w := send("POST", path, body, runner.Token, uuid.NewString()); w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
	key := uuid.NewString()
	if w := send("POST", path, body, publisher.Token, key); w.Code != 503 {
		t.Fatal("money SQL ran without sender", w.Code, w.Body.String())
	}
	if w := send("GET", "/api/fund-requests/"+key, "", publisher.Token); w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"PREPARED"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := send("GET", "/api/fund-requests/"+key, "", runner.Token); w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"NOT_OBSERVED"`) {
		t.Fatal("result leaked", w.Code, w.Body.String())
	}
	if w := send("GET", "/api/credit", "", runner.Token); w.Code != 200 || !strings.Contains(w.Body.String(), `"version":"`) {
		t.Fatal("credit wire", w.Code, w.Body.String())
	}
}
