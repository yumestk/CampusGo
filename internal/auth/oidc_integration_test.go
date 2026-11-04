package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
)

func TestIntegrationOIDCSecurity(t *testing.T) {
	s := integrationService(t)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var issuer, mode, nonce, challenge string
	var mu sync.Mutex
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			if mode == "discovery outage" {
				w.WriteHeader(503)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}})
		case "/keys":
			if mode == "keys outage" {
				w.WriteHeader(503)
				return
			}
			json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "test-key", Algorithm: "RS256", Use: "sig"}}})
		case "/token":
			if mode == "token outage" {
				w.WriteHeader(503)
				return
			}
			hash := sha256.Sum256([]byte(r.FormValue("code_verifier")))
			if base64.RawURLEncoding.EncodeToString(hash[:]) != challenge {
				w.WriteHeader(400)
				json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
				return
			}
			claims := map[string]any{"iss": issuer, "sub": "bound-user", "aud": "test-client", "exp": time.Now().Add(time.Minute).Unix(), "iat": time.Now().Unix(), "nonce": nonce}
			private := key
			switch mode {
			case "nonce":
				claims["nonce"] = "wrong"
			case "issuer":
				claims["iss"] = "https://wrong.example"
			case "audience":
				claims["aud"] = "wrong-client"
			case "expired":
				claims["exp"] = time.Now().Add(-time.Minute).Unix()
			case "signature":
				private = other
			case "unbound":
				claims["sub"] = "unbound-user"
			case "authorized party":
				claims["aud"] = []string{"test-client", "other-client"}
				claims["azp"] = "other-client"
			case "access hash":
				claims["at_hash"] = "incorrect"
			}
			signer, e := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: private}, (&jose.SignerOptions{}).WithHeader("kid", "test-key"))
			if e != nil {
				w.WriteHeader(500)
				return
			}
			data, _ := json.Marshal(claims)
			signed, e := signer.Sign(data)
			if e != nil {
				w.WriteHeader(500)
				return
			}
			raw, e := signed.CompactSerialize()
			if e != nil {
				w.WriteHeader(500)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "provider-access", "token_type": "Bearer", "id_token": raw, "expires_in": 60})
		default:
			w.WriteHeader(404)
		}
	}))
	defer provider.Close()
	issuer = provider.URL
	s.Config.Issuer = issuer
	s.Config.ClientID = "test-client"
	s.Config.ClientSecret = "test-secret-long-enough-for-client"
	s.Config.CallbackURL = "http://127.0.0.1/callback"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := s.DB.ExecContext(ctx, "INSERT INTO identity_binding (issuer,subject,user_id) VALUES (?,'bound-user',1001)", issuer); err != nil {
		t.Fatal(err)
	}
	defer s.DB.ExecContext(context.Background(), "DELETE FROM identity_binding WHERE issuer=?", issuer)
	for _, tc := range []struct{ mode, code string }{{"valid", ""}, {"nonce", "UNAUTHORIZED"}, {"issuer", "UNAUTHORIZED"}, {"audience", "UNAUTHORIZED"}, {"expired", "UNAUTHORIZED"}, {"signature", "UNAUTHORIZED"}, {"authorized party", "UNAUTHORIZED"}, {"access hash", "UNAUTHORIZED"}, {"unbound", "IDENTITY_NOT_BOUND"}, {"keys outage", "DEPENDENCY_UNAVAILABLE"}, {"token outage", "DEPENDENCY_UNAVAILABLE"}, {"wrong verifier", "UNAUTHORIZED"}} {
		t.Run(tc.mode, func(t *testing.T) {
			mu.Lock()
			mode = tc.mode
			mu.Unlock()
			location, state, err := s.OIDCStart(ctx)
			if err != nil {
				t.Fatal(err)
			}
			u, err := url.Parse(location)
			if err != nil {
				t.Fatal(err)
			}
			if u.Query().Get("code_challenge_method") != "S256" {
				t.Fatal("PKCE S256 missing")
			}
			mu.Lock()
			nonce = u.Query().Get("nonce")
			challenge = u.Query().Get("code_challenge")
			mu.Unlock()
			if tc.mode == "wrong verifier" {
				data, _ := json.Marshal(loginFlow{Nonce: nonce, Verifier: randomToken()})
				if err := s.Redis.Set(ctx, s.key("oidc", state), data, time.Minute).Err(); err != nil {
					t.Fatal(err)
				}
			}
			_, err = s.OIDCCallback(ctx, state, randomToken(), "code")
			assertCode(t, err, "UNAUTHORIZED")
			session, err := s.OIDCCallback(ctx, state, state, "code")
			if tc.code != "" {
				assertCode(t, err, tc.code)
			} else {
				if err != nil || session.UserID != 1001 {
					t.Fatalf("login result: %v", err)
				}
				if err := s.Logout(ctx, session.Token); err != nil {
					t.Fatal(err)
				}
			}
			_, err = s.OIDCCallback(ctx, state, state, "code")
			assertCode(t, err, "UNAUTHORIZED")
		})
	}
	mu.Lock()
	mode = "discovery outage"
	mu.Unlock()
	_, _, err = s.OIDCStart(ctx)
	assertCode(t, err, "DEPENDENCY_UNAVAILABLE")
}
