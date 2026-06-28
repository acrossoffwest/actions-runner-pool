package auth

import (
	"errors"
	"time"
)

// ErrNotInvited is returned by UpsertUserOnLogin when the user has no allowlist entry.
var ErrNotInvited = errors.New("auth: user not invited")

// User is a local mirror of the portal user record.
// Contains only the fields the auth package needs.
type User struct {
	ID       int64
	GitHubID int64
	Login    string
	Role     string
	Status   string
}

// Session is a local mirror of the portal session record.
type Session struct {
	Token     string
	UserID    int64
	CSRF      string
	ExpiresAt time.Time
}

// Store is the narrow store interface for the auth package.
// WS-E wires this up via an adapter over internal/store (WS-A).
// Only the methods needed here are listed; see spec §6 for the full Store interface.
type Store interface {
	// UpsertUserOnLogin looks up by githubID; if not found by ID, looks for an
	// invited row matching login (case-insensitive), binds the ID, sets status
	// active, and returns.  If neither found, returns ErrNotInvited.
	// Bootstrap admin creation is handled by the caller (auth package) via InviteUser.
	UpsertUserOnLogin(githubID int64, login string) (User, error)

	// InviteUser creates an allowlist entry (status=invited) for the given login+role.
	// Used only for bootstrap admin creation on first login.
	InviteUser(login, role string) (User, error)

	// GetUserByID retrieves a user by their portal DB id (used in session middleware).
	GetUserByID(id int64) (User, bool, error)

	CreateSession(userID int64, ttl time.Duration) (Session, error)
	GetSession(token string) (Session, bool, error)
	DeleteSession(token string) error
	Audit(actorID int64, action, target, detail string) error
}
