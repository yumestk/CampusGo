package auth

import (
	"errors"
	"testing"

	"github.com/yumestk/CampusGo/internal/platform/fault"
)

func TestConfigurationGuards(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values map[string]string
		valid  bool
	}{
		{"production demo", map[string]string{"CAMPUSGO_ENV": "production", "CAMPUSGO_DEMO_LOGIN": "true"}, false},
		{"production missing issuer", map[string]string{"CAMPUSGO_ENV": "production"}, false},
		{"local demo", map[string]string{"CAMPUSGO_ENV": "local", "CAMPUSGO_DEMO_LOGIN": "true"}, true},
		{"namespace injection", map[string]string{"CAMPUSGO_SESSION_NAMESPACE": "a:b"}, false},
		{"invalid duration", map[string]string{"CAMPUSGO_SESSION_TTL": "0s"}, false},
		{"unbounded duration", map[string]string{"CAMPUSGO_SESSION_TTL": "1000h"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, k := range []string{"CAMPUSGO_ENV", "CAMPUSGO_DEMO_LOGIN", "CAMPUSGO_SESSION_NAMESPACE", "CAMPUSGO_OIDC_ISSUER", "CAMPUSGO_SESSION_TTL"} {
				t.Setenv(k, "")
			}
			t.Setenv("CAMPUSGO_DEMO_LOGIN", "false")
			t.Setenv("CAMPUSGO_SESSION_TTL", "2h")
			for k, v := range tc.values {
				t.Setenv(k, v)
			}
			_, err := LoadConfig()
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}
func assertCode(t *testing.T, err error, code string) {
	t.Helper()
	var f *fault.Error
	if !errors.As(err, &f) || f.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

func TestOIDCConfigurationTrustBoundaries(t *testing.T) {
	base := map[string]string{
		"CAMPUSGO_ENV": "production", "CAMPUSGO_DEMO_LOGIN": "false", "CAMPUSGO_SESSION_NAMESPACE": "live", "CAMPUSGO_SESSION_TTL": "2h",
		"CAMPUSGO_OIDC_ISSUER": "https://identity.example/dex", "CAMPUSGO_OIDC_CLIENT_ID": "campusgo", "CAMPUSGO_OIDC_CLIENT_SECRET": "a-test-secret-at-least-24-characters",
		"CAMPUSGO_OIDC_CALLBACK_URL": "https://api.example/api/auth/oidc/callback", "CAMPUSGO_FRONTEND_ORIGIN": "https://web.example",
	}
	for _, tc := range []struct {
		name, key, value string
		valid            bool
	}{
		{"secure", "", "", true},
		{"issuer plaintext", "CAMPUSGO_OIDC_ISSUER", "http://127.0.0.1/dex", false},
		{"frontend path", "CAMPUSGO_FRONTEND_ORIGIN", "https://web.example/path", false},
		{"callback query", "CAMPUSGO_OIDC_CALLBACK_URL", "https://api.example/api/auth/oidc/callback?next=evil", false},
		{"callback path", "CAMPUSGO_OIDC_CALLBACK_URL", "https://api.example/other", false},
		{"url userinfo", "CAMPUSGO_OIDC_ISSUER", "https://name:password@identity.example", false},
		{"secret missing", "CAMPUSGO_OIDC_CLIENT_SECRET", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range base {
				t.Setenv(k, v)
			}
			if tc.key != "" {
				t.Setenv(tc.key, tc.value)
			}
			_, err := LoadConfig()
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}
