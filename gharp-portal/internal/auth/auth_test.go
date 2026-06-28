package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ── mock store ────────────────────────────────────────────────────────────────

type mockStore struct {
	users    map[int64]User // keyed by portal ID
	sessions map[string]Session

	upsertFunc  func(githubID int64, login string) (User, error)
	inviteFunc  func(login, role string) (User, error)
	getUserFunc func(id int64) (User, bool, error)

	auditLog []string
}

func (m *mockStore) UpsertUserOnLogin(githubID int64, login string) (User, error) {
	if m.upsertFunc != nil {
		return m.upsertFunc(githubID, login)
	}
	for _, u := range m.users {
		if u.GitHubID == githubID || strings.EqualFold(u.Login, login) {
			return u, nil
		}
	}
	return User{}, ErrNotInvited
}

func (m *mockStore) InviteUser(login, role string) (User, error) {
	if m.inviteFunc != nil {
		return m.inviteFunc(login, role)
	}
	u := User{ID: 99, Login: login, Role: role, Status: "invited"}
	if m.users == nil {
		m.users = make(map[int64]User)
	}
	m.users[u.ID] = u
	return u, nil
}

func (m *mockStore) GetUserByID(id int64) (User, bool, error) {
	if m.getUserFunc != nil {
		return m.getUserFunc(id)
	}
	u, ok := m.users[id]
	return u, ok, nil
}

func (m *mockStore) CreateSession(userID int64, ttl time.Duration) (Session, error) {
	tok, _ := randomToken(32)
	csrf, _ := randomToken(16)
	sess := Session{
		Token:     tok,
		UserID:    userID,
		CSRF:      csrf,
		ExpiresAt: time.Now().Add(ttl),
	}
	if m.sessions == nil {
		m.sessions = make(map[string]Session)
	}
	m.sessions[tok] = sess
	return sess, nil
}

func (m *mockStore) GetSession(token string) (Session, bool, error) {
	if m.sessions == nil {
		return Session{}, false, nil
	}
	s, ok := m.sessions[token]
	return s, ok, nil
}

func (m *mockStore) DeleteSession(token string) error {
	if m.sessions != nil {
		delete(m.sessions, token)
	}
	return nil
}

func (m *mockStore) Audit(actorID int64, action, target, detail string) error {
	m.auditLog = append(m.auditLog, action)
	return nil
}

// ── mock GitHub client ────────────────────────────────────────────────────────

type mockGH struct {
	exchangeFunc func(ctx context.Context, code string) (string, error)
	getUserFunc  func(ctx context.Context, token string) (GitHubUser, error)
}

func (m *mockGH) ExchangeCode(ctx context.Context, code string) (string, error) {
	if m.exchangeFunc != nil {
		return m.exchangeFunc(ctx, code)
	}
	return "test-access-token", nil
}

func (m *mockGH) GetUser(ctx context.Context, token string) (GitHubUser, error) {
	if m.getUserFunc != nil {
		return m.getUserFunc(ctx, token)
	}
	return GitHubUser{ID: 1, Login: "alice"}, nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func defaultCfg() Config {
	return Config{
		OAuthClientID:     "test-client",
		OAuthClientSecret: "test-secret",
		BaseURL:           "http://localhost",
		SessionTTL:        7 * 24 * time.Hour,
		Secure:            false,
	}
}

func validUser(id int64) User {
	return User{ID: id, GitHubID: int64(id * 10), Login: "alice", Role: "user", Status: "active"}
}

// ── OAuthStates ───────────────────────────────────────────────────────────────

func TestOAuthStates_ClaimValid(t *testing.T) {
	s := NewOAuthStates()
	s.Put("tok1")
	if !s.Claim("tok1") {
		t.Fatal("expected Claim to succeed for a registered token")
	}
}

func TestOAuthStates_ClaimReplay(t *testing.T) {
	s := NewOAuthStates()
	s.Put("tok2")
	s.Claim("tok2") // first claim consumes
	if s.Claim("tok2") {
		t.Fatal("second Claim must fail (single-use)")
	}
}

func TestOAuthStates_ClaimUnknown(t *testing.T) {
	s := NewOAuthStates()
	if s.Claim("nonexistent") {
		t.Fatal("Claim on unknown token must return false")
	}
}

func TestOAuthStates_ClaimEmpty(t *testing.T) {
	s := NewOAuthStates()
	if s.Claim("") {
		t.Fatal("empty state must be rejected")
	}
}

// ── /auth/start ───────────────────────────────────────────────────────────────

func TestStartHandler_Redirects(t *testing.T) {
	states := NewOAuthStates()
	cfg := defaultCfg()

	req := httptest.NewRequest(http.MethodGet, "/auth/start", nil)
	rec := httptest.NewRecorder()
	StartHandler(cfg, states).ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "github.com/login/oauth/authorize") {
		t.Errorf("redirect should point to GitHub OAuth; got %s", loc)
	}
	if !strings.Contains(loc, "read%3Auser") && !strings.Contains(loc, "read:user") {
		t.Errorf("scope read:user missing from redirect; got %s", loc)
	}
	if !strings.Contains(loc, cfg.OAuthClientID) {
		t.Errorf("client_id missing from redirect; got %s", loc)
	}
}

// ── /auth/callback — state mismatch ──────────────────────────────────────────

func TestCallback_StateMismatch(t *testing.T) {
	states := NewOAuthStates()
	// do NOT register the state we send
	req := httptest.NewRequest(http.MethodGet, "/auth/callback?state=wrong&code=abc", nil)
	rec := httptest.NewRecorder()
	CallbackHandler(defaultCfg(), &mockGH{}, &mockStore{}, states).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("state mismatch must return 400, got %d", rec.Code)
	}
}

func TestCallback_StateReplay(t *testing.T) {
	states := NewOAuthStates()
	states.Put("s1")

	// first callback consumes the state
	user := validUser(1)
	st := &mockStore{
		users:      map[int64]User{1: user},
		sessions:   make(map[string]Session),
		upsertFunc: func(id int64, login string) (User, error) { return user, nil },
	}
	r1 := httptest.NewRequest(http.MethodGet, "/auth/callback?state=s1&code=c", nil)
	CallbackHandler(defaultCfg(), &mockGH{}, st, states).ServeHTTP(httptest.NewRecorder(), r1)

	// second callback with same state must be rejected
	rec2 := httptest.NewRecorder()
	r2 := httptest.NewRequest(http.MethodGet, "/auth/callback?state=s1&code=c", nil)
	CallbackHandler(defaultCfg(), &mockGH{}, st, states).ServeHTTP(rec2, r2)

	if rec2.Code != http.StatusBadRequest {
		t.Errorf("replayed state must return 400, got %d", rec2.Code)
	}
}

// ── /auth/callback — happy path ───────────────────────────────────────────────

func TestCallback_HappyPath_User(t *testing.T) {
	states := NewOAuthStates()
	states.Put("valid-state")

	user := validUser(1)
	st := &mockStore{
		users:      map[int64]User{1: user},
		sessions:   make(map[string]Session),
		upsertFunc: func(id int64, login string) (User, error) { return user, nil },
	}
	gh := &mockGH{
		getUserFunc: func(_ context.Context, _ string) (GitHubUser, error) {
			return GitHubUser{ID: 10, Login: "alice"}, nil
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/auth/callback?state=valid-state&code=mycode", nil)
	rec := httptest.NewRecorder()
	CallbackHandler(defaultCfg(), gh, st, states).ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect, got %d: %s", rec.Code, rec.Body.String())
	}
	// Regular user → /app
	if loc := rec.Header().Get("Location"); loc != "/app" {
		t.Errorf("expected redirect to /app, got %s", loc)
	}
	// Session cookie must be set HttpOnly
	var found bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieName {
			found = true
			if !c.HttpOnly {
				t.Error("session cookie must be HttpOnly")
			}
		}
	}
	if !found {
		t.Error("session cookie not set after successful login")
	}
	// Audit entry logged
	if len(st.auditLog) == 0 {
		t.Error("expected audit log entry on login")
	}
}

func TestCallback_HappyPath_Admin(t *testing.T) {
	states := NewOAuthStates()
	states.Put("st")

	admin := User{ID: 1, GitHubID: 10, Login: "boss", Role: "admin", Status: "active"}
	st := &mockStore{
		users:      map[int64]User{1: admin},
		sessions:   make(map[string]Session),
		upsertFunc: func(id int64, login string) (User, error) { return admin, nil },
	}

	req := httptest.NewRequest(http.MethodGet, "/auth/callback?state=st&code=c", nil)
	rec := httptest.NewRecorder()
	CallbackHandler(defaultCfg(), &mockGH{}, st, states).ServeHTTP(rec, req)

	if loc := rec.Header().Get("Location"); loc != "/admin" {
		t.Errorf("admin must redirect to /admin, got %s", loc)
	}
}

// ── /auth/callback — gate: not invited ───────────────────────────────────────

func TestCallback_NotInvited(t *testing.T) {
	states := NewOAuthStates()
	states.Put("st")

	st := &mockStore{
		upsertFunc: func(id int64, login string) (User, error) { return User{}, ErrNotInvited },
	}

	req := httptest.NewRequest(http.MethodGet, "/auth/callback?state=st&code=c", nil)
	rec := httptest.NewRecorder()
	CallbackHandler(defaultCfg(), &mockGH{}, st, states).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("not-invited user must get 403, got %d", rec.Code)
	}
}

// ── /auth/callback — bootstrap admin ─────────────────────────────────────────

func TestIsBootstrapAdmin(t *testing.T) {
	cases := []struct {
		cfg, login string
		want       bool
	}{
		{"superuser", "superuser", true},      // single, exact
		{"Admin", "admin", true},              // case-insensitive
		{"alice,bob,carol", "bob", true},      // middle of a list
		{"alice, bob , carol", "carol", true}, // surrounding spaces trimmed
		{"alice,bob", "dave", false},          // not listed
		{"alice,", "", false},                 // empty entry must not match empty login
		{"", "alice", false},                  // unset → nobody is bootstrap admin
		{"  ", "alice", false},                // whitespace-only → nobody
		{"alice,,bob", "bob", true},           // double comma tolerated
	}
	for _, c := range cases {
		cfg := Config{BootstrapAdminLogin: c.cfg}
		if got := cfg.isBootstrapAdmin(c.login); got != c.want {
			t.Errorf("isBootstrapAdmin(cfg=%q, login=%q) = %v, want %v", c.cfg, c.login, got, c.want)
		}
	}
}

func TestCallback_BootstrapAdmin(t *testing.T) {
	states := NewOAuthStates()
	states.Put("st")

	cfg := defaultCfg()
	cfg.BootstrapAdminLogin = "superuser"

	callCount := 0
	adminUser := User{ID: 99, GitHubID: 77, Login: "superuser", Role: "admin", Status: "active"}
	var inviteLogin, inviteRole string

	st := &mockStore{
		users:    map[int64]User{99: adminUser},
		sessions: make(map[string]Session),
		upsertFunc: func(id int64, login string) (User, error) {
			callCount++
			if callCount == 1 {
				return User{}, ErrNotInvited // not invited yet
			}
			return adminUser, nil // second call after InviteUser
		},
		inviteFunc: func(login, role string) (User, error) {
			inviteLogin = login
			inviteRole = role
			return User{Login: login, Role: role, Status: "invited"}, nil
		},
	}
	gh := &mockGH{
		getUserFunc: func(_ context.Context, _ string) (GitHubUser, error) {
			return GitHubUser{ID: 77, Login: "superuser"}, nil
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/auth/callback?state=st&code=c", nil)
	rec := httptest.NewRecorder()
	CallbackHandler(cfg, gh, st, states).ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("bootstrap admin must succeed, got %d: %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/admin" {
		t.Errorf("bootstrap admin must redirect to /admin, got %s", loc)
	}
	if inviteLogin != "superuser" {
		t.Errorf("InviteUser must be called with bootstrap login, got %q", inviteLogin)
	}
	if inviteRole != "admin" {
		t.Errorf("InviteUser must use role=admin, got %q", inviteRole)
	}
	if callCount != 2 {
		t.Errorf("UpsertUserOnLogin must be called twice (fail then succeed), got %d", callCount)
	}
}

func TestCallback_BootstrapAdmin_CaseInsensitive(t *testing.T) {
	states := NewOAuthStates()
	states.Put("st")

	cfg := defaultCfg()
	cfg.BootstrapAdminLogin = "Admin" // mixed case

	adminUser := User{ID: 1, GitHubID: 42, Login: "admin", Role: "admin", Status: "active"}
	callCount := 0
	st := &mockStore{
		users:    map[int64]User{1: adminUser},
		sessions: make(map[string]Session),
		upsertFunc: func(id int64, login string) (User, error) {
			callCount++
			if callCount == 1 {
				return User{}, ErrNotInvited
			}
			return adminUser, nil
		},
	}
	gh := &mockGH{
		getUserFunc: func(_ context.Context, _ string) (GitHubUser, error) {
			return GitHubUser{ID: 42, Login: "admin"}, nil // lowercase from GitHub
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/auth/callback?state=st&code=c", nil)
	rec := httptest.NewRecorder()
	CallbackHandler(cfg, gh, st, states).ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("case-insensitive bootstrap admin match must succeed, got %d", rec.Code)
	}
}

func TestCallback_DisabledUser(t *testing.T) {
	states := NewOAuthStates()
	states.Put("st")

	disabled := User{ID: 1, GitHubID: 10, Login: "alice", Role: "user", Status: "disabled"}
	st := &mockStore{
		upsertFunc: func(id int64, login string) (User, error) { return disabled, nil },
	}

	req := httptest.NewRequest(http.MethodGet, "/auth/callback?state=st&code=c", nil)
	rec := httptest.NewRecorder()
	CallbackHandler(defaultCfg(), &mockGH{}, st, states).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("disabled user must get 403, got %d", rec.Code)
	}
}

// ── /auth/callback — session rotation ────────────────────────────────────────

func TestCallback_RotatesExistingSession(t *testing.T) {
	states := NewOAuthStates()
	states.Put("st")

	user := validUser(1)
	st := &mockStore{
		users:      map[int64]User{1: user},
		sessions:   make(map[string]Session),
		upsertFunc: func(id int64, login string) (User, error) { return user, nil },
	}
	// pre-seed an old session
	oldToken := "old-session-token"
	st.sessions[oldToken] = Session{Token: oldToken, UserID: 1, ExpiresAt: time.Now().Add(time.Hour)}

	req := httptest.NewRequest(http.MethodGet, "/auth/callback?state=st&code=c", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: oldToken})
	rec := httptest.NewRecorder()
	CallbackHandler(defaultCfg(), &mockGH{}, st, states).ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect, got %d", rec.Code)
	}
	if _, ok := st.sessions[oldToken]; ok {
		t.Error("old session must be deleted on login (rotation)")
	}
}

// ── /auth/logout ──────────────────────────────────────────────────────────────

func TestLogout_DeletesSession(t *testing.T) {
	user := validUser(1)
	st := &mockStore{
		users:    map[int64]User{1: user},
		sessions: make(map[string]Session),
	}
	// create a session
	sess, _ := st.CreateSession(1, time.Hour)

	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: sess.Token})
	rec := httptest.NewRecorder()
	LogoutHandler(st).ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("logout must redirect, got %d", rec.Code)
	}
	if _, ok := st.sessions[sess.Token]; ok {
		t.Error("session must be deleted on logout")
	}
	// Cookie must be cleared (MaxAge=-1)
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieName && c.MaxAge >= 0 {
			t.Errorf("cookie must be cleared (MaxAge=-1), got MaxAge=%d", c.MaxAge)
		}
	}
	if len(st.auditLog) == 0 {
		t.Error("expected audit log entry on logout")
	}
}

func TestLogout_MethodNotAllowed(t *testing.T) {
	st := &mockStore{}
	req := httptest.NewRequest(http.MethodGet, "/auth/logout", nil)
	rec := httptest.NewRecorder()
	LogoutHandler(st).ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /auth/logout must return 405, got %d", rec.Code)
	}
}

// ── RequireUser middleware ────────────────────────────────────────────────────

func TestRequireUser_NoSession(t *testing.T) {
	st := &mockStore{}
	req := httptest.NewRequest(http.MethodGet, "/app", nil)
	rec := httptest.NewRecorder()
	RequireUser(st)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("no cookie must redirect to /login, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login" {
		t.Errorf("expected redirect to /login, got %s", loc)
	}
}

func TestRequireUser_ValidSession(t *testing.T) {
	user := validUser(1)
	st := &mockStore{
		users:    map[int64]User{1: user},
		sessions: make(map[string]Session),
	}
	sess, _ := st.CreateSession(1, time.Hour)

	req := httptest.NewRequest(http.MethodGet, "/app", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: sess.Token})
	rec := httptest.NewRecorder()

	var gotUser User
	RequireUser(st)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, _ = UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("valid session must reach handler, got %d", rec.Code)
	}
	if gotUser.ID != user.ID {
		t.Errorf("user in context must match, got %v", gotUser)
	}
}

func TestRequireUser_ExpiredSession(t *testing.T) {
	user := validUser(1)
	st := &mockStore{
		users: map[int64]User{1: user},
		sessions: map[string]Session{
			"expired-tok": {
				Token:     "expired-tok",
				UserID:    1,
				ExpiresAt: time.Now().Add(-time.Minute), // already expired
			},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/app", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "expired-tok"})
	rec := httptest.NewRecorder()
	RequireUser(st)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("expired session must redirect to /login, got %d", rec.Code)
	}
	// Expired session must be deleted
	if _, ok := st.sessions["expired-tok"]; ok {
		t.Error("expired session must be deleted from store")
	}
}

func TestRequireUser_InvalidToken(t *testing.T) {
	st := &mockStore{sessions: make(map[string]Session)}
	req := httptest.NewRequest(http.MethodGet, "/app", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "no-such-token"})
	rec := httptest.NewRecorder()
	RequireUser(st)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("invalid token must redirect to /login, got %d", rec.Code)
	}
}

// ── RequireAdmin middleware ───────────────────────────────────────────────────

func TestRequireAdmin_BlocksUser(t *testing.T) {
	user := validUser(1) // role=user
	st := &mockStore{
		users:    map[int64]User{1: user},
		sessions: make(map[string]Session),
	}
	sess, _ := st.CreateSession(1, time.Hour)

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: sess.Token})
	rec := httptest.NewRecorder()
	RequireAdmin(st)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("non-admin must get 403, got %d", rec.Code)
	}
}

func TestRequireAdmin_AllowsAdmin(t *testing.T) {
	admin := User{ID: 1, GitHubID: 10, Login: "boss", Role: "admin", Status: "active"}
	st := &mockStore{
		users:    map[int64]User{1: admin},
		sessions: make(map[string]Session),
	}
	sess, _ := st.CreateSession(1, time.Hour)

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: sess.Token})
	rec := httptest.NewRecorder()
	RequireAdmin(st)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("admin must reach handler, got %d", rec.Code)
	}
}

func TestRequireAdmin_NoSession(t *testing.T) {
	st := &mockStore{}
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	rec := httptest.NewRecorder()
	RequireAdmin(st)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("no session must redirect to /login, got %d", rec.Code)
	}
}

// ── CSRF middleware ───────────────────────────────────────────────────────────

// loadCtx injects a session+user into a request's context (simulating RequireUser).
func loadCtx(r *http.Request, sess Session, u User) *http.Request {
	ctx := context.WithValue(r.Context(), ctxSession, sess)
	ctx = context.WithValue(ctx, ctxUser, u)
	return r.WithContext(ctx)
}

func TestCSRF_GetPassthrough(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	rec := httptest.NewRecorder()
	CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("GET must pass CSRF middleware, got %d", rec.Code)
	}
}

func TestCSRF_PostWithValidHeader(t *testing.T) {
	sess := Session{Token: "tok", UserID: 1, CSRF: "good-csrf", ExpiresAt: time.Now().Add(time.Hour)}
	user := validUser(1)

	req := httptest.NewRequest(http.MethodPost, "/admin/users", nil)
	req = loadCtx(req, sess, user)
	req.Header.Set(headerCSRF, "good-csrf")

	rec := httptest.NewRecorder()
	CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("valid CSRF header must pass, got %d", rec.Code)
	}
}

func TestCSRF_PostWithValidForm(t *testing.T) {
	sess := Session{Token: "tok", UserID: 1, CSRF: "good-csrf", ExpiresAt: time.Now().Add(time.Hour)}
	user := validUser(1)

	body := strings.NewReader("csrf_token=good-csrf&other=value")
	req := httptest.NewRequest(http.MethodPost, "/admin/users", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = loadCtx(req, sess, user)

	rec := httptest.NewRecorder()
	CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("valid CSRF form field must pass, got %d", rec.Code)
	}
}

func TestCSRF_PostWithWrongToken(t *testing.T) {
	sess := Session{Token: "tok", UserID: 1, CSRF: "correct", ExpiresAt: time.Now().Add(time.Hour)}
	user := validUser(1)

	req := httptest.NewRequest(http.MethodPost, "/admin/users", nil)
	req = loadCtx(req, sess, user)
	req.Header.Set(headerCSRF, "wrong")

	rec := httptest.NewRecorder()
	CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("wrong CSRF token must return 403, got %d", rec.Code)
	}
}

func TestCSRF_PostMissingToken(t *testing.T) {
	sess := Session{Token: "tok", UserID: 1, CSRF: "correct", ExpiresAt: time.Now().Add(time.Hour)}
	user := validUser(1)

	req := httptest.NewRequest(http.MethodPost, "/admin/users", nil)
	req = loadCtx(req, sess, user)
	// no CSRF header or form field

	rec := httptest.NewRecorder()
	CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("missing CSRF token must return 403, got %d", rec.Code)
	}
}

func TestCSRF_PostNoSession(t *testing.T) {
	// No session in context (RequireUser not applied)
	req := httptest.NewRequest(http.MethodPost, "/admin/users", nil)
	req.Header.Set(headerCSRF, "anything")

	rec := httptest.NewRecorder()
	CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("no session in ctx must return 403, got %d", rec.Code)
	}
}

// ── IssueCSRF ─────────────────────────────────────────────────────────────────

func TestIssueCSRF(t *testing.T) {
	sess := Session{CSRF: "my-csrf-token"}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxSession, sess))

	if got := IssueCSRF(req); got != "my-csrf-token" {
		t.Errorf("IssueCSRF must return session CSRF, got %q", got)
	}
}

func TestIssueCSRF_NoSession(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if got := IssueCSRF(req); got != "" {
		t.Errorf("IssueCSRF with no session must return empty, got %q", got)
	}
}

// ── context helpers ───────────────────────────────────────────────────────────

func TestContextHelpers(t *testing.T) {
	u := User{ID: 42, Login: "ctx-user"}
	s := Session{Token: "ctx-tok", CSRF: "ctx-csrf"}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = loadCtx(req, s, u)

	gotUser, ok := UserFromContext(req.Context())
	if !ok || gotUser.ID != 42 {
		t.Errorf("UserFromContext: expected user 42, ok=true; got %v, %v", gotUser, ok)
	}
	gotSess, ok := SessionFromContext(req.Context())
	if !ok || gotSess.Token != "ctx-tok" {
		t.Errorf("SessionFromContext: expected tok ctx-tok; got %v, %v", gotSess, ok)
	}
}
