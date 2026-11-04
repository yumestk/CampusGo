package config

import "testing"

func TestInvalidConfigurationFailsBeforeStartup(t *testing.T) {
	for _, tc := range []struct {
		name, key, value string
	}{
		{"empty address", "CAMPUSGO_API_ADDR", ""},
		{"missing port", "CAMPUSGO_API_ADDR", "127.0.0.1"},
		{"invalid port", "CAMPUSGO_API_ADDR", "127.0.0.1:65536"},
		{"negative timeout", "CAMPUSGO_SHUTDOWN_TIMEOUT", "-1s"},
		{"invalid timeout", "CAMPUSGO_SHUTDOWN_TIMEOUT", "later"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CAMPUSGO_API_ADDR", "127.0.0.1:18080")
			t.Setenv("CAMPUSGO_SHUTDOWN_TIMEOUT", "10s")
			t.Setenv(tc.key, tc.value)
			if _, err := Load("api"); err == nil {
				t.Fatal("expected invalid configuration to fail")
			}
		})
	}
}
