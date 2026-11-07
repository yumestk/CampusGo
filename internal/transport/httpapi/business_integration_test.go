package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	errand "github.com/yumestk/CampusGo/internal/adapter/mysql"
	"github.com/yumestk/CampusGo/internal/auth"
	"github.com/yumestk/CampusGo/internal/platform/database"
	"github.com/yumestk/CampusGo/internal/platform/idgen"
	"github.com/yumestk/CampusGo/internal/platform/rediscache"
	"github.com/yumestk/CampusGo/internal/platform/telemetry"
)

func TestIntegrationHTTPIdentityAndQueries(t *testing.T) {
	if os.Getenv("CAMPUSGO_INTEGRATION") != "1" {
		t.Skip("run make verify-d1")
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
	router := NewRouter(slog.New(slog.NewJSONHandler(io.Discard, nil)), telemetry.NewRegistry("api"))
	ids, _ := idgen.New(1020)
	registerTestBusiness(router, s, errand.Query{DB: db}, &errand.PublishStore{DB: db, IDs: ids})
	request := func(method, path, token, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-User-Id", "9001")
		r.Header.Set("X-Campus-Id", "2")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/api/users/me", "/api/errands", "/api/errands/9007199254740993"} {
		if w := request("GET", path, "", ""); w.Code != 401 {
			t.Fatalf("forged header status=%d", w.Code)
		}
	}
	tokens := map[int64]string{}
	cursor := base64.RawURLEncoding.EncodeToString([]byte("1793750400123:9007199254740994"))
	for _, id := range []int64{1001, 2001, 9001} {
		w := request("POST", "/api/auth/login", "", fmt.Sprintf(`{"userId":"%d"}`, id))
		var out struct {
			Data auth.Session `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || out.Data.UserID != id {
			t.Fatalf("login status=%d err=%v", w.Code, err)
		}
		tokens[id] = out.Data.Token
		defer s.Logout(context.Background(), out.Data.Token)
	}
	for _, tc := range []struct {
		path     string
		id       int64
		status   int
		contains string
	}{
		{"/api/users/me", 1001, 200, `"id":"1001"`},
		{"/api/errands/9007199254740993", 2001, 200, `"rewardCents":"9223372036854775807"`},
		{"/api/errands/9007199254740993", 2001, 200, `"id":"9007199254740993"`},
		{"/api/errands/9007199254740993", 2001, 200, `"title":"D1只读验证:精度边界"`},
		{"/api/errands/9007199254740995", 2001, 403, "FORBIDDEN"},
		{"/api/errands/9007199254740995", 1001, 200, `"role":"PUBLISHER"`},
		{"/api/errands/9007199254740995", 9001, 200, `"role":"ARBITRATOR"`},
		{"/api/errands/9007199254740997", 1001, 404, "ERRAND_NOT_FOUND"},
		{"/api/errands/9007199254740996", 2001, 200, `"role":"CURRENT_RUNNER"`},
		{"/api/errands/9007199254740993/timeline", 2001, 200, `"operatorId":"1001"`},
		{"/api/errands?cursor=&size=1", 2001, 200, `"nextCursor":"` + cursor + `"`},
		{"/api/errands?cursor=" + cursor + "&size=1", 2001, 200, `"id":"9007199254740993"`},
		{"/api/errands?size=101", 1001, 400, "INVALID_REQUEST"},
		{"/api/errands?cursor=9223372036854775808", 1001, 400, "INVALID_REQUEST"},
		{"/api/errands?status=bogus", 1001, 400, "INVALID_REQUEST"},
		{"/api/errands?campusId=2", 1001, 403, "FORBIDDEN"},
		{"/api/errands/mine?role=ANY", 1001, 400, "INVALID_REQUEST"},
		{"/api/errands/mine?role=GRABBED_BY_ME", 2001, 200, `"role":"CURRENT_RUNNER"`},
	} {
		w := request("GET", tc.path, tokens[tc.id], "")
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.contains) {
			t.Fatalf("%s: status=%d body=%s", tc.path, w.Code, w.Body.String())
		}

	}
	wHistory := request("GET", "/api/errands/mine?cursor=&role=GRABBED_BY_ME", tokens[2001], "")
	var history struct {
		Data errand.Page `json:"data"`
	}
	if err := json.Unmarshal(wHistory.Body.Bytes(), &history); err != nil || wHistory.Code != 200 {
		t.Fatal("historical grabs unavailable", err)
	}
	count := 0
	for _, c := range history.Data.Items {
		if c.ID == 9007199254740994 {
			count++
			if c.GrabberID != nil {
				t.Fatal("fixture must be a historical runner")
			}
		}
	}
	if count != 1 {
		t.Fatal("historical grabs missing or duplicate", count)
	}
	for _, body := range []string{`{"userId":1001,"role":"ARBITRATOR"}`, `{"userId":1001} {}`, `{"userId":1e3}`, strings.Repeat("x", 17000)} {
		if w := request("POST", "/api/auth/login", "", body); w.Code != 400 {
			t.Fatalf("bad JSON status=%d", w.Code)
		}
	}
	if w := request("POST", "/api/auth/login", "", `{"userId":30000}`); w.Code != 401 {
		t.Fatal("arbitrary demo user accepted")
	}
	// A real socket failure is 503 and does not delete the healthy session.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := l.Addr().String()
	l.Close()
	broken := *s
	broken.Redis = redis.NewClient(&redis.Options{Addr: address, MaxRetries: -1, DialTimeout: 100 * time.Millisecond})
	defer broken.Redis.Close()
	failedRouter := NewRouter(slog.New(slog.NewJSONHandler(io.Discard, nil)), telemetry.NewRegistry("api"))
	registerTestBusiness(failedRouter, &broken, errand.Query{DB: db}, &errand.PublishStore{DB: db, IDs: ids})
	r := httptest.NewRequest("GET", "/api/users/me", nil)
	r.Header.Set("Authorization", "Bearer "+tokens[1001])
	w := httptest.NewRecorder()
	failedRouter.ServeHTTP(w, r)
	if w.Code != 503 || w.Header().Get("Retry-After") != "1" {
		t.Fatalf("dependency response=%d %s", w.Code, w.Body.String())
	}
	if _, err := s.Resolve(context.Background(), tokens[1001]); err != nil {
		t.Fatal("dependency failure revoked session", err)
	}
	if w := request("POST", "/api/auth/logout", tokens[1001], ""); w.Code != 200 {
		t.Fatal("logout failed")
	}
	if w := request("GET", "/api/users/me", tokens[1001], ""); w.Code != 401 {
		t.Fatal("logout did not revoke token")
	}
}
