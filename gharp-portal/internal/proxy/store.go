package proxy

// Assignment is the local projection of store.Assignment used by this package.
type Assignment struct {
	UserID int64
	SlotID string
}

// Slot is the local projection of store.Slot used by this package.
// NOTE: AdminToken is not in the §6 store.Slot definition; WS-A must add it
// (or provide it via a separate secrets store) before the proxy can be wired.
type Slot struct {
	InternalAddr string
	AdminToken   string
}

// Store is the narrow store interface required by proxy.
type Store interface {
	GetAssignmentByUser(userID int64) (Assignment, bool, error)
	GetSlot(id string) (Slot, bool, error)
}
