package slots

import "fmt"

// Assigner wraps the narrow Store interface and provides assignment helpers.
// Selection policy: prefer AssignFreeSlot (store picks atomically); error if
// no free slot exists. For explicit assignment use AssignSlot.
type Assigner struct {
	store Store
}

// NewAssigner creates an Assigner backed by st.
func NewAssigner(st Store) *Assigner {
	return &Assigner{store: st}
}

// AssignFreeSlot atomically assigns any free slot to userID.
// Returns an error (from the store) when no free slot is available.
func (a *Assigner) AssignFreeSlot(userID int64) (Assignment, error) {
	asgn, err := a.store.AssignFreeSlot(userID)
	if err != nil {
		return Assignment{}, fmt.Errorf("slots: assign free slot to user %d: %w", userID, err)
	}
	return asgn, nil
}

// AssignSlot assigns a specific slot (by slotID) to userID.
// Returns an error if the slot is already assigned or does not exist.
func (a *Assigner) AssignSlot(userID int64, slotID string) (Assignment, error) {
	asgn, err := a.store.AssignSlot(userID, slotID)
	if err != nil {
		return Assignment{}, fmt.Errorf("slots: assign slot %q to user %d: %w", slotID, userID, err)
	}
	return asgn, nil
}
