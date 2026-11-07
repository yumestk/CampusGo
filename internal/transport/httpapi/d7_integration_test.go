package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	errand "github.com/yumestk/CampusGo/internal/adapter/mysql"
	"github.com/yumestk/CampusGo/internal/auth"
	"github.com/yumestk/CampusGo/internal/notify"
	"github.com/yumestk/CampusGo/internal/platform/database"
	"github.com/yumestk/CampusGo/internal/platform/rediscache"
	"github.com/yumestk/CampusGo/internal/platform/telemetry"
)

func TestIntegrationD7Notifications(t *testing.T) {
	if os.Getenv("CAMPUSGO_INTEGRATION") != "1" {
		t.Skip("run make verify-d7")
	}
	ctx := context.Background()
	db, e := database.Open(ctx, "campusgo_verification")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	r, e := rediscache.Open()
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	s := &auth.Service{DB: db, Redis: r, Config: auth.Config{Environment: "test", Namespace: uuid.NewString(), Demo: true, SessionTTL: time.Hour, RequireGateway: true, GatewaySecret: strings.Repeat("g", 40)}}
	router := NewRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), telemetry.NewRegistry("api"))
	registerTestBusiness(router, s, errand.Query{DB: db}, &errand.PublishStore{DB: db})
	a, e := s.DemoLogin(ctx, 1001)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Logout(ctx, a.Token)
	b, e := s.DemoLogin(ctx, 2001)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Logout(ctx, b.Token)
	call := func(method, path, token string) *httptest.ResponseRecorder {
		q := httptest.NewRequest(method, path, nil)
		q.Header.Set("Authorization", "Bearer "+token)
		q.Header.Set("X-CampusGo-Gateway", s.Config.GatewaySecret)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, q)
		return w
	}
	key := uuid.NewString()
	defer db.Exec("DELETE FROM notification WHERE msg_key=?", key)
	var ids []int64
	for _, u := range []int64{1001, 2001} {
		res, e := db.Exec("INSERT INTO notification(msg_key,user_id,errand_id,type,content) VALUES(?,?,1,'CANCEL','D7 query fixture')", key, u)
		if e != nil {
			t.Fatal(e)
		}
		id, _ := res.LastInsertId()
		ids = append(ids, id)
	}
	t.Run("pagination_int64_and_ownership", func(t *testing.T) {
		w := call("GET", "/api/notifications?size=1", a.Token)
		var p struct{ Data notify.Page }
		if json.Unmarshal(w.Body.Bytes(), &p) != nil || w.Code != 200 || len(p.Data.Items) != 1 || p.Data.Items[0].ID != ids[0] {
			t.Fatal(w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), fmt.Sprintf(`"id":"%d"`, ids[0])) {
			t.Fatal("numeric int64")
		}
		w = call("POST", fmt.Sprintf("/api/notifications/%d/read", ids[0]), b.Token)
		if w.Code != 404 {
			t.Fatal("foreign read", w.Code)
		}
		for range 2 {
			if w = call("POST", fmt.Sprintf("/api/notifications/%d/read", ids[0]), a.Token); w.Code != 200 {
				t.Fatal(w.Body.String())
			}
		}
		var flag bool
		db.QueryRow("SELECT read_flag FROM notification WHERE id=?", ids[0]).Scan(&flag)
		if !flag {
			t.Fatal("not marked")
		}
	})
	t.Run("unread_and_invalid_parameters", func(t *testing.T) {
		w := call("GET", "/api/notifications/unread", b.Token)
		var v struct{ Data struct{ Count string } }
		if json.Unmarshal(w.Body.Bytes(), &v) != nil || v.Data.Count == "0" || w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		for _, path := range []string{"?size=0", "?size=101", "?cursor=-1", "?cursor=1e3", "?cursor=01"} {
			w = call("GET", "/api/notifications"+path, b.Token)
			if w.Code != 400 {
				t.Fatal(path, w.Code)
			}
		}
		if w = call("GET", "/api/notifications", ""); w.Code != 401 {
			t.Fatal(w.Code)
		}
	})
	secondKey := uuid.NewString()
	defer db.Exec("DELETE FROM notification WHERE msg_key=?", secondKey)
	if _, e := db.Exec("INSERT INTO notification(msg_key,user_id,errand_id,type,content) VALUES(?,1001,1,'CANCEL','D7 second page')", secondKey); e != nil {
		t.Fatal(e)
	}
	t.Run("cursor_has_no_overlap", func(t *testing.T) {
		var a, b notify.Page
		a, e = notify.List(ctx, db, 1001, "", 1)
		if e != nil {
			t.Fatal(e)
		}
		if a.NextCursor == "" {
			t.Fatal("missing cursor")
		}
		if a.NextCursor != "" {
			b, e = notify.List(ctx, db, 1001, a.NextCursor, 1)
			if e != nil {
				t.Fatal(e)
			}
			if len(b.Items) > 0 && a.Items[0].ID == b.Items[0].ID {
				t.Fatal("duplicate page")
			}
		}
	})
}
