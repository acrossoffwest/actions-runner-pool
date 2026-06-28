package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewRouter_Healthz(t *testing.T) {
	d, _, _, _, _ := testDeps(t)
	router := NewRouter(d)

	r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("GET /healthz status = %d, want 200", w.Code)
	}
}

func TestNewRouter_LoginPage(t *testing.T) {
	d, _, _, _, _ := testDeps(t)
	router := NewRouter(d)

	r := httptest.NewRequest(http.MethodGet, "/login", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("GET /login status = %d, want 200", w.Code)
	}
}

func TestNewRouter_RootRedirectsUnauth(t *testing.T) {
	// With no session cookie, GET / should show login or redirect to /login.
	d, _, _, _, _ := testDeps(t)
	// Override RequireUser to NOT inject a user (simulate unauthenticated).
	d.RequireUser = func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// no user in context
			next.ServeHTTP(w, r)
		})
	}
	router := NewRouter(d)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)

	// Should be 200 (login page) or redirect to /login.
	if w.Code != http.StatusOK && w.Code != http.StatusFound && w.Code != http.StatusSeeOther {
		t.Errorf("GET / unauthenticated status = %d, want 200, 302, or 303", w.Code)
	}
}

func TestNewRouter_AppRequiresAuth(t *testing.T) {
	d, _, _, _, _ := testDeps(t)
	// RequireUser denies all — simulates unauthenticated.
	d.RequireUser = denyAll
	router := NewRouter(d)

	r := httptest.NewRequest(http.MethodGet, "/app", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("GET /app unauthenticated status = %d, want 403", w.Code)
	}
}

func TestNewRouter_AdminRequiresAdmin(t *testing.T) {
	d, _, _, _, _ := testDeps(t)
	d.RequireAdmin = denyAll
	router := NewRouter(d)

	r := httptest.NewRequest(http.MethodGet, "/admin", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("GET /admin non-admin status = %d, want 403", w.Code)
	}
}

func TestNewRouter_ProxyRoute(t *testing.T) {
	d, _, _, _, _ := testDeps(t)
	router := NewRouter(d)

	r := httptest.NewRequest(http.MethodGet, "/app/dashboard", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)

	// Mock proxy sets X-Proxied header.
	if w.Header().Get("X-Proxied") != "true" {
		t.Error("GET /app/dashboard should be proxied but X-Proxied header absent")
	}
}

func TestNewRouter_StartStopNotProxied(t *testing.T) {
	d, _, _, _, _ := testDeps(t)
	// Add a user assignment so start/stop handlers work.
	store := d.Store.(*mockStore)
	store.assignments[2] = Assignment{UserID: 2, SlotID: "slot-1", GharpState: "stopped"}

	router := NewRouter(d)

	for _, path := range []string{"/app/start", "/app/stop"} {
		r := httptest.NewRequest(http.MethodPost, path, nil)
		r.Header.Set(csrfHeader, "testcsrf")
		r = r.WithContext(withUserCtx(r.Context(), User{ID: 2, Role: "user", Status: "active"}, "testcsrf"))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)

		// Should NOT have X-Proxied (handled by lifecycle, not proxy).
		if w.Header().Get("X-Proxied") == "true" {
			t.Errorf("POST %s was proxied but should be handled by lifecycle handler", path)
		}
	}
}
