package notify

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/yumestk/CampusGo/internal/auth"
	"github.com/yumestk/CampusGo/internal/platform/database"
	"github.com/yumestk/CampusGo/internal/platform/rediscache"
)

func TestIntegrationD7Realtime(t *testing.T) {
	for _, mode := range []string{"session", "jwt"} {
		t.Run(mode, func(t *testing.T) { runD7Realtime(t, mode) })
	}
}
func runD7Realtime(t *testing.T, mode string) {
	if os.Getenv("CAMPUSGO_INTEGRATION") != "1" {
		t.Skip("run make verify-d7")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := database.Open(ctx, "campusgo_verification")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r, err := rediscache.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	s := &auth.Service{DB: db, Redis: r, Config: auth.Config{Environment: "test", Namespace: uuid.NewString(), Demo: true, Mode: mode, JWTSecret: strings.Repeat("j", 40), RefreshTTL: time.Hour, SessionTTL: time.Hour, FrontendOrigin: "http://127.0.0.1:15173"}}
	a, b := NewHub(s, "campusgo_verification"), NewHub(s, "campusgo_verification")
	done := make(chan struct{}, 2)
	for _, h := range []*Hub{a, b} {
		go func() { h.Run(ctx); done <- struct{}{} }()
	}
	defer func() { cancel(); <-done; <-done }()
	for _, h := range []*Hub{a, b} {
		waitFor(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.ready })
	}
	sa, sb := httptest.NewServer(a), httptest.NewServer(b)
	defer sa.Close()
	defer sb.Close()
	login, err := s.DemoLogin(ctx, 1001)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Logout(ctx, login.Token)
	open := func(server, token string) *websocket.Conn {
		t.Helper()
		c, _, e := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server, "http"), &websocket.DialOptions{HTTPHeader: map[string][]string{"Origin": {s.Config.FrontendOrigin}}})
		if e != nil {
			t.Fatal(e)
		}
		if e = wsjson.Write(ctx, c, map[string]string{"token": token}); e != nil {
			t.Fatal(e)
		}
		var event Event
		read, stop := context.WithTimeout(ctx, time.Second)
		defer stop()
		if e = wsjson.Read(read, c, &event); e != nil || event.Type != "ready" {
			t.Fatal("ready", event, e)
		}
		return c
	}
	one, two := open(sa.URL, login.Token), open(sb.URL, login.Token)
	defer one.CloseNow()
	defer two.CloseNow()
	// Each connection is on a different HTTP server/node. A single sender locates both.
	if err = a.Publish(ctx, 1001, Event{Type: "notification.new", Payload: map[string]string{"content": "D7 multi-node"}}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*websocket.Conn{one, two} {
		var e Event
		read, stop := context.WithTimeout(ctx, 2*time.Second)
		err = wsjson.Read(read, c, &e)
		stop()
		if err != nil || e.Type != "notification.new" {
			t.Fatal(e, err)
		}
	}

	t.Run("multiple_sockets_on_one_node_are_routed_once_and_cleaned_individually", func(t *testing.T) {
		third := open(sa.URL, login.Token)
		if e := a.Publish(ctx, 1001, Event{Type: "notification.new", Payload: "first"}); e != nil {
			t.Fatal(e)
		}
		for _, c := range []*websocket.Conn{one, two, third} {
			var e Event
			read, stop := context.WithTimeout(ctx, time.Second)
			err := wsjson.Read(read, c, &e)
			stop()
			if err != nil || e.Payload != "first" {
				t.Fatal(e, err)
			}
		}
		third.CloseNow()
		waitFor(t, func() bool {
			members, _ := locateScript.Run(ctx, r, []string{a.routeKey(1001)}).StringSlice()
			return len(members) == 2
		})
		if e := a.Publish(ctx, 1001, Event{Type: "notification.new", Payload: "second"}); e != nil {
			t.Fatal(e)
		}
		for _, c := range []*websocket.Conn{one, two} {
			var e Event
			read, stop := context.WithTimeout(ctx, time.Second)
			err := wsjson.Read(read, c, &e)
			stop()
			if err != nil || e.Payload != "second" {
				t.Fatal("duplicate node publication or sibling cleanup", e, err)
			}
		}
	})
	t.Run("per_user_connection_limit", func(t *testing.T) {
		extra := []*websocket.Conn{}
		defer func() {
			for _, c := range extra {
				c.CloseNow()
			}
		}()
		for range 4 {
			extra = append(extra, open(sa.URL, login.Token))
		}
		c, _, e := websocket.Dial(ctx, "ws"+strings.TrimPrefix(sa.URL, "http"), &websocket.DialOptions{HTTPHeader: map[string][]string{"Origin": {s.Config.FrontendOrigin}}})
		if e != nil {
			t.Fatal(e)
		}
		defer c.CloseNow()
		wsjson.Write(ctx, c, map[string]string{"token": login.Token})
		var event Event
		read, stop := context.WithTimeout(ctx, time.Second)
		defer stop()
		e = wsjson.Read(read, c, &event)
		if websocket.CloseStatus(e) != websocket.StatusTryAgainLater {
			t.Fatal("user connection cap", event, e)
		}
	})
	waitFor(t, func() bool {
		members, _ := locateScript.Run(ctx, r, []string{a.routeKey(1001)}).StringSlice()
		return len(members) == 2
	})
	t.Run("namespace_and_stale_lease", func(t *testing.T) {
		other := NewHub(s, "other-db")
		if nodes, _ := locateScript.Run(ctx, r, []string{other.routeKey(1001)}).StringSlice(); len(nodes) != 0 {
			t.Fatal("namespace leak")
		}
		r.ZAdd(ctx, a.routeKey(1001), redis.Z{Score: 1, Member: "dead-node"})
		nodes, e := locateScript.Run(ctx, r, []string{a.routeKey(1001)}).StringSlice()
		if e != nil || len(nodes) != 2 {
			t.Fatal(nodes, e)
		}
	})
	t.Run("origin_and_url_credentials_rejected", func(t *testing.T) {
		for _, suffix := range []string{"", "?token=secret"} {
			origin := "http://evil.invalid"
			if suffix != "" {
				origin = s.Config.FrontendOrigin
			}
			c, resp, e := websocket.Dial(ctx, "ws"+strings.TrimPrefix(sa.URL, "http")+suffix, &websocket.DialOptions{HTTPHeader: map[string][]string{"Origin": {origin}}})
			if c != nil {
				c.CloseNow()
			}
			if e == nil || resp == nil || resp.StatusCode != 403 {
				t.Fatal("unsafe upgrade", e)
			}
		}
	})
	t.Run("revoked_identity_receives_no_hint", func(t *testing.T) {
		if err = s.Logout(ctx, login.Token); err != nil {
			t.Fatal(err)
		}
		if err = a.Publish(ctx, 1001, Event{Type: "notification.new", Payload: "private"}); err != nil {
			t.Fatal(err)
		}
		for _, c := range []*websocket.Conn{one, two} {
			var e Event
			read, stop := context.WithTimeout(ctx, 3*time.Second)
			err = wsjson.Read(read, c, &e)
			stop()
			if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
				t.Fatal("revoked socket", e, err)
			}
		}
	})
	waitFor(t, func() bool {
		nodes, _ := locateScript.Run(ctx, r, []string{a.routeKey(1001)}).StringSlice()
		return len(nodes) == 0
	})
	t.Run("expired_credential_and_heartbeat", func(t *testing.T) {
		short := *s
		short.Config.SessionTTL = 8 * time.Second
		login, e := short.DemoLogin(ctx, 2001)
		if e != nil {
			t.Fatal(e)
		}
		defer short.Logout(ctx, login.Token)
		c := open(sa.URL, login.Token)
		defer c.CloseNow()
		start := time.Now()
		for {
			var event Event
			read, stop := context.WithTimeout(ctx, 12*time.Second)
			e = wsjson.Read(read, c, &event)
			stop()
			if e != nil {
				break
			}
		}
		if websocket.CloseStatus(e) != websocket.StatusPolicyViolation || time.Since(start) < 8*time.Second {
			t.Fatal("expiry or >3s heartbeat failed", e)
		}
	})
	t.Run("invalid_auth_and_no_auth_deadline", func(t *testing.T) {
		for _, body := range []string{`{"token":"invalid"}`, ""} {
			c, _, e := websocket.Dial(ctx, "ws"+strings.TrimPrefix(sa.URL, "http"), &websocket.DialOptions{HTTPHeader: map[string][]string{"Origin": {s.Config.FrontendOrigin}}})
			if e != nil {
				t.Fatal(e)
			}
			if body != "" {
				c.Write(ctx, websocket.MessageText, []byte(body))
			}
			var ev Event
			read, stop := context.WithTimeout(ctx, 5*time.Second)
			e = wsjson.Read(read, c, &ev)
			stop()
			c.CloseNow()
			if e == nil {
				t.Fatal("unauthenticated socket active")
			}
		}
	})
	t.Run("bounded_frames", func(t *testing.T) {
		login, e := s.DemoLogin(ctx, 2002)
		if e != nil {
			t.Fatal(e)
		}
		defer s.Logout(ctx, login.Token)
		c := open(sa.URL, login.Token)
		defer c.CloseNow()
		c.Write(ctx, websocket.MessageText, []byte(strings.Repeat("x", 8192)))
		var v any
		read, stop := context.WithTimeout(ctx, time.Second)
		defer stop()
		if wsjson.Read(read, c, &v) == nil {
			t.Fatal("oversize accepted")
		}
	})
	t.Run("node_shutdown_closes_connections", func(t *testing.T) {
		login, e := s.DemoLogin(ctx, 2001)
		if e != nil {
			t.Fatal(e)
		}
		defer s.Logout(context.Background(), login.Token)
		c := open(sa.URL, login.Token)
		defer c.CloseNow()
		cancel()
		var v any
		read, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		if wsjson.Read(read, c, &v) == nil {
			t.Fatal("shutdown retained socket")
		}
	})

}
func waitFor(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition timed out")
}
