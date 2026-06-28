package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestUserApp_RendersShell(t *testing.T) {
	d, _, store, _, _ := testDeps(t)
	store.assignments[2] = Assignment{UserID: 2, SlotID: "slot-1", GharpState: "running"}
	router := NewRouter(d)

	r := httptest.NewRequest(http.MethodGet, "/app", nil)
	r = r.WithContext(withUserCtx(r.Context(), User{ID: 2, Role: "user", Status: "active"}, "testcsrf"))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("GET /app = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "start") && !strings.Contains(body, "stop") {
		t.Error("user shell should contain start or stop controls")
	}
}

func TestUserApp_NoAssignment(t *testing.T) {
	d, _, _, _, _ := testDeps(t)
	// no assignment for user 2
	router := NewRouter(d)

	r := httptest.NewRequest(http.MethodGet, "/app", nil)
	r = r.WithContext(withUserCtx(r.Context(), User{ID: 2, Role: "user", Status: "active"}, "testcsrf"))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)

	// Should render with "no slot" state — still 200.
	if w.Code != http.StatusOK {
		t.Errorf("GET /app no assignment = %d, want 200", w.Code)
	}
}

func TestUserStart(t *testing.T) {
	d, _, store, lc, _ := testDeps(t)
	store.assignments[2] = Assignment{UserID: 2, SlotID: "slot-1", GharpState: "stopped"}
	router := NewRouter(d)

	form := url.Values{"_csrf": {"testcsrf"}}
	r := httptest.NewRequest(http.MethodPost, "/app/start", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r = r.WithContext(withUserCtx(r.Context(), User{ID: 2, Role: "user", Status: "active"}, "testcsrf"))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)

	if w.Code != http.StatusFound && w.Code != http.StatusSeeOther && w.Code != http.StatusOK {
		t.Errorf("POST /app/start = %d, want redirect or 200", w.Code)
	}
	if len(lc.started) == 0 || lc.started[0] != 2 {
		t.Error("Lifecycle.Start should have been called for user 2")
	}
}

func TestUserStart_MissingCSRF(t *testing.T) {
	d, _, _, _, _ := testDeps(t)
	router := NewRouter(d)

	r := httptest.NewRequest(http.MethodPost, "/app/start", nil)
	// No CSRF header/field.
	r = r.WithContext(withUserCtx(r.Context(), User{ID: 2, Role: "user", Status: "active"}, "testcsrf"))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("POST /app/start without CSRF = %d, want 403", w.Code)
	}
}

func TestUserStop(t *testing.T) {
	d, _, store, lc, _ := testDeps(t)
	store.assignments[2] = Assignment{UserID: 2, SlotID: "slot-1", GharpState: "running"}
	router := NewRouter(d)

	form := url.Values{"_csrf": {"testcsrf"}}
	r := httptest.NewRequest(http.MethodPost, "/app/stop", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r = r.WithContext(withUserCtx(r.Context(), User{ID: 2, Role: "user", Status: "active"}, "testcsrf"))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)

	if w.Code != http.StatusFound && w.Code != http.StatusSeeOther && w.Code != http.StatusOK {
		t.Errorf("POST /app/stop = %d, want redirect or 200", w.Code)
	}
	if len(lc.stopped) == 0 || lc.stopped[0] != 2 {
		t.Error("Lifecycle.Stop should have been called for user 2")
	}
}

func TestProxySubpath(t *testing.T) {
	d, _, _, _, _ := testDeps(t)
	router := NewRouter(d)

	for _, path := range []string{"/app/dashboard", "/app/api/jobs", "/app/stats"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r = r.WithContext(withUserCtx(r.Context(), User{ID: 2, Role: "user", Status: "active"}, "testcsrf"))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)

		if w.Header().Get("X-Proxied") != "true" {
			t.Errorf("GET %s: want X-Proxied=true, got %q", path, w.Header().Get("X-Proxied"))
		}
	}
}

func TestLogout(t *testing.T) {
	sess := newMockSessions(Session{
		Token: "tok123", UserID: 1, CSRF: "csrf1",
	})
	d, _, _, _, _ := testDeps(t)
	d.Sessions = sess
	router := NewRouter(d)

	r := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	r.AddCookie(&http.Cookie{Name: "gharp_session", Value: "tok123"})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)

	if w.Code != http.StatusFound && w.Code != http.StatusSeeOther {
		t.Errorf("POST /auth/logout = %d, want redirect", w.Code)
	}
	if len(sess.deleted) == 0 || sess.deleted[0] != "tok123" {
		t.Error("logout should have deleted the session token")
	}
}
