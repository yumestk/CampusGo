package auth

import (
	"context"
	"encoding/json"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"strings"
	"sync"
	"testing"
	"time"
)

func jwtService(t *testing.T) *Service {
	s := integrationService(t)
	s.Config.Mode = "jwt"
	s.Config.JWTSecret = "test-jwt-key-at-least-32-bytes-long"
	s.Config.SessionTTL = 2 * time.Hour
	s.Config.RefreshTTL = 7 * 24 * time.Hour
	t.Cleanup(func() {
		ctx := context.Background()
		var cursor uint64
		for {
			keys, next, e := s.Redis.Scan(ctx, cursor, s.key("jwt-family", "")[:strings.LastIndex(s.key("jwt-family", ""), ":")+1]+"*", 100).Result()
			if e != nil {
				return
			}
			if len(keys) > 0 {
				s.Redis.Del(ctx, keys...)
			}
			cursor = next
			if cursor == 0 {
				return
			}
		}
	})
	return s
}
func TestIntegrationJWTLifecycle(t *testing.T) {
	s := jwtService(t)
	ctx := context.Background()
	login := func() Session {
		t.Helper()
		v, e := s.DemoLogin(ctx, 1001)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	a := login()
	u, e := s.Resolve(ctx, a.Token)
	if e != nil || u.ID != 1001 {
		t.Fatal(u, e)
	}
	c, _ := s.parseJWT(a.Token, false)
	ttl, _ := s.Redis.PTTL(ctx, s.familyKey(c.Family)).Result()
	if ttl < 6*24*time.Hour {
		t.Fatal(ttl)
	}
	t.Run("same request concurrent and lost response", func(t *testing.T) {
		key := uuid.NewString()
		var mu sync.Mutex
		var outs []Session
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				v, e := s.Refresh(ctx, a.RefreshToken, key)
				mu.Lock()
				defer mu.Unlock()
				if e != nil {
					t.Error(e)
				}
				outs = append(outs, v)
			})
		}
		wg.Wait()
		for _, v := range outs {
			if v.Token != outs[0].Token || v.RefreshToken != outs[0].RefreshToken {
				t.Fatal("rotation not idempotent")
			}
		}
		v, e := s.Refresh(ctx, a.RefreshToken, key)
		if e != nil || v.Token != outs[0].Token {
			t.Fatal(e)
		}
		f := *s
		f.Config.Namespace = "foreign"
		_, e = f.Resolve(ctx, v.Token)
		assertCode(t, e, "UNAUTHORIZED")
		_, e = s.Refresh(ctx, a.RefreshToken, uuid.NewString())
		assertCode(t, e, "REFRESH_REUSED")
		_, e = s.Resolve(ctx, v.Token)
		assertCode(t, e, "UNAUTHORIZED")
		_, e = s.Refresh(ctx, v.RefreshToken, uuid.NewString())
		assertCode(t, e, "UNAUTHORIZED")
	})
	t.Run("logout revokes access and refresh including expired access", func(t *testing.T) {
		a := login()
		b, e := s.Refresh(ctx, a.RefreshToken, uuid.NewString())
		if e != nil {
			t.Fatal(e)
		}
		if e = s.Logout(ctx, a.Token); e != nil {
			t.Fatal(e)
		}
		if e = s.Logout(ctx, a.Token); e != nil {
			t.Fatal(e)
		}
		_, e = s.Resolve(ctx, b.Token)
		assertCode(t, e, "UNAUTHORIZED")
		_, e = s.Refresh(ctx, b.RefreshToken, uuid.NewString())
		assertCode(t, e, "UNAUTHORIZED")
	})
	t.Run("unknown refresh cannot revoke known family", func(t *testing.T) {
		a := login()
		f, _, _ := strings.Cut(a.RefreshToken, ".")
		_, e := s.Refresh(ctx, f+"."+randomToken(), uuid.NewString())
		assertCode(t, e, "UNAUTHORIZED")
		_, e = s.Resolve(ctx, a.Token)
		if e != nil {
			t.Fatal(e)
		}
	})
	t.Run("claims algorithm signature expiry and environment", func(t *testing.T) {
		a := login()
		base, _ := s.parseJWT(a.Token, false)
		for _, name := range []string{"issuer", "audience", "expired", "future", "subject", "sid", "jti", "signature", "algorithm"} {
			t.Run(name, func(t *testing.T) {
				c := base
				key := s.Config.JWTSecret
				alg := jose.HS256
				switch name {
				case "issuer":
					c.Issuer = "other"
				case "audience":
					c.Audience = jwt.Audience{"other"}
				case "expired":
					c.Expiry = jwt.NewNumericDate(time.Now().Add(-time.Minute))
				case "future":
					c.NotBefore = jwt.NewNumericDate(time.Now().Add(time.Hour))
				case "subject":
					c.Subject = "9007199254740993x"
				case "sid":
					c.Family = ""
				case "jti":
					c.ID = ""
				case "signature":
					key = strings.Repeat("x", 32)
				case "algorithm":
					alg = jose.HS512
					key = strings.Repeat("x", 64)
				}
				signer, _ := jose.NewSigner(jose.SigningKey{Algorithm: alg, Key: []byte(key)}, nil)
				b, _ := json.Marshal(c)
				signed, _ := signer.Sign(b)
				raw, _ := signed.CompactSerialize()
				_, e = s.Resolve(ctx, raw)
				assertCode(t, e, "UNAUTHORIZED")
			})
		}
		for _, variant := range []string{"demo", "env", "namespace"} {
			foreign := *s
			switch variant {
			case "demo":
				foreign.Config.Demo = false
			case "env":
				foreign.Config.Environment = "local"
			case "namespace":
				foreign.Config.Namespace = "other"
			}
			_, e = foreign.Resolve(ctx, a.Token)
			assertCode(t, e, "UNAUTHORIZED")
		}
	})
	t.Run("rotation keeps absolute family expiry and encrypted receipt", func(t *testing.T) {
		a := login()
		c, _ := s.parseJWT(a.Token, false)
		before, _ := s.Redis.PTTL(ctx, s.familyKey(c.Family)).Result()
		_, e := s.Refresh(ctx, a.RefreshToken, uuid.NewString())
		if e != nil {
			t.Fatal(e)
		}
		after, _ := s.Redis.PTTL(ctx, s.familyKey(c.Family)).Result()
		if after > before {
			t.Fatal("sliding family")
		}
		m, _ := s.Redis.HGetAll(ctx, s.familyKey(c.Family)).Result()
		raw, _ := json.Marshal(m)
		if strings.Contains(string(raw), a.RefreshToken) || strings.Contains(string(raw), "eyJ") {
			t.Fatal("plaintext credentials stored")
		}
	})
	t.Run("expired access can revoke family", func(t *testing.T) {
		s.Config.SessionTTL = time.Second
		a := login()
		time.Sleep(1100 * time.Millisecond)
		_, e := s.Resolve(ctx, a.Token)
		assertCode(t, e, "UNAUTHORIZED")
		if e = s.Logout(ctx, a.Token); e != nil {
			t.Fatal(e)
		}
		_, e = s.Refresh(ctx, a.RefreshToken, uuid.NewString())
		assertCode(t, e, "UNAUTHORIZED")
	})
}

func TestIntegrationD6RefreshLogoutRace(t *testing.T) {
	s := jwtService(t)
	ctx := context.Background()
	for range 20 {
		a, e := s.DemoLogin(ctx, 1001)
		if e != nil {
			t.Fatal(e)
		}
		var next Session
		var refreshErr, logoutErr error
		var wg sync.WaitGroup
		wg.Go(func() { next, refreshErr = s.Refresh(ctx, a.RefreshToken, uuid.NewString()) })
		wg.Go(func() { logoutErr = s.Logout(ctx, a.Token) })
		wg.Wait()
		if logoutErr != nil {
			t.Fatal(logoutErr)
		}
		if refreshErr == nil {
			_, e = s.Resolve(ctx, next.Token)
			assertCode(t, e, "UNAUTHORIZED")
			_, e = s.Refresh(ctx, next.RefreshToken, uuid.NewString())
			assertCode(t, e, "UNAUTHORIZED")
		}
		_, e = s.Resolve(ctx, a.Token)
		assertCode(t, e, "UNAUTHORIZED")
	}
}

func TestIntegrationD6RefreshDifferentRequests(t *testing.T) {
	s := jwtService(t)
	ctx := context.Background()
	a, e := s.DemoLogin(ctx, 1001)
	if e != nil {
		t.Fatal(e)
	}
	type result struct {
		out Session
		err error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { v, e := s.Refresh(ctx, a.RefreshToken, uuid.NewString()); results <- result{v, e} })
	}
	wg.Wait()
	close(results)
	success := 0
	for r := range results {
		if r.err == nil {
			success++
			_, e = s.Resolve(ctx, r.out.Token)
			assertCode(t, e, "UNAUTHORIZED")
		} else {
			assertCode(t, r.err, "REFRESH_REUSED")
		}
	}
	if success != 1 {
		t.Fatal("rotation race winners", success)
	}
}

func TestIntegrationD6DependencySemantics(t *testing.T) {
	s := jwtService(t)
	ctx := context.Background()
	a, e := s.DemoLogin(ctx, 1001)
	if e != nil {
		t.Fatal(e)
	}
	broken := *s
	broken.Redis = redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1, DialTimeout: 50 * time.Millisecond})
	defer broken.Redis.Close()
	_, e = broken.Resolve(ctx, a.Token)
	assertCode(t, e, "DEPENDENCY_UNAVAILABLE")
	_, e = broken.Refresh(ctx, a.RefreshToken, uuid.NewString())
	assertCode(t, e, "DEPENDENCY_UNAVAILABLE")
	e = broken.Logout(ctx, a.Token)
	assertCode(t, e, "DEPENDENCY_UNAVAILABLE")
	_, e = s.Resolve(ctx, a.Token)
	if e != nil {
		t.Fatal("dependency failure changed credential", e)
	}
}
