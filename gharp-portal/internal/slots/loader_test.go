package slots_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gharp/portal/internal/slots"
)

// ---- helpers ----------------------------------------------------------------

const validYAML = `
slots:
  - id: slot-1
    os_user: gharp-s1
    uid: 1001
    docker_host: unix:///run/user/1001/docker.sock
    network: gharp-s1
    base_url: https://s1.gharp.example.com
    internal_addr: 127.0.0.1:9001
    cpu_limit: "2.0"
    mem_limit: "4g"
    max_runners: 4
  - id: slot-2
    os_user: gharp-s2
    uid: 1002
    docker_host: unix:///run/user/1002/docker.sock
    network: gharp-s2
    base_url: https://s2.gharp.example.com
    internal_addr: 127.0.0.1:9002
    cpu_limit: "1.0"
    mem_limit: "2g"
    max_runners: 2
`

// mockStore is a hand-written mock; it never imports internal/store.
type mockStore struct {
	upserted   []slots.Slot
	listResult []slots.Slot
	listErr    error
	upsertErr  error

	assignFreeResult slots.Assignment
	assignFreeErr    error
	assignResult     slots.Assignment
	assignErr        error
}

func (m *mockStore) ListSlots() ([]slots.Slot, error) { return m.listResult, m.listErr }
func (m *mockStore) GetSlot(id string) (slots.Slot, bool, error) {
	for _, s := range m.listResult {
		if s.ID == id {
			return s, true, nil
		}
	}
	return slots.Slot{}, false, nil
}
func (m *mockStore) UpsertSlot(s slots.Slot) error {
	m.upserted = append(m.upserted, s)
	return m.upsertErr
}
func (m *mockStore) AssignFreeSlot(userID int64) (slots.Assignment, error) {
	return m.assignFreeResult, m.assignFreeErr
}
func (m *mockStore) AssignSlot(userID int64, slotID string) (slots.Assignment, error) {
	return m.assignResult, m.assignErr
}

// ---- ParseFile --------------------------------------------------------------

func TestParseFile_valid(t *testing.T) {
	ss, err := slots.ParseFile(strings.NewReader(validYAML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ss) != 2 {
		t.Fatalf("want 2 slots, got %d", len(ss))
	}
	s := ss[0]
	if s.ID != "slot-1" {
		t.Errorf("ID: want slot-1, got %q", s.ID)
	}
	if s.OSUser != "gharp-s1" {
		t.Errorf("OSUser: want gharp-s1, got %q", s.OSUser)
	}
	if s.DockerHost != "unix:///run/user/1001/docker.sock" {
		t.Errorf("DockerHost: got %q", s.DockerHost)
	}
	if s.InternalAddr != "127.0.0.1:9001" {
		t.Errorf("InternalAddr: got %q", s.InternalAddr)
	}
	if s.MaxRunners != 4 {
		t.Errorf("MaxRunners: want 4, got %d", s.MaxRunners)
	}
}

func TestParseFile_emptySlots(t *testing.T) {
	_, err := slots.ParseFile(strings.NewReader("slots: []\n"))
	if err == nil {
		t.Fatal("expected error for empty slots list")
	}
}

func TestParseFile_missingID(t *testing.T) {
	yaml := `
slots:
  - os_user: gharp-s1
    uid: 1001
    docker_host: unix:///run/user/1001/docker.sock
    network: gharp-s1
    base_url: https://s1.gharp.example.com
    internal_addr: 127.0.0.1:9001
`
	_, err := slots.ParseFile(strings.NewReader(yaml))
	if err == nil {
		t.Fatal("expected error for missing id")
	}
}

func TestParseFile_missingOSUser(t *testing.T) {
	yaml := `
slots:
  - id: slot-1
    uid: 1001
    docker_host: unix:///run/user/1001/docker.sock
    network: gharp-s1
    base_url: https://s1.gharp.example.com
    internal_addr: 127.0.0.1:9001
`
	_, err := slots.ParseFile(strings.NewReader(yaml))
	if err == nil {
		t.Fatal("expected error for missing os_user")
	}
}

func TestParseFile_missingDockerHost(t *testing.T) {
	yaml := `
slots:
  - id: slot-1
    os_user: gharp-s1
    uid: 1001
    network: gharp-s1
    base_url: https://s1.gharp.example.com
    internal_addr: 127.0.0.1:9001
`
	_, err := slots.ParseFile(strings.NewReader(yaml))
	if err == nil {
		t.Fatal("expected error for missing docker_host")
	}
}

func TestParseFile_missingBaseURL(t *testing.T) {
	yaml := `
slots:
  - id: slot-1
    os_user: gharp-s1
    uid: 1001
    docker_host: unix:///run/user/1001/docker.sock
    network: gharp-s1
    internal_addr: 127.0.0.1:9001
`
	_, err := slots.ParseFile(strings.NewReader(yaml))
	if err == nil {
		t.Fatal("expected error for missing base_url")
	}
}

func TestParseFile_missingInternalAddr(t *testing.T) {
	yaml := `
slots:
  - id: slot-1
    os_user: gharp-s1
    uid: 1001
    docker_host: unix:///run/user/1001/docker.sock
    network: gharp-s1
    base_url: https://s1.gharp.example.com
`
	_, err := slots.ParseFile(strings.NewReader(yaml))
	if err == nil {
		t.Fatal("expected error for missing internal_addr")
	}
}

func TestParseFile_duplicateIDs(t *testing.T) {
	yaml := `
slots:
  - id: slot-1
    os_user: gharp-s1
    uid: 1001
    docker_host: unix:///run/user/1001/docker.sock
    network: gharp-s1
    base_url: https://s1.gharp.example.com
    internal_addr: 127.0.0.1:9001
  - id: slot-1
    os_user: gharp-s2
    uid: 1002
    docker_host: unix:///run/user/1002/docker.sock
    network: gharp-s2
    base_url: https://s2.gharp.example.com
    internal_addr: 127.0.0.1:9002
`
	_, err := slots.ParseFile(strings.NewReader(yaml))
	if err == nil {
		t.Fatal("expected error for duplicate ids")
	}
}

func TestParseFile_invalidDockerHost(t *testing.T) {
	yaml := `
slots:
  - id: slot-1
    os_user: gharp-s1
    uid: 1001
    docker_host: ftp:///run/user/1001/docker.sock
    network: gharp-s1
    base_url: https://s1.gharp.example.com
    internal_addr: 127.0.0.1:9001
`
	_, err := slots.ParseFile(strings.NewReader(yaml))
	if err == nil {
		t.Fatal("expected error for invalid docker_host scheme")
	}
}

func TestParseFile_tcpDockerHostAllowed(t *testing.T) {
	yaml := `
slots:
  - id: slot-1
    os_user: gharp-s1
    uid: 1001
    docker_host: tcp://127.0.0.1:2375
    network: gharp-s1
    base_url: https://s1.gharp.example.com
    internal_addr: 127.0.0.1:9001
`
	_, err := slots.ParseFile(strings.NewReader(yaml))
	if err != nil {
		t.Fatalf("tcp docker_host should be valid: %v", err)
	}
}

func TestParseFile_missingNetwork(t *testing.T) {
	yaml := `
slots:
  - id: slot-1
    os_user: gharp-s1
    uid: 1001
    docker_host: unix:///run/user/1001/docker.sock
    base_url: https://s1.gharp.example.com
    internal_addr: 127.0.0.1:9001
`
	_, err := slots.ParseFile(strings.NewReader(yaml))
	if err == nil {
		t.Fatal("expected error for missing network")
	}
}

// ---- LoadRegistry -----------------------------------------------------------

func TestLoadRegistry_upsertsAll(t *testing.T) {
	st := &mockStore{}
	err := slots.LoadRegistry(strings.NewReader(validYAML), st)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(st.upserted) != 2 {
		t.Fatalf("want 2 upserts, got %d", len(st.upserted))
	}
	if st.upserted[0].ID != "slot-1" {
		t.Errorf("first upserted slot id: want slot-1, got %q", st.upserted[0].ID)
	}
	if st.upserted[1].ID != "slot-2" {
		t.Errorf("second upserted slot id: want slot-2, got %q", st.upserted[1].ID)
	}
}

func TestLoadRegistry_idempotent(t *testing.T) {
	st := &mockStore{}
	if err := slots.LoadRegistry(strings.NewReader(validYAML), st); err != nil {
		t.Fatalf("first load: %v", err)
	}
	if err := slots.LoadRegistry(strings.NewReader(validYAML), st); err != nil {
		t.Fatalf("second load: %v", err)
	}
	// UpsertSlot called 2×2 = 4 times total; idempotency is store's responsibility
	if len(st.upserted) != 4 {
		t.Errorf("want 4 total upserts (2 per load × 2 loads), got %d", len(st.upserted))
	}
}

func TestLoadRegistry_propagatesUpsertError(t *testing.T) {
	st := &mockStore{upsertErr: errors.New("db full")}
	err := slots.LoadRegistry(strings.NewReader(validYAML), st)
	if err == nil {
		t.Fatal("expected error propagated from upsert")
	}
}

func TestLoadRegistry_invalidYAML(t *testing.T) {
	err := slots.LoadRegistry(strings.NewReader(": bad: yaml: [\n"), &mockStore{})
	if err == nil {
		t.Fatal("expected error for invalid yaml")
	}
}

// ---- Assigner ---------------------------------------------------------------

func TestAssigner_AssignFreeSlot_success(t *testing.T) {
	want := slots.Assignment{UserID: 42, SlotID: "slot-1", GharpState: "stopped", AssignedAt: time.Now()}
	st := &mockStore{assignFreeResult: want}
	a := slots.NewAssigner(st)
	got, err := a.AssignFreeSlot(42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.SlotID != want.SlotID {
		t.Errorf("SlotID: want %q, got %q", want.SlotID, got.SlotID)
	}
}

func TestAssigner_AssignFreeSlot_noneAvailable(t *testing.T) {
	st := &mockStore{assignFreeErr: errors.New("no free slot")}
	a := slots.NewAssigner(st)
	_, err := a.AssignFreeSlot(99)
	if err == nil {
		t.Fatal("expected error when no free slot")
	}
}

func TestAssigner_AssignSlot_explicit(t *testing.T) {
	want := slots.Assignment{UserID: 7, SlotID: "slot-2", GharpState: "stopped", AssignedAt: time.Now()}
	st := &mockStore{assignResult: want}
	a := slots.NewAssigner(st)
	got, err := a.AssignSlot(7, "slot-2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.SlotID != want.SlotID {
		t.Errorf("SlotID: want %q, got %q", want.SlotID, got.SlotID)
	}
}

func TestAssigner_AssignSlot_storeError(t *testing.T) {
	st := &mockStore{assignErr: errors.New("slot taken")}
	a := slots.NewAssigner(st)
	_, err := a.AssignSlot(1, "slot-1")
	if err == nil {
		t.Fatal("expected error from store")
	}
}
