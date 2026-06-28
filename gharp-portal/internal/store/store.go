// Package store defines the Portal persistence contract.
// All workstreams (auth, slots, lifecycle, httpapi) code against the Store
// interface; none import the concrete sqlite package directly.
package store

import "time"

// User is a portal account. Status cycles: invited → active → disabled.
type User struct {
	ID        int64
	GitHubID  int64
	Login     string
	Role      string // "admin" | "user"
	Status    string // "active" | "disabled" | "invited"
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Slot is a pre-provisioned kernel-isolated sandbox for one user's gharp instance.
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
	// AdminToken is the gharp ADMIN_TOKEN for this slot's instance, injected
	// server-side by the reverse proxy so users never see it. Carried in the
	// slot registry (slots.yaml); a per-slot secret, not a GitHub App key.
	AdminToken string
}

// Assignment binds one user to one slot (1:1).
type Assignment struct {
	UserID     int64
	SlotID     string
	GharpState string // "stopped" | "starting" | "running" | "error"
	AssignedAt time.Time
}

// Session is a server-side authenticated session identified by an opaque token.
type Session struct {
	Token     string
	UserID    int64
	CSRF      string
	ExpiresAt time.Time
}

// AuditEntry is one row of the audit log.
type AuditEntry struct {
	ID      int64
	ActorID int64
	Action  string
	Target  string
	Detail  string
	At      time.Time
}

// Store is the full persistence interface.
// Consumers should define a narrow local interface with only the methods they
// need rather than depending on this interface directly.
type Store interface {
	// users
	UpsertUserOnLogin(githubID int64, login string) (User, error) // binds invited→active
	GetUserByGitHubID(id int64) (User, bool, error)
	GetUserByID(id int64) (User, bool, error) // by portal DB id (session middleware)
	InviteUser(login, role string) (User, error)
	ListUsers() ([]User, error)
	SetUserStatus(id int64, status string) error
	// slots
	ListSlots() ([]Slot, error)
	GetSlot(id string) (Slot, bool, error)
	UpsertSlot(Slot) error // used by registry loader (WS-C)
	// assignments
	AssignFreeSlot(userID int64) (Assignment, error) // atomically picks a free slot
	AssignSlot(userID int64, slotID string) (Assignment, error)
	GetAssignmentByUser(userID int64) (Assignment, bool, error)
	ListAssignments() ([]Assignment, error) // all assignments (lifecycle health sweep)
	SetGharpState(userID int64, state string) error
	// sessions
	CreateSession(userID int64, ttl time.Duration) (Session, error)
	GetSession(token string) (Session, bool, error)
	DeleteSession(token string) error
	// audit
	Audit(actorID int64, action, target, detail string) error
	ListAuditLog(limit int) ([]AuditEntry, error) // most recent first
}
