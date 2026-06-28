package httpapi

import "net/http"

const (
	csrfHeader = "X-CSRF-Token"
	csrfField  = "_csrf"
)

// requireCSRF validates the CSRF token for state-changing methods.
// It reads the expected token from context (set by WS-B's session middleware)
// and compares it to the value sent by the client in either the X-CSRF-Token
// header or the _csrf form field. Returns 403 on mismatch.
func requireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
			expected := CSRFFromContext(r.Context())
			if expected == "" {
				http.Error(w, "CSRF token missing from session", http.StatusForbidden)
				return
			}
			got := r.Header.Get(csrfHeader)
			if got == "" {
				if err := r.ParseForm(); err == nil {
					got = r.FormValue(csrfField)
				}
			}
			if got != expected {
				http.Error(w, "CSRF token invalid", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
