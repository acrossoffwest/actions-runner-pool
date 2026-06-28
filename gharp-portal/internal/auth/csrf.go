package auth

import (
	"crypto/subtle"
	"net/http"
)

// CSRFMiddleware verifies a double-submit CSRF token on state-changing requests
// (POST, PUT, PATCH, DELETE).  The expected token is read from the session in
// context (set by RequireUser); the submitted token must appear in the
// X-CSRF-Token request header or the csrf_token form field.
//
// Must be applied after RequireUser so the session is already in context.
func CSRFMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isMutating(r.Method) {
			sess, ok := SessionFromContext(r.Context())
			if !ok {
				http.Error(w, "forbidden: no session", http.StatusForbidden)
				return
			}
			submitted := r.Header.Get(headerCSRF)
			if submitted == "" {
				_ = r.ParseForm()
				submitted = r.FormValue(fieldCSRF)
			}
			if submitted == "" || !csrfEqual(submitted, sess.CSRF) {
				http.Error(w, "forbidden: invalid CSRF token", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// IssueCSRF returns the CSRF token from the active session for use in HTML templates.
// Returns empty string if no session is in context.
func IssueCSRF(r *http.Request) string {
	if sess, ok := SessionFromContext(r.Context()); ok {
		return sess.CSRF
	}
	return ""
}

func isMutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

func csrfEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
