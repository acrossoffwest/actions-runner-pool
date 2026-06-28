package lifecycle

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"time"
)

// State constants mirror the DB CHECK constraint on assignments.gharp_state.
const (
	StateStopped  = "stopped"
	StateStarting = "starting"
	StateRunning  = "running"
	StateError    = "error"
)

// slotIDRe validates slot ids before they are passed to the OS command runner.
// Only alphanumerics and dashes; must start with alphanumeric; max 63 chars.
var slotIDRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]{0,62}$`)

// Manager controls start/stop/health of per-user gharp instances.
type Manager struct {
	store        Store
	runner       CommandRunner
	client       *http.Client
	pollTimeout  time.Duration
	pollInterval time.Duration
}

// New creates a Manager with default poll settings (30 s timeout, 1 s interval).
func New(store Store, runner CommandRunner, client *http.Client) *Manager {
	return &Manager{
		store:        store,
		runner:       runner,
		client:       client,
		pollTimeout:  30 * time.Second,
		pollInterval: time.Second,
	}
}

// WithPollConfig overrides the healthz poll timeout and interval (useful in tests).
func (m *Manager) WithPollConfig(timeout, interval time.Duration) *Manager {
	m.pollTimeout = timeout
	m.pollInterval = interval
	return m
}

// Start transitions userID's gharp from stopped → starting → running.
// It sets state to "starting", invokes start-gharp <slot-id>, polls /healthz,
// then sets state to "running". On any failure it sets state to "error".
func (m *Manager) Start(ctx context.Context, userID int64) error {
	a, ok, err := m.store.GetAssignmentByUser(userID)
	if err != nil {
		return fmt.Errorf("lifecycle.Start: store: %w", err)
	}
	if !ok {
		return fmt.Errorf("lifecycle.Start: no assignment for user %d", userID)
	}
	if err := validateSlotID(a.SlotID); err != nil {
		return fmt.Errorf("lifecycle.Start: %w", err)
	}
	slot, ok, err := m.store.GetSlot(a.SlotID)
	if err != nil {
		return fmt.Errorf("lifecycle.Start: get slot: %w", err)
	}
	if !ok {
		return fmt.Errorf("lifecycle.Start: slot %q not found", a.SlotID)
	}

	if err := m.store.SetGharpState(userID, StateStarting); err != nil {
		return fmt.Errorf("lifecycle.Start: set starting: %w", err)
	}

	if err := m.runner.Run(ctx, "start-gharp", a.SlotID); err != nil {
		_ = m.store.SetGharpState(userID, StateError)
		return fmt.Errorf("lifecycle.Start: start-gharp: %w", err)
	}

	if err := m.pollHealthz(ctx, slot.InternalAddr); err != nil {
		_ = m.store.SetGharpState(userID, StateError)
		return fmt.Errorf("lifecycle.Start: healthz poll: %w", err)
	}

	if err := m.store.SetGharpState(userID, StateRunning); err != nil {
		return fmt.Errorf("lifecycle.Start: set running: %w", err)
	}
	return nil
}

// Stop transitions userID's gharp to stopped by invoking stop-gharp <slot-id>.
func (m *Manager) Stop(ctx context.Context, userID int64) error {
	a, ok, err := m.store.GetAssignmentByUser(userID)
	if err != nil {
		return fmt.Errorf("lifecycle.Stop: store: %w", err)
	}
	if !ok {
		return fmt.Errorf("lifecycle.Stop: no assignment for user %d", userID)
	}
	if err := validateSlotID(a.SlotID); err != nil {
		return fmt.Errorf("lifecycle.Stop: %w", err)
	}

	if err := m.runner.Run(ctx, "stop-gharp", a.SlotID); err != nil {
		_ = m.store.SetGharpState(userID, StateError)
		return fmt.Errorf("lifecycle.Stop: stop-gharp: %w", err)
	}

	if err := m.store.SetGharpState(userID, StateStopped); err != nil {
		return fmt.Errorf("lifecycle.Stop: set stopped: %w", err)
	}
	return nil
}

// Health polls /healthz for all running/starting slots and sets state to "error"
// on failure. It returns a snapshot of userID → state for the admin view.
func (m *Manager) Health(ctx context.Context) (map[int64]string, error) {
	assignments, err := m.store.ListAssignments()
	if err != nil {
		return nil, fmt.Errorf("lifecycle.Health: list: %w", err)
	}
	states := make(map[int64]string, len(assignments))
	for _, a := range assignments {
		if a.GharpState != StateRunning && a.GharpState != StateStarting {
			states[a.UserID] = a.GharpState
			continue
		}
		slot, ok, err := m.store.GetSlot(a.SlotID)
		if err != nil || !ok {
			_ = m.store.SetGharpState(a.UserID, StateError)
			states[a.UserID] = StateError
			continue
		}
		if err := m.checkHealthz(ctx, slot.InternalAddr); err != nil {
			_ = m.store.SetGharpState(a.UserID, StateError)
			states[a.UserID] = StateError
			continue
		}
		// A "starting" instance that now answers /healthz is up — promote it to
		// "running". Without this, a portal restart between Start's "starting"
		// write and its "running" write would strand a healthy instance in
		// "starting" forever (user never sees the dashboard iframe).
		if a.GharpState == StateStarting {
			_ = m.store.SetGharpState(a.UserID, StateRunning)
			states[a.UserID] = StateRunning
			continue
		}
		states[a.UserID] = a.GharpState
	}
	return states, nil
}

// pollHealthz retries checkHealthz until success or deadline.
func (m *Manager) pollHealthz(ctx context.Context, addr string) error {
	deadline := time.Now().Add(m.pollTimeout)
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("healthz poll timed out after %v", m.pollTimeout)
		}
		if err := m.checkHealthz(ctx, addr); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(m.pollInterval):
		}
	}
}

func (m *Manager) checkHealthz(ctx context.Context, addr string) error {
	url := "http://" + addr + "/healthz"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz: status %d", resp.StatusCode)
	}
	return nil
}

func validateSlotID(id string) error {
	if !slotIDRe.MatchString(id) {
		return fmt.Errorf("invalid slot id %q: must match ^[a-zA-Z0-9][a-zA-Z0-9-]{0,62}$", id)
	}
	return nil
}
