package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
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

func TestIntegrationD2HTTP(t *testing.T) {
	if os.Getenv("CAMPUSGO_D2_INTEGRATION") != "1" {
		t.Skip("run make verify-d2")
	}
	db, err := database.Open(context.Background(), "campusgo_verification")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cache, err := rediscache.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	s := &auth.Service{DB: db, Redis: cache, Config: auth.Config{Environment: "test", Namespace: uuid.NewString(), Demo: true, SessionTTL: time.Minute}}
	session, err := s.DemoLogin(context.Background(), 2002)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Logout(context.Background(), session.Token)
	ids, _ := idgen.New(1020)
	router := NewRouter(slog.New(slog.NewJSONHandler(io.Discard, nil)), telemetry.NewRegistry("api"))
	registerTestBusiness(router, s, mysqlstore.Query{DB: db}, &mysqlstore.PublishStore{DB: db, IDs: ids})
	key := uuid.NewString()
	defer db.Exec("DELETE FROM publish_request WHERE publisher_id=2002 AND request_key=?", key)
	send := func(method, path, body, token string, keys ...string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("X-User-ID", "1001")
		req.Header.Set("X-Campus-ID", "1")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		for _, k := range keys {
			req.Header.Add("Idempotency-Key", k)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"title":"D2接口验证","rewardCents":"0"}`, 400},
		{`{"title":"D2接口验证","rewardCents":"-1"}`, 400},
		{`{"title":"D2接口验证","rewardCents":"9223372036854775808"}`, 400},
		{`{"title":"D2接口验证","rewardCents":1.5}`, 400},
		{`{"title":"D2接口验证","rewardCents":1e3}`, 400},
		{`{"title":"D2接口验证","rewardCents":null}`, 400},
		{`{"title":"D2接口验证","rewardCents":"1","slotTotal":2}`, 400},
		{`{"title":"D2接口验证","rewardCents":"1","publisherId":"1001"}`, 400},
		{`{"title":"D2接口验证","rewardCents":"1","campusId":"2"}`, 403},
		{`{"title":"D2接口验证","rewardCents":"1"} {}`, 400},
	} {
		w := send("POST", "/api/errands", tc.body, session.Token, key)
		if w.Code != tc.status {
			t.Fatalf("body=%s status=%d response=%s", tc.body, w.Code, w.Body.String())
		}
	}
	body := `{"title":"D2接口验证","rewardCents":"1"}`
	for _, keys := range [][]string{nil, {"bad"}, {key, key}} {
		if w := send("POST", "/api/errands", body, session.Token, keys...); w.Code != 400 {
			t.Fatalf("key validation %d: %s", w.Code, w.Body.String())
		}
	}
	if w := send("POST", "/api/errands", body, "", key); w.Code != 401 {
		t.Fatalf("forged user accepted: %d", w.Code)
	}
	for range 2 {
		w := send("POST", "/api/errands", body, session.Token, key)
		if w.Code != 409 || !strings.Contains(w.Body.String(), "INSUFFICIENT_BALANCE") {
			t.Fatalf("rejection=%d %s", w.Code, w.Body.String())
		}
	}
	for _, tc := range []struct {
		path, contains string
		status         int
	}{
		{"/api/publish-requests/" + key, `"state":"REJECTED"`, 200},
		{"/api/publish-requests/" + uuid.NewString(), `"state":"NOT_OBSERVED"`, 200},
		{"/api/wallet", `"availableCents":"0"`, 200},
		{"/api/wallet/ledger", `"items":[]`, 200},
		{"/api/wallet/ledger?cursor=-1", "INVALID_REQUEST", 400},
		{"/api/wallet/ledger?size=101", "INVALID_REQUEST", 400},
		{"/api/wallet/ledger?size=1e3", "INVALID_REQUEST", 400},
		{"/api/errands/9007199254740993/escrow", "ESCROW_NOT_FOUND", 404},
	} {
		w := send("GET", tc.path, "", session.Token)
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.contains) {
			t.Fatalf("path=%s status=%d response=%s", tc.path, w.Code, w.Body.String())
		}
	}
}
