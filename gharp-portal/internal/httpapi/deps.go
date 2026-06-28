package httpapi

import (
	"net/http"
	"time"
)

// Local mirror types match WS-A's store contract (§6). We do NOT import
// internal/store — narrow local interfaces keep build graphs decoupled
// across parallel workstreams.

type User struct {
	ID        int64
	GitHubID  int64
	Login     string
	Role      string // "admin" | "user"
	Status    string // "active" | "disabled" | "invited"
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Slot struct {
	ID           string
	OSUser       string
	DockerHost   string
	Network      string
	BaseURL      string
	InternalAddr string
	CPULimit     string
	MemLimit     string
	Status       string // "free" | "assigned" | "disabled"
	MaxRunners   int
}

type Assignment struct {
	UserID     int64
	SlotID     string
	GharpState string // "stopped" | "starting" | "running" | "error"
	AssignedAt time.Time
}

type Session struct {
	Token     string
	UserID    int64
	CSRF      string
	ExpiresAt time.Time
}

type AuditEntry struct {
	ID      int64
	ActorID int64
	Action  string
	Target  string
	Detail  string
	At      time.Time
}

// SessionStore is the narrow interface for session operations.
// WS-B (auth) provides the real implementation; tests supply a mock.
type SessionStore interface {
	GetSession(token string) (Session, bool, error)
	DeleteSession(token string) error
}

// UserStore is the narrow interface for data operations used by httpapi.
// WS-A (store) provides the real implementation.
// NOTE for WS-A: GetUserByID and ListAuditLog are required here but not in
// the original §6 Store interface — please add them to the concrete implementation.
type UserStore interface {
	GetUserByID(id int64) (User, bool, error)
	ListUsers() ([]User, error)
	InviteUser(login, role string) (User, error)
	SetUserStatus(id int64, status string) error
	GetAssignmentByUser(userID int64) (Assignment, bool, error)
	AssignFreeSlot(userID int64) (Assignment, error)
	AssignSlot(userID int64, slotID string) (Assignment, error)
	ListSlots() ([]Slot, error)
	ListAuditLog(limit int) ([]AuditEntry, error)
	Audit(actorID int64, action, target, detail string) error
}

// LifecycleManager is the narrow interface for starting/stopping a user's gharp.
// WS-D (lifecycle) provides the real implementation.
type LifecycleManager interface {
	Start(userID int64) error
	Stop(userID int64) error
}

// SlotReloader reloads the slot registry from slots.yaml.
// WS-C (slots) provides the real implementation.
type SlotReloader interface {
	Reload() error
}

// Deps wires all dependencies into the router. The lead provides real
// implementations in cmd/portal/main.go; tests supply mocks.
//
// Integration contract for WS-B: RequireUser and RequireAdmin must call
// httpapi.SetUserInContext and httpapi.SetCSRFInContext before invoking
// the next handler, so WS-E's handlers can read them.
type Deps struct {
	// WS-B provides these middleware factories.
	// They must set User and CSRF token in context via SetUserInContext/SetCSRFInContext.
	RequireUser  func(http.Handler) http.Handler
	RequireAdmin func(http.Handler) http.Handler

	Sessions          SessionStore
	Store             UserStore
	Lifecycle         LifecycleManager
	SlotsReloader     SlotReloader
	Proxy             http.Handler // handles /app/{path...} → proxied to user's gharp
	SessionCookieName string       // defaults to "gharp_session" if empty
}

func (d *Deps) cookieName() string {
	if d.SessionCookieName == "" {
		return "gharp_session"
	}
	return d.SessionCookieName
}
