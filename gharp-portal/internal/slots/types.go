package slots

import "time"

// Slot mirrors store.Slot. Defined here to keep this package independent of
// internal/store (Go interface segregation — consumers depend on the interface,
// not the concrete package).
type Slot struct {
	ID           string
	OSUser       string
	DockerHost   string
	Network      string
	BaseURL      string
	InternalAddr string
	CPULimit     string
	MemLimit     string
	Status       string
	MaxRunners   int
	AdminToken   string // gharp ADMIN_TOKEN for this slot's instance (secret)
}

// Assignment mirrors store.Assignment.
type Assignment struct {
	UserID     int64
	SlotID     string
	GharpState string
	AssignedAt time.Time
}

// Store is the narrow interface this package requires from the concrete store.
// Satisfying this interface is sufficient; the concrete store need not be imported.
type Store interface {
	ListSlots() ([]Slot, error)
	GetSlot(id string) (Slot, bool, error)
	UpsertSlot(Slot) error
	AssignFreeSlot(userID int64) (Assignment, error)
	AssignSlot(userID int64, slotID string) (Assignment, error)
}
