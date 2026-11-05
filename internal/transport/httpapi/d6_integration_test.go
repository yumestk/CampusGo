package httpapi

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/yumestk/CampusGo/internal/auth"
	"github.com/yumestk/CampusGo/internal/errand"
	"github.com/yumestk/CampusGo/internal/platform/database"
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

func TestIntegrationD6HTTP(t *testing.T) {
	if os.Getenv("CAMPUSGO_INTEGRATION") != "1" {
		t.Skip("run make verify-d6")
	}
	db, e := database.Open(context.Background(), "campusgo_verification")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	r, e := rediscache.Open()
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	s := &auth.Service{DB: db, Redis: r, Config: auth.Config{Environment: "test", Namespace: uuid.NewString(), Demo: true, Mode: "jwt", JWTSecret: strings.Repeat("j", 40), SessionTTL: time.Hour, RefreshTTL: 24 * time.Hour, RequireGateway: true, GatewaySecret: strings.Repeat("g", 40)}}
	router := NewRouter(slog.New(slog.NewJSONHandler(io.Discard, nil)), telemetry.NewRegistry("api"))
	RegisterBusiness(router, s, errand.Query{DB: db}, &errand.Publisher{DB: db})
	request := func(method, path, body, token, key, gateway string) *httptest.ResponseRecorder {
		t.Helper()
		q := httptest.NewRequest(method, path, strings.NewReader(body))
		q.Header.Set("Authorization", "Bearer "+token)
		q.Header.Set("Idempotency-Key", key)
		q.Header.Set("X-CampusGo-Gateway", gateway)
		q.Header.Set("X-User-Id", "9001")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, q)
		return w
	}
	w := request("POST", "/api/auth/login", `{"userId":"1001"}`, "", "", "fake")
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	w = request("POST", "/api/auth/login", `{"userId":"1001"}`, "", "", s.Config.GatewaySecret)
	var a struct {
		Data auth.Session `json:"data"`
	}
	if json.Unmarshal(w.Body.Bytes(), &a) != nil || w.Code != 200 || a.Data.Mode != "jwt" {
		t.Fatal(w.Code, w.Body.String())
	}
	defer s.Logout(context.Background(), a.Data.Token)
	w = request("GET", "/api/users/me", "", a.Data.Token, "", s.Config.GatewaySecret)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"1001"`) {
		t.Fatal(w.Body.String())
	}
	w = request("POST", "/api/auth/refresh", `{"refreshToken":"`+a.Data.RefreshToken+`"}`, "", "", s.Config.GatewaySecret)
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
	key := uuid.NewString()
	w = request("POST", "/api/auth/refresh", `{"refreshToken":"`+a.Data.RefreshToken+`"}`, "", key, s.Config.GatewaySecret)
	var b struct {
		Data auth.Session `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &b)
	if w.Code != 200 || b.Data.Token == a.Data.Token {
		t.Fatal(w.Body.String())
	}
	replay := request("POST", "/api/auth/refresh", `{"refreshToken":"`+a.Data.RefreshToken+`"}`, "", key, s.Config.GatewaySecret)
	if w.Body.String() != replay.Body.String() {
		t.Fatal("different replay")
	}
	w = request("POST", "/api/auth/logout", "", a.Data.Token, "", s.Config.GatewaySecret)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = request("GET", "/api/users/me", "", b.Data.Token, "", s.Config.GatewaySecret)
	if w.Code != 401 {
		t.Fatal(w.Body.String())
	}
	w = request("POST", "/api/auth/refresh", `{"refreshToken":"`+b.Data.RefreshToken+`"}`, "", uuid.NewString(), s.Config.GatewaySecret)
	if w.Code != 401 {
		t.Fatal(w.Body.String())
	}
}
