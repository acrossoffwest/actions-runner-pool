// Package config loads and validates Portal configuration from environment variables.
// Call Load at startup; fatal-exit on validation failure so misconfiguration is
// caught before any network or DB connections are opened.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// C holds validated Portal configuration.
type C struct {
	// Network
	BaseURL  string // public base URL, e.g. https://portal.example.com
	Port     string // listen port, default "8080"
	BindAddr string // interface to bind, default 127.0.0.1 (sit behind a reverse proxy)

	// OAuth (GitHub App for the Portal itself — NOT user runner Apps)
	OAuthClientID     string
	OAuthClientSecret string // never log or render

	// Bootstrap
	BootstrapAdminLogin string // comma-separated GitHub logins promoted to admin on first login

	// Session
	SessionTTL time.Duration // default 7 days

	// Database
	StoreDSN string // SQLite file path or :memory:

	// Slots
	SlotsConfig string // path to slots.yaml
}

// validationError collects all missing/invalid fields before returning.
type validationError struct{ msgs []string }

func (ve *validationError) add(msg string)  { ve.msgs = append(ve.msgs, msg) }
func (ve *validationError) hasErrors() bool { return len(ve.msgs) > 0 }
func (ve *validationError) Error() string   { return "config: " + strings.Join(ve.msgs, "; ") }

// Load reads environment variables and returns a validated Config.
// Any missing required variable or invalid value produces a descriptive error.
func Load() (C, error) {
	ve := &validationError{}
	c := C{}

	// required
	c.BaseURL = requireEnv(ve, "BASE_URL")
	c.OAuthClientID = requireEnv(ve, "PORTAL_OAUTH_CLIENT_ID")
	c.OAuthClientSecret = requireEnv(ve, "PORTAL_OAUTH_CLIENT_SECRET")
	c.StoreDSN = requireEnv(ve, "STORE_DSN")

	// optional with defaults
	c.Port = envOr("PORT", "8080")
	c.BindAddr = envOr("BIND_ADDR", "127.0.0.1")
	c.BootstrapAdminLogin = os.Getenv("BOOTSTRAP_ADMIN_LOGIN")
	c.SlotsConfig = envOr("SLOTS_CONFIG", "provisioner/slots.yaml")

	// SESSION_TTL — default 7 days
	if raw := os.Getenv("SESSION_TTL"); raw != "" {
		d, err := parseDuration(raw)
		if err != nil {
			ve.add(fmt.Sprintf("SESSION_TTL=%q: %v", raw, err))
		} else {
			c.SessionTTL = d
		}
	} else {
		c.SessionTTL = 7 * 24 * time.Hour
	}

	if ve.hasErrors() {
		return C{}, ve
	}
	return c, nil
}

func requireEnv(ve *validationError, key string) string {
	v := os.Getenv(key)
	if v == "" {
		ve.add(fmt.Sprintf("%s is required", key))
	}
	return v
}

func envOr(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

// parseDuration accepts Go duration strings ("7d", "24h", "30m") and also
// plain seconds ("604800").
func parseDuration(s string) (time.Duration, error) {
	// Try Go duration first.
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	// Try days suffix (e.g. "7d").
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err == nil && n > 0 {
			return time.Duration(n) * 24 * time.Hour, nil
		}
	}
	// Try plain integer seconds.
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return time.Duration(n) * time.Second, nil
	}
	return 0, errors.New("unrecognised duration; use Go duration (7h, 30m), days suffix (7d), or integer seconds")
}
