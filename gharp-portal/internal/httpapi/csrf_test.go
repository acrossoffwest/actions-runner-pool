package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequireCSRF(t *testing.T) {
	const token = "abc123"
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := requireCSRF(next)

	cases := []struct {
		name       string
		method     string
		header     string
		formField  string
		wantStatus int
	}{
		{
			name:       "GET bypasses CSRF check",
			method:     http.MethodGet,
			wantStatus: http.StatusOK,
		},
		{
			name:       "POST with no token → 403",
			method:     http.MethodPost,
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "POST with wrong header → 403",
			method:     http.MethodPost,
			header:     "wrong",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "POST with correct header → 200",
			method:     http.MethodPost,
			header:     token,
			wantStatus: http.StatusOK,
		},
		{
			name:       "POST with correct form field → 200",
			method:     http.MethodPost,
			formField:  token,
			wantStatus: http.StatusOK,
		},
		{
			name:       "PUT with correct header → 200",
			method:     http.MethodPut,
			header:     token,
			wantStatus: http.StatusOK,
		},
		{
			name:       "DELETE with no token → 403",
			method:     http.MethodDelete,
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body strings.Builder
			if tc.formField != "" {
				body.WriteString("_csrf=" + tc.formField)
			}
			r := httptest.NewRequest(tc.method, "/test", strings.NewReader(body.String()))
			if body.Len() > 0 {
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			}
			if tc.header != "" {
				r.Header.Set(csrfHeader, tc.header)
			}
			// inject CSRF token into context
			ctx := SetCSRFInContext(r.Context(), token)
			r = r.WithContext(ctx)

			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)

			if w.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tc.wantStatus)
			}
		})
	}
}

func TestRequireCSRF_MissingFromSession(t *testing.T) {
	// CSRF absent from context (session has no CSRF) → always 403 on mutations.
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := requireCSRF(next)

	r := httptest.NewRequest(http.MethodPost, "/test", nil)
	r.Header.Set(csrfHeader, "anything") // client sends a token but session has none
	// no SetCSRFInContext → empty CSRF in context

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 when session CSRF is missing", w.Code)
	}
}
