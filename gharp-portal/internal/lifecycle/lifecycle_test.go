package lifecycle_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gharp/portal/internal/lifecycle"
)

// ---- mock store ----

type mockStore struct {
	assignment    lifecycle.Assignment
	hasAssignment bool
	slot          lifecycle.Slot
	hasSlot       bool
	assignments   []lifecycle.Assignment
	stateLog      []stateCall
	storeErr      error
}

type stateCall struct {
	userID int64
	state  string
}

func (s *mockStore) GetAssignmentByUser(userID int64) (lifecycle.Assignment, bool, error) {
	return s.assignment, s.hasAssignment, s.storeErr
}
func (s *mockStore) GetSlot(id string) (lifecycle.Slot, bool, error) {
	return s.slot, s.hasSlot, s.storeErr
}
func (s *mockStore) SetGharpState(userID int64, state string) error {
	s.stateLog = append(s.stateLog, stateCall{userID, state})
	return s.storeErr
}
func (s *mockStore) ListAssignments() ([]lifecycle.Assignment, error) {
	return s.assignments, s.storeErr
}

// ---- mock runner ----

type mockRunner struct {
	calls  [][]string
	runErr error
}

func (r *mockRunner) Run(_ context.Context, name string, args ...string) error {
	r.calls = append(r.calls, append([]string{name}, args...))
	return r.runErr
}

// ---- helpers ----

func healthyServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
}

func unhealthyServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
}

func addrOf(srv *httptest.Server) string {
	return strings.TrimPrefix(srv.URL, "http://")
}

func fastManager(st lifecycle.Store, r lifecycle.CommandRunner, client *http.Client) *lifecycle.Manager {
	return lifecycle.New(st, r, client).WithPollConfig(200*time.Millisecond, 20*time.Millisecond)
}

// ---- Start tests ----

func TestStart_Success(t *testing.T) {
	srv := healthyServer(t)
	defer srv.Close()

	runner := &mockRunner{}
	store := &mockStore{
		hasAssignment: true,
		assignment:    lifecycle.Assignment{UserID: 1, SlotID: "slot-1"},
		hasSlot:       true,
		slot:          lifecycle.Slot{ID: "slot-1", InternalAddr: addrOf(srv)},
	}
	m := fastManager(store, runner, &http.Client{})

	if err := m.Start(context.Background(), 1); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	// command must be exactly: start-gharp slot-1
	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 runner call, got %d", len(runner.calls))
	}
	if runner.calls[0][0] != "start-gharp" || runner.calls[0][1] != "slot-1" || len(runner.calls[0]) != 2 {
		t.Errorf("unexpected runner call: %v", runner.calls[0])
	}

	// state transitions: starting → running
	wantStates := []stateCall{{1, lifecycle.StateStarting}, {1, lifecycle.StateRunning}}
	if len(store.stateLog) != len(wantStates) {
		t.Fatalf("state log: want %v, got %v", wantStates, store.stateLog)
	}
	for i, w := range wantStates {
		if store.stateLog[i] != w {
			t.Errorf("stateLog[%d]: want %v, got %v", i, w, store.stateLog[i])
		}
	}
}

func TestStart_NoAssignment(t *testing.T) {
	runner := &mockRunner{}
	store := &mockStore{hasAssignment: false}
	m := fastManager(store, runner, &http.Client{})

	err := m.Start(context.Background(), 42)
	if err == nil {
		t.Fatal("expected error for missing assignment")
	}
	if len(runner.calls) != 0 {
		t.Errorf("runner should not be called when no assignment")
	}
}

func TestStart_StoreError(t *testing.T) {
	runner := &mockRunner{}
	store := &mockStore{storeErr: errors.New("db down")}
	m := fastManager(store, runner, &http.Client{})

	if err := m.Start(context.Background(), 1); err == nil {
		t.Fatal("expected error on store failure")
	}
}

func TestStart_InvalidSlotID(t *testing.T) {
	runner := &mockRunner{}
	store := &mockStore{
		hasAssignment: true,
		// slot-id with shell metacharacters — must be rejected before runner is called
		assignment: lifecycle.Assignment{UserID: 1, SlotID: "slot-1; rm -rf /"},
		hasSlot:    true,
		slot:       lifecycle.Slot{ID: "slot-1; rm -rf /"},
	}
	m := fastManager(store, runner, &http.Client{})

	err := m.Start(context.Background(), 1)
	if err == nil {
		t.Fatal("expected validation error for bad slot id")
	}
	if len(runner.calls) != 0 {
		t.Errorf("runner must not be called with invalid slot id")
	}
}

func TestStart_CommandFails_SetsError(t *testing.T) {
	srv := healthyServer(t)
	defer srv.Close()

	runner := &mockRunner{runErr: errors.New("command failed")}
	store := &mockStore{
		hasAssignment: true,
		assignment:    lifecycle.Assignment{UserID: 1, SlotID: "slot-1"},
		hasSlot:       true,
		slot:          lifecycle.Slot{ID: "slot-1", InternalAddr: addrOf(srv)},
	}
	m := fastManager(store, runner, &http.Client{})

	err := m.Start(context.Background(), 1)
	if err == nil {
		t.Fatal("expected error when command fails")
	}
	// must have tried to set starting, then error
	if len(store.stateLog) < 2 {
		t.Fatalf("expected at least 2 state transitions, got %v", store.stateLog)
	}
	last := store.stateLog[len(store.stateLog)-1]
	if last.state != lifecycle.StateError {
		t.Errorf("last state should be error, got %q", last.state)
	}
}

func TestStart_HealthzTimeout_SetsError(t *testing.T) {
	srv := unhealthyServer(t)
	defer srv.Close()

	runner := &mockRunner{}
	store := &mockStore{
		hasAssignment: true,
		assignment:    lifecycle.Assignment{UserID: 1, SlotID: "slot-1"},
		hasSlot:       true,
		slot:          lifecycle.Slot{ID: "slot-1", InternalAddr: addrOf(srv)},
	}
	m := fastManager(store, runner, &http.Client{})

	err := m.Start(context.Background(), 1)
	if err == nil {
		t.Fatal("expected error when healthz times out")
	}
	last := store.stateLog[len(store.stateLog)-1]
	if last.state != lifecycle.StateError {
		t.Errorf("state should be error after healthz timeout, got %q", last.state)
	}
}

func TestStart_CommandReceivesOnlySlotID(t *testing.T) {
	srv := healthyServer(t)
	defer srv.Close()

	runner := &mockRunner{}
	store := &mockStore{
		hasAssignment: true,
		assignment:    lifecycle.Assignment{UserID: 7, SlotID: "slot-7"},
		hasSlot:       true,
		slot:          lifecycle.Slot{ID: "slot-7", InternalAddr: addrOf(srv)},
	}
	m := fastManager(store, runner, &http.Client{})
	_ = m.Start(context.Background(), 7)

	if len(runner.calls) != 1 {
		t.Fatalf("expected exactly 1 runner call")
	}
	call := runner.calls[0]
	if call[0] != "start-gharp" {
		t.Errorf("command name must be start-gharp, got %q", call[0])
	}
	if len(call) != 2 {
		t.Errorf("command must have exactly 1 arg (slot id), got args: %v", call[1:])
	}
	if call[1] != "slot-7" {
		t.Errorf("arg must be slot id %q, got %q", "slot-7", call[1])
	}
}

// ---- Stop tests ----

func TestStop_Success(t *testing.T) {
	runner := &mockRunner{}
	store := &mockStore{
		hasAssignment: true,
		assignment:    lifecycle.Assignment{UserID: 1, SlotID: "slot-1", GharpState: lifecycle.StateRunning},
		hasSlot:       true,
		slot:          lifecycle.Slot{ID: "slot-1"},
	}
	m := fastManager(store, runner, &http.Client{})

	if err := m.Stop(context.Background(), 1); err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}

	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 runner call, got %d", len(runner.calls))
	}
	call := runner.calls[0]
	if call[0] != "stop-gharp" || call[1] != "slot-1" || len(call) != 2 {
		t.Errorf("unexpected runner call: %v", call)
	}

	last := store.stateLog[len(store.stateLog)-1]
	if last.state != lifecycle.StateStopped {
		t.Errorf("state should be stopped, got %q", last.state)
	}
}

func TestStop_NoAssignment(t *testing.T) {
	runner := &mockRunner{}
	store := &mockStore{hasAssignment: false}
	m := fastManager(store, runner, &http.Client{})

	if err := m.Stop(context.Background(), 99); err == nil {
		t.Fatal("expected error for missing assignment")
	}
	if len(runner.calls) != 0 {
		t.Errorf("runner should not be called when no assignment")
	}
}

func TestStop_CommandFails_SetsError(t *testing.T) {
	runner := &mockRunner{runErr: errors.New("systemd failure")}
	store := &mockStore{
		hasAssignment: true,
		assignment:    lifecycle.Assignment{UserID: 1, SlotID: "slot-1"},
		hasSlot:       true,
		slot:          lifecycle.Slot{ID: "slot-1"},
	}
	m := fastManager(store, runner, &http.Client{})

	err := m.Stop(context.Background(), 1)
	if err == nil {
		t.Fatal("expected error when stop command fails")
	}
	last := store.stateLog[len(store.stateLog)-1]
	if last.state != lifecycle.StateError {
		t.Errorf("state should be error when stop fails, got %q", last.state)
	}
}

func TestStop_InvalidSlotID(t *testing.T) {
	runner := &mockRunner{}
	store := &mockStore{
		hasAssignment: true,
		assignment:    lifecycle.Assignment{UserID: 1, SlotID: "../../../etc/passwd"},
	}
	m := fastManager(store, runner, &http.Client{})

	if err := m.Stop(context.Background(), 1); err == nil {
		t.Fatal("expected validation error for bad slot id")
	}
	if len(runner.calls) != 0 {
		t.Errorf("runner must not be called with invalid slot id")
	}
}

// ---- Health tests ----

func TestHealth_ReturnsStates(t *testing.T) {
	srv := healthyServer(t)
	defer srv.Close()

	store := &mockStore{
		assignments: []lifecycle.Assignment{
			{UserID: 1, SlotID: "slot-1", GharpState: lifecycle.StateRunning},
			{UserID: 2, SlotID: "slot-2", GharpState: lifecycle.StateStopped},
		},
		hasSlot: true,
		slot:    lifecycle.Slot{ID: "slot-1", InternalAddr: addrOf(srv)},
	}
	m := fastManager(store, &mockRunner{}, &http.Client{})

	states, err := m.Health(context.Background())
	if err != nil {
		t.Fatalf("Health error: %v", err)
	}
	if states[1] != lifecycle.StateRunning {
		t.Errorf("user 1 state: want running, got %q", states[1])
	}
	if states[2] != lifecycle.StateStopped {
		t.Errorf("user 2 state: want stopped, got %q", states[2])
	}
}

func TestHealth_SetsErrorOnHealthzFailure(t *testing.T) {
	srv := unhealthyServer(t)
	defer srv.Close()

	store := &mockStore{
		assignments: []lifecycle.Assignment{
			{UserID: 3, SlotID: "slot-3", GharpState: lifecycle.StateRunning},
		},
		hasSlot: true,
		slot:    lifecycle.Slot{ID: "slot-3", InternalAddr: addrOf(srv)},
	}
	m := fastManager(store, &mockRunner{}, &http.Client{})

	states, err := m.Health(context.Background())
	if err != nil {
		t.Fatalf("Health error: %v", err)
	}
	if states[3] != lifecycle.StateError {
		t.Errorf("user 3 should be error when healthz fails, got %q", states[3])
	}
	// store must have been called to record error
	found := false
	for _, c := range store.stateLog {
		if c.userID == 3 && c.state == lifecycle.StateError {
			found = true
		}
	}
	if !found {
		t.Errorf("SetGharpState(3, error) not called; stateLog: %v", store.stateLog)
	}
}

func TestHealth_ListError(t *testing.T) {
	store := &mockStore{storeErr: errors.New("db down")}
	m := fastManager(store, &mockRunner{}, &http.Client{})

	_, err := m.Health(context.Background())
	if err == nil {
		t.Fatal("expected error when ListAssignments fails")
	}
}

// ---- slot-id validation edge cases ----

func TestValidSlotIDs(t *testing.T) {
	valid := []string{"slot-1", "slot-99", "s1", "SLOT-1", "a", "abc-def-ghi"}
	invalid := []string{
		"", "-slot", "slot 1", "slot_1", "slot;1", "slot/1",
		"../etc", "slot-1; rm -rf /", "$HOME",
	}

	for _, id := range valid {
		srv := healthyServer(t)
		runner := &mockRunner{}
		store := &mockStore{
			hasAssignment: true,
			assignment:    lifecycle.Assignment{UserID: 1, SlotID: id},
			hasSlot:       true,
			slot:          lifecycle.Slot{ID: id, InternalAddr: addrOf(srv)},
		}
		m := fastManager(store, runner, &http.Client{})
		_ = m.Start(context.Background(), 1)
		if len(runner.calls) == 0 {
			t.Errorf("valid slot id %q was rejected", id)
		}
		srv.Close()
	}

	for _, id := range invalid {
		runner := &mockRunner{}
		store := &mockStore{
			hasAssignment: true,
			assignment:    lifecycle.Assignment{UserID: 1, SlotID: id},
			hasSlot:       true,
		}
		m := fastManager(store, runner, &http.Client{})
		if err := m.Start(context.Background(), 1); err == nil {
			t.Errorf("invalid slot id %q should have been rejected", id)
		}
		if len(runner.calls) != 0 {
			t.Errorf("runner called with invalid slot id %q", id)
		}
	}
}

// TestHealth_PromotesStartingToRunning locks the codex P2 fix: a "starting"
// instance that now answers /healthz is promoted to "running" (otherwise a
// portal restart could strand it in "starting" forever).
func TestHealth_PromotesStartingToRunning(t *testing.T) {
	srv := healthyServer(t)
	defer srv.Close()

	store := &mockStore{
		assignments: []lifecycle.Assignment{
			{UserID: 7, SlotID: "slot-7", GharpState: lifecycle.StateStarting},
		},
		hasSlot: true,
		slot:    lifecycle.Slot{ID: "slot-7", InternalAddr: addrOf(srv)},
	}
	m := fastManager(store, &mockRunner{}, &http.Client{})

	states, err := m.Health(context.Background())
	if err != nil {
		t.Fatalf("Health error: %v", err)
	}
	if states[7] != lifecycle.StateRunning {
		t.Errorf("starting+healthy should promote to running, got %q", states[7])
	}
	found := false
	for _, c := range store.stateLog {
		if c.userID == 7 && c.state == lifecycle.StateRunning {
			found = true
		}
	}
	if !found {
		t.Errorf("SetGharpState(7, running) not called; stateLog: %v", store.stateLog)
	}
}
