package auth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/redis/go-redis/v9"
	"github.com/yumestk/CampusGo/internal/platform/fault"
	"golang.org/x/oauth2"
)

type loginFlow struct {
	Verifier string `json:"verifier"`
	Nonce    string `json:"nonce"`
}

// go-oidc deliberately wraps some transport errors without %w. Preserve availability
// evidence separately so a JWKS outage cannot be reported as invalid credentials.
type oidcTransport struct {
	mu          sync.Mutex
	unavailable bool
	secure      bool
}

func (t *oidcTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if t.secure && r.URL.Scheme != "https" {
		return nil, errors.New("OIDC endpoint requires HTTPS")
	}
	response, err := http.DefaultTransport.RoundTrip(r)
	if err != nil || (response != nil && (response.StatusCode >= 500 || response.StatusCode == 429)) {
		t.mu.Lock()
		t.unavailable = true
		t.mu.Unlock()
	}
	return response, err
}
func (t *oidcTransport) failure(err error) error {
	t.mu.Lock()
	unavailable := t.unavailable
	t.mu.Unlock()
	if unavailable {
		return &fault.Error{Code: "DEPENDENCY_UNAVAILABLE", Status: 503, Cause: err}
	}
	return fault.Dependency(err)
}
func (s *Service) provider(ctx context.Context) (*oidc.Provider, oauth2.Config, context.Context, *oidcTransport, error) {
	t := &oidcTransport{secure: s.Config.Environment == "production"}
	client := &http.Client{Timeout: 5 * time.Second, Transport: t}
	ctx = oidc.ClientContext(ctx, client)
	provider, err := oidc.NewProvider(ctx, s.Config.Issuer)
	if err != nil {
		return nil, oauth2.Config{}, ctx, t, t.failure(err)
	}
	c := oauth2.Config{ClientID: s.Config.ClientID, ClientSecret: s.Config.ClientSecret, RedirectURL: s.Config.CallbackURL, Endpoint: provider.Endpoint(), Scopes: []string{oidc.ScopeOpenID, "profile", "email"}}
	return provider, c, ctx, t, nil
}
func (s *Service) OIDCStart(ctx context.Context) (location, state string, err error) {
	if s.Config.Issuer == "" {
		return "", "", fault.New("OIDC_DISABLED", 404)
	}
	_, config, _, _, err := s.provider(ctx)
	if err != nil {
		return "", "", err
	}
	state = randomToken()
	flow := loginFlow{Verifier: oauth2.GenerateVerifier(), Nonce: randomToken()}
	data, err := json.Marshal(flow)
	if err != nil {
		return "", "", fault.Dependency(err)
	}
	ok, err := s.Redis.SetNX(ctx, s.key("oidc", state), data, 5*time.Minute).Result()
	if err != nil {
		return "", "", fault.Dependency(err)
	}
	if !ok {
		return "", "", fault.Dependency(errors.New("OIDC state collision"))
	}
	return config.AuthCodeURL(state, oauth2.S256ChallengeOption(flow.Verifier), oidc.Nonce(flow.Nonce)), state, nil
}
func (s *Service) OIDCCallback(ctx context.Context, state, cookie, code string) (Session, error) {
	if s.Config.Issuer == "" || !ValidToken(state) || !ValidToken(cookie) || subtle.ConstantTimeCompare([]byte(state), []byte(cookie)) != 1 || code == "" || len(code) > 4096 {
		return Session{}, fault.New("UNAUTHORIZED", 401)
	}
	data, err := s.Redis.GetDel(ctx, s.key("oidc", state)).Bytes()
	if errors.Is(err, redis.Nil) {
		return Session{}, fault.New("UNAUTHORIZED", 401)
	}
	if err != nil {
		return Session{}, fault.Dependency(err)
	}
	var flow loginFlow
	if err := json.Unmarshal(data, &flow); err != nil || !ValidToken(flow.Nonce) || flow.Verifier == "" {
		return Session{}, fault.Dependency(errors.New("corrupt OIDC flow"))
	}
	provider, config, oidcCtx, transport, err := s.provider(ctx)
	if err != nil {
		return Session{}, err
	}
	token, err := config.Exchange(oidcCtx, code, oauth2.VerifierOption(flow.Verifier))
	if err != nil {
		var rejected *oauth2.RetrieveError
		if errors.As(err, &rejected) && (rejected.ErrorCode == "invalid_grant" || rejected.ErrorCode == "access_denied") {
			return Session{}, fault.New("UNAUTHORIZED", 401)
		}
		return Session{}, transport.failure(err)
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok {
		return Session{}, fault.New("UNAUTHORIZED", 401)
	}
	// Remote key retrieval uses a bounded client without inheriting a completed request.
	verifyCtx := context.WithoutCancel(oidcCtx)
	verified, err := provider.VerifierContext(verifyCtx, &oidc.Config{ClientID: s.Config.ClientID}).Verify(oidcCtx, raw)
	if err != nil {
		transport.mu.Lock()
		unavailable := transport.unavailable
		transport.mu.Unlock()
		if unavailable {
			return Session{}, transport.failure(err)
		}
		return Session{}, fault.New("UNAUTHORIZED", 401)
	}
	if verified.Subject == "" || subtle.ConstantTimeCompare([]byte(verified.Nonce), []byte(flow.Nonce)) != 1 {
		return Session{}, fault.New("UNAUTHORIZED", 401)
	}
	var claims struct {
		AuthorizedParty string `json:"azp"`
	}
	if err := verified.Claims(&claims); err != nil {
		return Session{}, fault.New("UNAUTHORIZED", 401)
	}
	if (len(verified.Audience) > 1 && claims.AuthorizedParty != s.Config.ClientID) || (claims.AuthorizedParty != "" && claims.AuthorizedParty != s.Config.ClientID) {
		return Session{}, fault.New("UNAUTHORIZED", 401)
	}
	if verified.AccessTokenHash != "" {
		if err := verified.VerifyAccessToken(token.AccessToken); err != nil {
			return Session{}, fault.New("UNAUTHORIZED", 401)
		}
	}
	return s.verifiedLogin(ctx, verified.Issuer, verified.Subject)
}
