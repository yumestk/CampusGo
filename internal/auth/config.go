package auth

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Environment, Namespace                                      string
	Demo                                                        bool
	Mode, JWTSecret, GatewaySecret                              string
	RequireGateway                                              bool
	RefreshTTL                                                  time.Duration
	SessionTTL                                                  time.Duration
	Issuer, ClientID, ClientSecret, CallbackURL, FrontendOrigin string
}

func LoadConfig() (Config, error) {
	c := Config{Environment: os.Getenv("CAMPUSGO_ENV"), Namespace: os.Getenv("CAMPUSGO_SESSION_NAMESPACE"), SessionTTL: 2 * time.Hour,
		Issuer: os.Getenv("CAMPUSGO_OIDC_ISSUER"), ClientID: os.Getenv("CAMPUSGO_OIDC_CLIENT_ID"), ClientSecret: os.Getenv("CAMPUSGO_OIDC_CLIENT_SECRET"), CallbackURL: os.Getenv("CAMPUSGO_OIDC_CALLBACK_URL"), FrontendOrigin: os.Getenv("CAMPUSGO_FRONTEND_ORIGIN")}
	if c.Environment == "" {
		c.Environment = "local"
	}
	if c.Namespace == "" {
		c.Namespace = "v1"
	}
	c.Mode = os.Getenv("CAMPUSGO_AUTH_MODE")
	if c.Mode == "" {
		c.Mode = "session"
	}
	if c.Mode != "session" && c.Mode != "jwt" {
		return c, fmt.Errorf("invalid auth mode")
	}
	c.JWTSecret = os.Getenv("CAMPUSGO_JWT_SECRET")
	if c.Mode == "jwt" && len(c.JWTSecret) < 32 {
		return c, fmt.Errorf("JWT secret must contain at least 32 bytes")
	}
	c.RefreshTTL = 7 * 24 * time.Hour
	if v := os.Getenv("CAMPUSGO_REFRESH_TTL"); v != "" {
		d, e := time.ParseDuration(v)
		if e != nil || d < time.Minute || d > 30*24*time.Hour {
			return c, fmt.Errorf("invalid refresh TTL")
		}
		c.RefreshTTL = d
	}
	c.GatewaySecret = os.Getenv("CAMPUSGO_GATEWAY_SECRET")
	c.RequireGateway = c.Environment == "production"
	if v := os.Getenv("CAMPUSGO_REQUIRE_GATEWAY"); v != "" {
		b, e := strconv.ParseBool(v)
		if e != nil {
			return c, e
		}
		c.RequireGateway = b
	}
	if c.Environment == "production" && !c.RequireGateway {
		return c, fmt.Errorf("production requires gateway boundary")
	}
	if c.RequireGateway && len(c.GatewaySecret) < 32 {
		return c, fmt.Errorf("gateway secret must contain at least 32 bytes")
	}
	var err error
	if value, ok := os.LookupEnv("CAMPUSGO_DEMO_LOGIN"); ok {
		c.Demo, err = strconv.ParseBool(value)
		if err != nil {
			return c, fmt.Errorf("invalid CAMPUSGO_DEMO_LOGIN")
		}
	}
	if value, ok := os.LookupEnv("CAMPUSGO_SESSION_TTL"); ok {
		c.SessionTTL, err = time.ParseDuration(value)
		if err != nil || c.SessionTTL < time.Second || c.SessionTTL > 24*time.Hour {
			return c, fmt.Errorf("session TTL must be between 1s and 24h")
		}
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9_-]{1,48}$`).MatchString(c.Namespace) {
		return c, fmt.Errorf("invalid session namespace")
	}
	if c.Environment != "local" && c.Environment != "test" && c.Environment != "production" {
		return c, fmt.Errorf("unknown CAMPUSGO_ENV")
	}
	if c.Demo && c.Environment != "local" && c.Environment != "test" {
		return c, fmt.Errorf("demo login is restricted to local/test")
	}
	if c.Issuer == "" {
		if c.Environment == "production" {
			return c, fmt.Errorf("production requires OIDC")
		}
		return c, nil
	}
	if c.ClientID == "" || len(c.ClientSecret) < 24 {
		return c, fmt.Errorf("OIDC client ID and secret are required")
	}
	for _, raw := range []string{c.Issuer, c.CallbackURL, c.FrontendOrigin} {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return c, fmt.Errorf("invalid OIDC URL")
		}
		if u.Scheme != "https" && !(c.Environment != "production" && u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost")) {
			return c, fmt.Errorf("OIDC URLs require HTTPS except local loopback")
		}
	}
	u, _ := url.Parse(c.FrontendOrigin)
	if u.Path != "" && u.Path != "/" {
		return c, fmt.Errorf("frontend origin must not contain a path")
	}
	c.FrontendOrigin = strings.TrimSuffix(c.FrontendOrigin, "/")
	u, _ = url.Parse(c.CallbackURL)
	if u.Path != "/api/auth/oidc/callback" {
		return c, fmt.Errorf("invalid callback path")
	}
	return c, nil
}
