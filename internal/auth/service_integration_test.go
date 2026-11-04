package auth

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/yumestk/CampusGo/internal/platform/database"
	"github.com/yumestk/CampusGo/internal/platform/rediscache"
)

func integrationService(t *testing.T) *Service {
	t.Helper()
	if os.Getenv("CAMPUSGO_INTEGRATION") != "1" {
		t.Skip("run make verify-d1 with isolated dependencies")
	}
	db, err := database.Open(context.Background(), "campusgo_verification")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cache, err := rediscache.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cache.Close() })
	return &Service{DB: db, Redis: cache, Config: Config{Environment: "test", Namespace: uuid.NewString(), Demo: true, SessionTTL: 2 * time.Minute}}
}
func TestIntegrationSessionLifecycle(t *testing.T) {
	s := integrationService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	assertCode(t, func() error { _, err := s.DemoLogin(ctx, -1); return err }(), "UNAUTHORIZED")
	assertCode(t, func() error { _, err := s.DemoLogin(ctx, 30000); return err }(), "UNAUTHORIZED")
	var mu sync.Mutex
	tokens := map[string]bool{}
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			session, err := s.DemoLogin(ctx, 1001)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if tokens[session.Token] {
				t.Error("duplicate token")
			}
			tokens[session.Token] = true
		})
	}
	wg.Wait()
	if len(tokens) != 12 {
		t.Fatalf("sessions=%d", len(tokens))
	}
	for token := range tokens {
		t.Cleanup(func() { s.Redis.Del(context.Background(), s.key("session", token)) })
		before, err := s.Redis.PTTL(ctx, s.key("session", token)).Result()
		if err != nil {
			t.Fatal(err)
		}
		user, err := s.Resolve(ctx, token)
		if err != nil || user.ID != 1001 {
			t.Fatalf("resolve: %v", err)
		}
		after, err := s.Redis.PTTL(ctx, s.key("session", token)).Result()
		if err != nil || after <= 0 || after > before {
			t.Fatalf("session TTL was extended: %s -> %s", before, after)
		}
		foreign := *s
		foreign.Config.Namespace = "different-" + s.Config.Namespace
		_, err = foreign.Resolve(ctx, token)
		assertCode(t, err, "UNAUTHORIZED")
		live := *s
		live.Config.Demo = false
		_, err = live.Resolve(ctx, token)
		assertCode(t, err, "UNAUTHORIZED")
		if err := s.Logout(ctx, token); err != nil {
			t.Fatal(err)
		}
		if err := s.Logout(ctx, token); err != nil {
			t.Fatal(err)
		}
		_, err = s.Resolve(ctx, token)
		assertCode(t, err, "UNAUTHORIZED")
	}
	session, err := s.DemoLogin(ctx, 1001)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Redis.PExpire(ctx, s.key("session", session.Token), time.Millisecond).Err(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	_, err = s.Resolve(ctx, session.Token)
	assertCode(t, err, "UNAUTHORIZED")
	if err := s.Redis.Set(ctx, s.key("session", session.Token), "not-an-id", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	defer s.Redis.Del(context.Background(), s.key("session", session.Token))
	_, err = s.Resolve(ctx, session.Token)
	assertCode(t, err, "INTERNAL_ERROR")
}
