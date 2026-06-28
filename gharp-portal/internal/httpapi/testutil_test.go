package httpapi

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

// ---- mock SessionStore ----

type mockSessions struct {
	mu       sync.Mutex
	sessions map[string]Session
	deleted  []string
}

func newMockSessions(sess ...Session) *mockSessions {
	m := &mockSessions{sessions: make(map[string]Session)}
	for _, s := range sess {
		m.sessions[s.Token] = s
	}
	return m
}

func (m *mockSessions) GetSession(token string) (Session, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[token]
	return s, ok, nil
}

func (m *mockSessions) DeleteSession(token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleted = append(m.deleted, token)
	delete(m.sessions, token)
	return nil
}

// ---- mock UserStore ----

type mockStore struct {
	mu          sync.Mutex
	users       []User
	slots       []Slot
	assignments map[int64]Assignment
	audit       []AuditEntry
	inviteErr   error
	statusErr   error
	assignErr   error
}

func newMockStore() *mockStore {
	return &mockStore{assignments: make(map[int64]Assignment)}
}

func (m *mockStore) GetUserByID(id int64) (User, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.users {
		if u.ID == id {
			return u, true, nil
		}
	}
	return User{}, false, nil
}

func (m *mockStore) ListUsers() ([]User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]User{}, m.users...), nil
}

func (m *mockStore) InviteUser(login, role string) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.inviteErr != nil {
		return User{}, m.inviteErr
	}
	u := User{ID: int64(len(m.users) + 1), Login: login, Role: role, Status: "invited"}
	m.users = append(m.users, u)
	return u, nil
}

func (m *mockStore) SetUserStatus(id int64, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.statusErr != nil {
		return m.statusErr
	}
	for i, u := range m.users {
		if u.ID == id {
			m.users[i].Status = status
			return nil
		}
	}
	return errors.New("user not found")
}

func (m *mockStore) GetAssignmentByUser(userID int64) (Assignment, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.assignments[userID]
	return a, ok, nil
}

func (m *mockStore) AssignFreeSlot(userID int64) (Assignment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.assignErr != nil {
		return Assignment{}, m.assignErr
	}
	for _, sl := range m.slots {
		if sl.Status == "free" {
			a := Assignment{UserID: userID, SlotID: sl.ID, GharpState: "stopped", AssignedAt: time.Now()}
			m.assignments[userID] = a
			return a, nil
		}
	}
	return Assignment{}, errors.New("no free slots")
}

func (m *mockStore) AssignSlot(userID int64, slotID string) (Assignment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.assignErr != nil {
		return Assignment{}, m.assignErr
	}
	a := Assignment{UserID: userID, SlotID: slotID, GharpState: "stopped", AssignedAt: time.Now()}
	m.assignments[userID] = a
	return a, nil
}

func (m *mockStore) ListSlots() ([]Slot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Slot{}, m.slots...), nil
}

func (m *mockStore) ListAuditLog(limit int) ([]AuditEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit > len(m.audit) {
		limit = len(m.audit)
	}
	return append([]AuditEntry{}, m.audit[:limit]...), nil
}

func (m *mockStore) Audit(actorID int64, action, target, detail string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.audit = append(m.audit, AuditEntry{
		ID: int64(len(m.audit) + 1), ActorID: actorID,
		Action: action, Target: target, Detail: detail, At: time.Now(),
	})
	return nil
}

// ---- mock LifecycleManager ----

type mockLifecycle struct {
	mu       sync.Mutex
	started  []int64
	stopped  []int64
	startErr error
	stopErr  error
}

func (m *mockLifecycle) Start(userID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.startErr != nil {
		return m.startErr
	}
	m.started = append(m.started, userID)
	return nil
}

func (m *mockLifecycle) Stop(userID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopErr != nil {
		return m.stopErr
	}
	m.stopped = append(m.stopped, userID)
	return nil
}

// ---- mock SlotReloader ----

type mockReloader struct {
	mu        sync.Mutex
	calls     int
	reloadErr error
}

func (m *mockReloader) Reload() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.reloadErr != nil {
		return m.reloadErr
	}
	m.calls++
	return nil
}

// ---- middleware helpers for tests ----

// injectUser returns a middleware that sets a user and CSRF in context.
// Used in tests to simulate WS-B's RequireUser/RequireAdmin middleware.
func injectUser(u User, csrf string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := SetUserInContext(r.Context(), u)
			ctx = SetCSRFInContext(ctx, csrf)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// denyAll returns a middleware that always responds 403.
// Used to test that protected routes are correctly gated.
func denyAll(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	})
}

// redirectToLogin returns a middleware that redirects unauthenticated requests.
// Used to simulate the redirect behavior of WS-B's RequireUser.
func redirectToLogin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, ok := UserFromContext(r.Context())
		if !ok {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withUserCtx sets user/CSRF directly in context for testing handler logic.
func withUserCtx(ctx context.Context, u User, csrf string) context.Context {
	ctx = SetUserInContext(ctx, u)
	return SetCSRFInContext(ctx, csrf)
}

func testDeps(t interface {
	Helper()
	Cleanup(func())
}) (Deps, *mockSessions, *mockStore, *mockLifecycle, *mockReloader) {
	sess := newMockSessions()
	store := newMockStore()
	lc := &mockLifecycle{}
	rl := &mockReloader{}

	proxy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Proxied", "true")
		w.WriteHeader(http.StatusOK)
	})

	adminUser := User{ID: 1, Login: "admin", Role: "admin", Status: "active"}
	regularUser := User{ID: 2, Login: "user", Role: "user", Status: "active"}
	_ = regularUser

	d := Deps{
		RequireUser:   injectUser(regularUser, "testcsrf"),
		RequireAdmin:  injectUser(adminUser, "testcsrf"),
		Sessions:      sess,
		Store:         store,
		Lifecycle:     lc,
		SlotsReloader: rl,
		Proxy:         proxy,
	}
	return d, sess, store, lc, rl
}
