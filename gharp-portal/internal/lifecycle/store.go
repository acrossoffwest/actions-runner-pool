package lifecycle

import "time"

// Assignment is the local projection of store.Assignment used by this package.
type Assignment struct {
	UserID     int64
	SlotID     string
	GharpState string
	AssignedAt time.Time
}

// Slot is the local projection of store.Slot used by this package.
type Slot struct {
	ID           string
	InternalAddr string
}

// Store is the narrow store interface required by lifecycle.
// WS-A note: ListAssignments() is not in the §6 Store interface yet;
// WS-A must add it before Health() can be wired end-to-end.
type Store interface {
	GetAssignmentByUser(userID int64) (Assignment, bool, error)
	GetSlot(id string) (Slot, bool, error)
	SetGharpState(userID int64, state string) error
	ListAssignments() ([]Assignment, error)
}
