package auth

import (
	"context"
	"net/http"
	"time"
)

// RequireUser middleware validates the session cookie, loads the user from the
// store, and stores both in the request context.
// Redirects to /login on missing, expired, or invalid sessions.
// Redirects to /login if the user account is disabled or not found.
func RequireUser(st Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r2, ok := loadSession(st, w, r)
			if !ok {
				return
			}
			next.ServeHTTP(w, r2)
		})
	}
}

// RequireAdmin middleware extends RequireUser by additionally rejecting
// requests from non-admin users with 403.
func RequireAdmin(st Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r2, ok := loadSession(st, w, r)
			if !ok {
				return
			}
			user := r2.Context().Value(ctxUser).(User)
			if user.Role != "admin" {
				http.Error(w, "403 forbidden: admin required", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r2)
		})
	}
}

// loadSession is the shared session-loading logic used by RequireUser and RequireAdmin.
// Returns (updated request with session+user in ctx, true) on success, or (nil, false)
// after writing the appropriate HTTP response.
func loadSession(st Store, w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	// If session already loaded by an outer RequireUser, reuse it.
	if _, ok := r.Context().Value(ctxUser).(User); ok {
		return r, true
	}

	c, err := r.Cookie(cookieName)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return nil, false
	}

	sess, found, err := st.GetSession(c.Value)
	if err != nil || !found {
		clearCookie(w)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return nil, false
	}
	if time.Now().After(sess.ExpiresAt) {
		_ = st.DeleteSession(c.Value)
		clearCookie(w)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return nil, false
	}

	user, found, err := st.GetUserByID(sess.UserID)
	if err != nil || !found || user.Status != "active" {
		clearCookie(w)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return nil, false
	}

	ctx := context.WithValue(r.Context(), ctxSession, sess)
	ctx = context.WithValue(ctx, ctxUser, user)
	return r.WithContext(ctx), true
}
