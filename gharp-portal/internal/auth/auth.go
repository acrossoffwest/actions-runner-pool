package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"time"
)

const (
	cookieName = "gharp_session"
	headerCSRF = "X-CSRF-Token"
	fieldCSRF  = "csrf_token"
)

// Config holds auth-package configuration read from environment by WS-A/cmd/portal.
type Config struct {
	OAuthClientID     string
	OAuthClientSecret string
	// BaseURL is the portal's public base URL, e.g. "https://portal.example.com".
	BaseURL string
	// BootstrapAdminLogin is the GitHub login that gets admin on first login,
	// bypassing the invite gate.  Corresponds to env BOOTSTRAP_ADMIN_LOGIN.
	BootstrapAdminLogin string
	// SessionTTL defaults to 7 days.  Corresponds to env SESSION_TTL.
	SessionTTL time.Duration
	// Secure sets the Secure flag on session cookies.  Set false only in dev.
	Secure bool
}

type contextKey int

const (
	ctxSession contextKey = iota
	ctxUser
)

// UserFromContext extracts the authenticated User set by RequireUser middleware.
func UserFromContext(ctx context.Context) (User, bool) {
	u, ok := ctx.Value(ctxUser).(User)
	return u, ok
}

// SessionFromContext extracts the Session set by RequireUser middleware.
func SessionFromContext(ctx context.Context) (Session, bool) {
	s, ok := ctx.Value(ctxSession).(Session)
	return s, ok
}

// randomToken returns a cryptographically random base64url string of n raw bytes.
func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:   cookieName,
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})
}
