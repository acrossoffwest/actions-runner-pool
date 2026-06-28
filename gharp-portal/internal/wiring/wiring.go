// Package wiring assembles the concrete *store.DB into the narrow interfaces
// each component (auth, httpapi, lifecycle, proxy, slots) requires, and bridges
// the auth middleware's context into the httpapi/proxy contexts. It is the only
// place that imports every component, keeping the components themselves
// decoupled (Go interface segregation).
package wiring

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gharp/portal/internal/auth"
	"github.com/gharp/portal/internal/config"
	"github.com/gharp/portal/internal/httpapi"
	"github.com/gharp/portal/internal/lifecycle"
	"github.com/gharp/portal/internal/proxy"
	"github.com/gharp/portal/internal/slots"
	"github.com/gharp/portal/internal/store"
)

// Assemble wires every component over db and returns the root HTTP handler plus
// the lifecycle manager (so the caller can run a periodic health sweep).
func Assemble(cfg config.C, db *store.DB) (http.Handler, *lifecycle.Manager) {
	secure := len(cfg.BaseURL) >= 8 && cfg.BaseURL[:8] == "https://"

	authCfg := auth.Config{
		OAuthClientID:       cfg.OAuthClientID,
		OAuthClientSecret:   cfg.OAuthClientSecret,
		BaseURL:             cfg.BaseURL,
		BootstrapAdminLogin: cfg.BootstrapAdminLogin,
		SessionTTL:          cfg.SessionTTL,
		Secure:              secure,
	}

	aStore := authStore{db}
	states := auth.NewOAuthStates()
	gh := auth.NewHTTPGitHubClient(cfg.OAuthClientID, cfg.OAuthClientSecret)

	mgr := lifecycle.New(lifecycleStore{db}, lifecycle.OSCommandRunner{}, &http.Client{Timeout: 10 * time.Second})
	proxyHandler := proxy.New(proxyStore{db})

	rel := &reloader{path: cfg.SlotsConfig, st: slotsStore{db}}
	// Best-effort initial registry load; absent slots.yaml is non-fatal (an
	// admin can add slots and reload later).
	if err := rel.Reload(); err != nil {
		log.Printf("wiring: initial slot registry load (%s): %v", cfg.SlotsConfig, err)
	}

	deps := httpapi.Deps{
		RequireUser:   bridge(auth.RequireUser(aStore)),
		RequireAdmin:  bridge(auth.RequireAdmin(aStore)),
		Sessions:      httpStore{db},
		Store:         httpStore{db},
		Lifecycle:     lifecycleAdapter{mgr},
		SlotsReloader: rel,
		Proxy:         proxyHandler,
	}

	router := httpapi.NewRouter(deps)

	// Top-level mux mounts the OAuth endpoints WS-B owns, then delegates the
	// rest to the httpapi router (which owns "/", /login, /app/*, /admin/*).
	root := http.NewServeMux()
	root.Handle("GET /auth/start", auth.StartHandler(authCfg, states))
	root.Handle("GET /auth/callback", auth.CallbackHandler(authCfg, gh, aStore, states))
	root.Handle("/", router)

	return root, mgr
}

// bridge wraps an auth middleware so that, after authentication, the auth.User
// and Session in the request context are re-published into the httpapi context
// (for WS-E handlers) and the proxy context (for WS-D's reverse proxy).
func bridge(authMW func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		translate := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, ok := auth.UserFromContext(r.Context())
			if !ok {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			s, _ := auth.SessionFromContext(r.Context())
			ctx := httpapi.SetUserInContext(r.Context(), httpapi.User{
				ID:       u.ID,
				GitHubID: u.GitHubID,
				Login:    u.Login,
				Role:     u.Role,
				Status:   u.Status,
			})
			ctx = httpapi.SetCSRFInContext(ctx, s.CSRF)
			ctx = proxy.WithUserID(ctx, u.ID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
		return authMW(translate)
	}
}

// lifecycleAdapter adapts *lifecycle.Manager (whose Start/Stop take a context)
// to httpapi.LifecycleManager (context-free).
type lifecycleAdapter struct{ mgr *lifecycle.Manager }

func (l lifecycleAdapter) Start(userID int64) error {
	return l.mgr.Start(context.Background(), userID)
}
func (l lifecycleAdapter) Stop(userID int64) error {
	return l.mgr.Stop(context.Background(), userID)
}

// reloader implements httpapi.SlotReloader by re-reading slots.yaml.
type reloader struct {
	path string
	st   slots.Store
}

func (r *reloader) Reload() error {
	f, err := os.Open(r.path)
	if err != nil {
		return err
	}
	defer f.Close()
	return slots.LoadRegistry(f, r.st)
}

// --- store adapters: one wrapper per consumer, converting store.* <-> local types ---

type authStore struct{ db *store.DB }

func (a authStore) UpsertUserOnLogin(githubID int64, login string) (auth.User, error) {
	u, err := a.db.UpsertUserOnLogin(githubID, login)
	if err != nil {
		// Preserve the auth package's not-invited sentinel.
		if err == store.ErrNotAllowed {
			return auth.User{}, auth.ErrNotInvited
		}
		return auth.User{}, err
	}
	return toAuthUser(u), nil
}
func (a authStore) InviteUser(login, role string) (auth.User, error) {
	u, err := a.db.InviteUser(login, role)
	return toAuthUser(u), err
}
func (a authStore) GetUserByID(id int64) (auth.User, bool, error) {
	u, ok, err := a.db.GetUserByID(id)
	return toAuthUser(u), ok, err
}
func (a authStore) CreateSession(userID int64, ttl time.Duration) (auth.Session, error) {
	s, err := a.db.CreateSession(userID, ttl)
	return toAuthSession(s), err
}
func (a authStore) GetSession(token string) (auth.Session, bool, error) {
	s, ok, err := a.db.GetSession(token)
	return toAuthSession(s), ok, err
}
func (a authStore) DeleteSession(token string) error { return a.db.DeleteSession(token) }
func (a authStore) Audit(actorID int64, action, target, detail string) error {
	return a.db.Audit(actorID, action, target, detail)
}

type httpStore struct{ db *store.DB }

func (h httpStore) GetUserByID(id int64) (httpapi.User, bool, error) {
	u, ok, err := h.db.GetUserByID(id)
	return toHTTPUser(u), ok, err
}
func (h httpStore) ListUsers() ([]httpapi.User, error) {
	us, err := h.db.ListUsers()
	out := make([]httpapi.User, len(us))
	for i, u := range us {
		out[i] = toHTTPUser(u)
	}
	return out, err
}
func (h httpStore) InviteUser(login, role string) (httpapi.User, error) {
	u, err := h.db.InviteUser(login, role)
	return toHTTPUser(u), err
}
func (h httpStore) SetUserStatus(id int64, status string) error {
	return h.db.SetUserStatus(id, status)
}
func (h httpStore) GetAssignmentByUser(userID int64) (httpapi.Assignment, bool, error) {
	a, ok, err := h.db.GetAssignmentByUser(userID)
	return toHTTPAssignment(a), ok, err
}
func (h httpStore) AssignFreeSlot(userID int64) (httpapi.Assignment, error) {
	a, err := h.db.AssignFreeSlot(userID)
	return toHTTPAssignment(a), err
}
func (h httpStore) AssignSlot(userID int64, slotID string) (httpapi.Assignment, error) {
	a, err := h.db.AssignSlot(userID, slotID)
	return toHTTPAssignment(a), err
}
func (h httpStore) ListSlots() ([]httpapi.Slot, error) {
	ss, err := h.db.ListSlots()
	out := make([]httpapi.Slot, len(ss))
	for i, s := range ss {
		out[i] = toHTTPSlot(s)
	}
	return out, err
}
func (h httpStore) ListAuditLog(limit int) ([]httpapi.AuditEntry, error) {
	es, err := h.db.ListAuditLog(limit)
	out := make([]httpapi.AuditEntry, len(es))
	for i, e := range es {
		out[i] = httpapi.AuditEntry{ID: e.ID, ActorID: e.ActorID, Action: e.Action, Target: e.Target, Detail: e.Detail, At: e.At}
	}
	return out, err
}
func (h httpStore) Audit(actorID int64, action, target, detail string) error {
	return h.db.Audit(actorID, action, target, detail)
}
func (h httpStore) GetSession(token string) (httpapi.Session, bool, error) {
	s, ok, err := h.db.GetSession(token)
	return httpapi.Session{Token: s.Token, UserID: s.UserID, CSRF: s.CSRF, ExpiresAt: s.ExpiresAt}, ok, err
}
func (h httpStore) DeleteSession(token string) error { return h.db.DeleteSession(token) }

type lifecycleStore struct{ db *store.DB }

func (l lifecycleStore) GetAssignmentByUser(userID int64) (lifecycle.Assignment, bool, error) {
	a, ok, err := l.db.GetAssignmentByUser(userID)
	return lifecycle.Assignment{UserID: a.UserID, SlotID: a.SlotID, GharpState: a.GharpState, AssignedAt: a.AssignedAt}, ok, err
}
func (l lifecycleStore) GetSlot(id string) (lifecycle.Slot, bool, error) {
	s, ok, err := l.db.GetSlot(id)
	return lifecycle.Slot{ID: s.ID, InternalAddr: s.InternalAddr}, ok, err
}
func (l lifecycleStore) SetGharpState(userID int64, state string) error {
	return l.db.SetGharpState(userID, state)
}
func (l lifecycleStore) ListAssignments() ([]lifecycle.Assignment, error) {
	as, err := l.db.ListAssignments()
	out := make([]lifecycle.Assignment, len(as))
	for i, a := range as {
		out[i] = lifecycle.Assignment{UserID: a.UserID, SlotID: a.SlotID, GharpState: a.GharpState, AssignedAt: a.AssignedAt}
	}
	return out, err
}

type proxyStore struct{ db *store.DB }

func (p proxyStore) GetAssignmentByUser(userID int64) (proxy.Assignment, bool, error) {
	a, ok, err := p.db.GetAssignmentByUser(userID)
	return proxy.Assignment{UserID: a.UserID, SlotID: a.SlotID}, ok, err
}
func (p proxyStore) GetSlot(id string) (proxy.Slot, bool, error) {
	s, ok, err := p.db.GetSlot(id)
	return proxy.Slot{InternalAddr: s.InternalAddr, AdminToken: s.AdminToken}, ok, err
}

type slotsStore struct{ db *store.DB }

func (sl slotsStore) ListSlots() ([]slots.Slot, error) {
	ss, err := sl.db.ListSlots()
	out := make([]slots.Slot, len(ss))
	for i, s := range ss {
		out[i] = toSlotsSlot(s)
	}
	return out, err
}
func (sl slotsStore) GetSlot(id string) (slots.Slot, bool, error) {
	s, ok, err := sl.db.GetSlot(id)
	return toSlotsSlot(s), ok, err
}
func (sl slotsStore) UpsertSlot(s slots.Slot) error {
	return sl.db.UpsertSlot(store.Slot{
		ID: s.ID, OSUser: s.OSUser, DockerHost: s.DockerHost, Network: s.Network,
		BaseURL: s.BaseURL, InternalAddr: s.InternalAddr, CPULimit: s.CPULimit,
		MemLimit: s.MemLimit, MaxRunners: s.MaxRunners, Status: s.Status, AdminToken: s.AdminToken,
	})
}
func (sl slotsStore) AssignFreeSlot(userID int64) (slots.Assignment, error) {
	a, err := sl.db.AssignFreeSlot(userID)
	return slots.Assignment{UserID: a.UserID, SlotID: a.SlotID, GharpState: a.GharpState, AssignedAt: a.AssignedAt}, err
}
func (sl slotsStore) AssignSlot(userID int64, slotID string) (slots.Assignment, error) {
	a, err := sl.db.AssignSlot(userID, slotID)
	return slots.Assignment{UserID: a.UserID, SlotID: a.SlotID, GharpState: a.GharpState, AssignedAt: a.AssignedAt}, err
}

// --- conversion helpers ---

func toAuthUser(u store.User) auth.User {
	return auth.User{ID: u.ID, GitHubID: u.GitHubID, Login: u.Login, Role: u.Role, Status: u.Status}
}
func toAuthSession(s store.Session) auth.Session {
	return auth.Session{Token: s.Token, UserID: s.UserID, CSRF: s.CSRF, ExpiresAt: s.ExpiresAt}
}
func toHTTPUser(u store.User) httpapi.User {
	return httpapi.User{ID: u.ID, GitHubID: u.GitHubID, Login: u.Login, Role: u.Role, Status: u.Status, CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt}
}
func toHTTPSlot(s store.Slot) httpapi.Slot {
	return httpapi.Slot{ID: s.ID, OSUser: s.OSUser, DockerHost: s.DockerHost, Network: s.Network, BaseURL: s.BaseURL, InternalAddr: s.InternalAddr, CPULimit: s.CPULimit, MemLimit: s.MemLimit, Status: s.Status, MaxRunners: s.MaxRunners}
}
func toHTTPAssignment(a store.Assignment) httpapi.Assignment {
	return httpapi.Assignment{UserID: a.UserID, SlotID: a.SlotID, GharpState: a.GharpState, AssignedAt: a.AssignedAt}
}
func toSlotsSlot(s store.Slot) slots.Slot {
	return slots.Slot{ID: s.ID, OSUser: s.OSUser, DockerHost: s.DockerHost, Network: s.Network, BaseURL: s.BaseURL, InternalAddr: s.InternalAddr, CPULimit: s.CPULimit, MemLimit: s.MemLimit, Status: s.Status, MaxRunners: s.MaxRunners, AdminToken: s.AdminToken}
}
