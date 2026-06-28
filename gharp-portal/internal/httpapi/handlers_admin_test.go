package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestAdminDashboard_Renders(t *testing.T) {
	d, _, store, _, _ := testDeps(t)
	store.users = []User{
		{ID: 1, Login: "alice", Role: "admin", Status: "active"},
		{ID: 2, Login: "bob", Role: "user", Status: "invited"},
	}
	store.slots = []Slot{
		{ID: "slot-1", Status: "free", MaxRunners: 4},
	}
	router := NewRouter(d)

	r := httptest.NewRequest(http.MethodGet, "/admin", nil)
	r = r.WithContext(withUserCtx(r.Context(), User{ID: 1, Role: "admin"}, "testcsrf"))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("GET /admin status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "alice") || !strings.Contains(body, "bob") {
		t.Error("admin dashboard should contain user logins")
	}
	if !strings.Contains(body, "slot-1") {
		t.Error("admin dashboard should contain slot IDs")
	}
}

func TestAdminInviteUser(t *testing.T) {
	d, _, store, _, _ := testDeps(t)
	router := NewRouter(d)

	form := url.Values{"login": {"charlie"}, "role": {"user"}, "_csrf": {"testcsrf"}}
	r := httptest.NewRequest(http.MethodPost, "/admin/users", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r = r.WithContext(withUserCtx(r.Context(), User{ID: 1, Role: "admin"}, "testcsrf"))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)

	if w.Code != http.StatusFound && w.Code != http.StatusSeeOther {
		t.Errorf("POST /admin/users status = %d, want redirect", w.Code)
	}
	found := false
	for _, u := range store.users {
		if u.Login == "charlie" {
			found = true
			break
		}
	}
	if !found {
		t.Error("InviteUser should have been called; charlie not in store")
	}
}

func TestAdminInviteUser_MissingCSRF(t *testing.T) {
	d, _, _, _, _ := testDeps(t)
	router := NewRouter(d)

	form := url.Values{"login": {"charlie"}, "role": {"user"}} // no _csrf
	r := httptest.NewRequest(http.MethodPost, "/admin/users", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r = r.WithContext(withUserCtx(r.Context(), User{ID: 1, Role: "admin"}, "testcsrf"))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("POST /admin/users without CSRF token = %d, want 403", w.Code)
	}
}

func TestAdminSetUserStatus(t *testing.T) {
	d, _, store, _, _ := testDeps(t)
	store.users = []User{{ID: 5, Login: "dave", Role: "user", Status: "active"}}
	router := NewRouter(d)

	form := url.Values{"status": {"disabled"}, "_csrf": {"testcsrf"}}
	r := httptest.NewRequest(http.MethodPost, "/admin/users/5/status", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r = r.WithContext(withUserCtx(r.Context(), User{ID: 1, Role: "admin"}, "testcsrf"))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)

	if w.Code != http.StatusFound && w.Code != http.StatusSeeOther && w.Code != http.StatusOK {
		t.Errorf("POST /admin/users/5/status = %d, want redirect or 200", w.Code)
	}
	if store.users[0].Status != "disabled" {
		t.Errorf("user status = %q, want disabled", store.users[0].Status)
	}
}

func TestAdminAssignSlot_Auto(t *testing.T) {
	d, _, store, _, _ := testDeps(t)
	store.slots = []Slot{{ID: "slot-1", Status: "free"}}
	store.users = []User{{ID: 5, Login: "dave", Role: "user", Status: "active"}}
	router := NewRouter(d)

	form := url.Values{"_csrf": {"testcsrf"}} // no explicit slot → auto-assign
	r := httptest.NewRequest(http.MethodPost, "/admin/users/5/assign", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r = r.WithContext(withUserCtx(r.Context(), User{ID: 1, Role: "admin"}, "testcsrf"))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)

	if w.Code != http.StatusFound && w.Code != http.StatusSeeOther && w.Code != http.StatusOK {
		t.Errorf("POST /admin/users/5/assign = %d, want redirect or 200", w.Code)
	}
	if _, ok := store.assignments[5]; !ok {
		t.Error("AssignFreeSlot should have created an assignment for user 5")
	}
}

func TestAdminAssignSlot_Explicit(t *testing.T) {
	d, _, store, _, _ := testDeps(t)
	store.slots = []Slot{{ID: "slot-2", Status: "free"}}
	store.users = []User{{ID: 5, Login: "dave", Role: "user", Status: "active"}}
	router := NewRouter(d)

	form := url.Values{"slot_id": {"slot-2"}, "_csrf": {"testcsrf"}}
	r := httptest.NewRequest(http.MethodPost, "/admin/users/5/assign", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r = r.WithContext(withUserCtx(r.Context(), User{ID: 1, Role: "admin"}, "testcsrf"))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)

	if w.Code != http.StatusFound && w.Code != http.StatusSeeOther && w.Code != http.StatusOK {
		t.Errorf("POST /admin/users/5/assign = %d, want redirect or 200", w.Code)
	}
	if a, ok := store.assignments[5]; !ok || a.SlotID != "slot-2" {
		t.Errorf("explicit assign: assignment.SlotID = %q, want slot-2", store.assignments[5].SlotID)
	}
}

func TestAdminSlotsReload(t *testing.T) {
	d, _, _, _, rl := testDeps(t)
	router := NewRouter(d)

	form := url.Values{"_csrf": {"testcsrf"}}
	r := httptest.NewRequest(http.MethodPost, "/admin/slots/reload", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r = r.WithContext(withUserCtx(r.Context(), User{ID: 1, Role: "admin"}, "testcsrf"))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)

	if w.Code != http.StatusFound && w.Code != http.StatusSeeOther && w.Code != http.StatusOK {
		t.Errorf("POST /admin/slots/reload = %d, want redirect or 200", w.Code)
	}
	if rl.calls != 1 {
		t.Errorf("Reload called %d times, want 1", rl.calls)
	}
}

func TestAdminAuditLog(t *testing.T) {
	d, _, store, _, _ := testDeps(t)
	store.audit = []AuditEntry{
		{ID: 1, ActorID: 1, Action: "user.invite", Target: "bob", Detail: ""},
	}
	router := NewRouter(d)

	r := httptest.NewRequest(http.MethodGet, "/admin/audit", nil)
	r = r.WithContext(withUserCtx(r.Context(), User{ID: 1, Role: "admin"}, "testcsrf"))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("GET /admin/audit = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "user.invite") {
		t.Error("audit page should contain audit action")
	}
}
