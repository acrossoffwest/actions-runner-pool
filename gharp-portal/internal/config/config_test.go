package config_test

import (
	"testing"
	"time"

	"github.com/gharp/portal/internal/config"
)

func setEnv(t *testing.T, pairs ...string) {
	t.Helper()
	for i := 0; i < len(pairs); i += 2 {
		t.Setenv(pairs[i], pairs[i+1])
	}
}

func minimalEnv(t *testing.T) {
	t.Helper()
	setEnv(t,
		"BASE_URL", "https://portal.example.com",
		"PORTAL_OAUTH_CLIENT_ID", "test-client-id",
		"PORTAL_OAUTH_CLIENT_SECRET", "test-client-secret",
		"STORE_DSN", "/tmp/test.db",
	)
}

func TestLoad_Minimal(t *testing.T) {
	minimalEnv(t)
	c, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.BaseURL != "https://portal.example.com" {
		t.Errorf("BaseURL: %q", c.BaseURL)
	}
	if c.Port != "8080" {
		t.Errorf("default Port: %q", c.Port)
	}
	if c.SessionTTL != 7*24*time.Hour {
		t.Errorf("default SessionTTL: %v", c.SessionTTL)
	}
	if c.SlotsConfig != "provisioner/slots.yaml" {
		t.Errorf("default SlotsConfig: %q", c.SlotsConfig)
	}
}

func TestLoad_MissingRequired(t *testing.T) {
	// No env vars at all → should list all required keys.
	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for missing required vars")
	}
	msg := err.Error()
	for _, key := range []string{"BASE_URL", "PORTAL_OAUTH_CLIENT_ID", "PORTAL_OAUTH_CLIENT_SECRET", "STORE_DSN"} {
		if !contains(msg, key) {
			t.Errorf("error missing mention of %q: %s", key, msg)
		}
	}
}

func TestLoad_PortOverride(t *testing.T) {
	minimalEnv(t)
	setEnv(t, "PORT", "9090")
	c, _ := config.Load()
	if c.Port != "9090" {
		t.Errorf("expected port 9090, got %q", c.Port)
	}
}

func TestLoad_BootstrapAdmin(t *testing.T) {
	minimalEnv(t)
	setEnv(t, "BOOTSTRAP_ADMIN_LOGIN", "superadmin")
	c, _ := config.Load()
	if c.BootstrapAdminLogin != "superadmin" {
		t.Errorf("expected superadmin, got %q", c.BootstrapAdminLogin)
	}
}

var sessionTTLCases = []struct {
	name string
	raw  string
	want time.Duration
}{
	{"go_duration_hours", "24h", 24 * time.Hour},
	{"go_duration_minutes", "30m", 30 * time.Minute},
	{"days_suffix", "7d", 7 * 24 * time.Hour},
	{"integer_seconds", "3600", time.Hour},
}

func TestLoad_SessionTTL(t *testing.T) {
	for _, tc := range sessionTTLCases {
		t.Run(tc.name, func(t *testing.T) {
			minimalEnv(t)
			setEnv(t, "SESSION_TTL", tc.raw)
			c, err := config.Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if c.SessionTTL != tc.want {
				t.Errorf("SESSION_TTL=%q: got %v, want %v", tc.raw, c.SessionTTL, tc.want)
			}
		})
	}
}

func TestLoad_SessionTTL_Invalid(t *testing.T) {
	minimalEnv(t)
	setEnv(t, "SESSION_TTL", "notaduration")
	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for invalid SESSION_TTL")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsHelper(s, sub))
}

func containsHelper(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
