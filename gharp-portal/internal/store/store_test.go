package store_test

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gharp/portal/internal/store"
)

func newTestDB(t *testing.T) *store.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "portal_test.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// --- users ---

func TestInviteUser(t *testing.T) {
	db := newTestDB(t)

	u, err := db.InviteUser("alice", "user")
	if err != nil {
		t.Fatalf("InviteUser: %v", err)
	}
	if u.Login != "alice" || u.Role != "user" || u.Status != "invited" {
		t.Errorf("unexpected user: %+v", u)
	}
	if u.ID == 0 {
		t.Error("ID must be non-zero")
	}

	// idempotent: invite again returns same record
	u2, err := db.InviteUser("alice", "user")
	if err != nil {
		t.Fatalf("InviteUser idempotent: %v", err)
	}
	if u2.ID != u.ID {
		t.Errorf("idempotent invite returned different ID: %d vs %d", u2.ID, u.ID)
	}
}

func TestInviteUser_AdminRole(t *testing.T) {
	db := newTestDB(t)
	u, err := db.InviteUser("bob", "admin")
	if err != nil {
		t.Fatalf("InviteUser admin: %v", err)
	}
	if u.Role != "admin" {
		t.Errorf("expected role admin, got %q", u.Role)
	}
}

func TestUpsertUserOnLogin_BindsInvited(t *testing.T) {
	db := newTestDB(t)

	_, err := db.InviteUser("charlie", "user")
	if err != nil {
		t.Fatalf("InviteUser: %v", err)
	}

	u, err := db.UpsertUserOnLogin(42, "charlie")
	if err != nil {
		t.Fatalf("UpsertUserOnLogin: %v", err)
	}
	if u.GitHubID != 42 {
		t.Errorf("expected GitHubID 42, got %d", u.GitHubID)
	}
	if u.Status != "active" {
		t.Errorf("expected status active, got %q", u.Status)
	}
}

func TestUpsertUserOnLogin_CaseInsensitive(t *testing.T) {
	db := newTestDB(t)
	_, err := db.InviteUser("Dave", "user")
	if err != nil {
		t.Fatalf("InviteUser: %v", err)
	}

	// login comes back from GitHub as "dave" (different case)
	u, err := db.UpsertUserOnLogin(99, "dave")
	if err != nil {
		t.Fatalf("UpsertUserOnLogin case-insensitive: %v", err)
	}
	if u.Status != "active" {
		t.Errorf("expected active, got %q", u.Status)
	}
}

func TestUpsertUserOnLogin_NotInAllowlist(t *testing.T) {
	db := newTestDB(t)
	_, err := db.UpsertUserOnLogin(7, "unknown")
	if !errors.Is(err, store.ErrNotAllowed) {
		t.Errorf("expected ErrNotAllowed, got %v", err)
	}
}

func TestUpsertUserOnLogin_UpdatesExisting(t *testing.T) {
	db := newTestDB(t)
	_, err := db.InviteUser("eve", "user")
	if err != nil {
		t.Fatalf("InviteUser: %v", err)
	}
	u, err := db.UpsertUserOnLogin(55, "eve")
	if err != nil {
		t.Fatalf("first login: %v", err)
	}

	// second login: login may have changed on GitHub
	u2, err := db.UpsertUserOnLogin(55, "Eve2")
	if err != nil {
		t.Fatalf("second login: %v", err)
	}
	if u2.ID != u.ID {
		t.Errorf("ID mismatch: %d vs %d", u2.ID, u.ID)
	}
	if u2.Login != "Eve2" {
		t.Errorf("expected updated login Eve2, got %q", u2.Login)
	}
}

func TestGetUserByGitHubID(t *testing.T) {
	db := newTestDB(t)
	_, _ = db.InviteUser("frank", "user")
	u, _ := db.UpsertUserOnLogin(100, "frank")

	got, ok, err := db.GetUserByGitHubID(100)
	if err != nil {
		t.Fatalf("GetUserByGitHubID: %v", err)
	}
	if !ok {
		t.Fatal("expected found=true")
	}
	if got.ID != u.ID {
		t.Errorf("ID mismatch")
	}

	_, ok2, err2 := db.GetUserByGitHubID(9999)
	if err2 != nil {
		t.Fatalf("GetUserByGitHubID missing: %v", err2)
	}
	if ok2 {
		t.Error("expected found=false for missing user")
	}
}

func TestListUsers(t *testing.T) {
	db := newTestDB(t)
	db.InviteUser("u1", "user")
	db.InviteUser("u2", "admin")

	users, err := db.ListUsers()
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 2 {
		t.Errorf("expected 2 users, got %d", len(users))
	}
}

func TestSetUserStatus(t *testing.T) {
	db := newTestDB(t)
	_, _ = db.InviteUser("grace", "user")
	u, _ := db.UpsertUserOnLogin(200, "grace")

	if err := db.SetUserStatus(u.ID, "disabled"); err != nil {
		t.Fatalf("SetUserStatus: %v", err)
	}
	got, ok, _ := db.GetUserByGitHubID(200)
	if !ok || got.Status != "disabled" {
		t.Errorf("expected disabled, got %q (ok=%v)", got.Status, ok)
	}
}

// --- slots ---

func testSlot(id string) store.Slot {
	return store.Slot{
		ID:           id,
		OSUser:       "gharp-" + id,
		DockerHost:   "unix:///run/user/1001/docker.sock",
		Network:      "net-" + id,
		BaseURL:      "https://" + id + ".example.com",
		InternalAddr: "127.0.0.1:9001",
		CPULimit:     "2.0",
		MemLimit:     "4g",
		MaxRunners:   4,
		Status:       "free",
	}
}

func TestUpsertSlot(t *testing.T) {
	db := newTestDB(t)

	sl := testSlot("slot-1")
	if err := db.UpsertSlot(sl); err != nil {
		t.Fatalf("UpsertSlot: %v", err)
	}

	got, ok, err := db.GetSlot("slot-1")
	if err != nil {
		t.Fatalf("GetSlot: %v", err)
	}
	if !ok {
		t.Fatal("expected slot found")
	}
	if got.ID != "slot-1" || got.OSUser != "gharp-slot-1" {
		t.Errorf("slot mismatch: %+v", got)
	}
}

// TestUpsertSlot_PreservesStatus verifies that a registry reload (UpsertSlot
// with the same data) does not revert a slot's status back to "free" after it
// has been assigned. UpsertSlot only updates configuration fields, not status.
func TestUpsertSlot_PreservesStatus(t *testing.T) {
	db := newTestDB(t)
	db.UpsertSlot(testSlot("slot-2"))

	// Assign the slot → status transitions to "assigned".
	db.InviteUser("preserve-user", "user")
	u, err := db.UpsertUserOnLogin(701, "preserve-user")
	if err != nil {
		t.Fatalf("UpsertUserOnLogin: %v", err)
	}
	if _, err := db.AssignSlot(u.ID, "slot-2"); err != nil {
		t.Fatalf("AssignSlot: %v", err)
	}

	// Re-upsert (simulates "admin reload slots" with Status="free" from yaml).
	db.UpsertSlot(testSlot("slot-2"))

	got, _, _ := db.GetSlot("slot-2")
	if got.Status != "assigned" {
		t.Errorf("expected status preserved as assigned, got %q", got.Status)
	}
}

func TestGetSlot_Missing(t *testing.T) {
	db := newTestDB(t)
	_, ok, err := db.GetSlot("nope")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected not found")
	}
}

func TestListSlots(t *testing.T) {
	db := newTestDB(t)
	for _, id := range []string{"slot-1", "slot-2", "slot-3"} {
		db.UpsertSlot(testSlot(id))
	}
	slots, err := db.ListSlots()
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 3 {
		t.Errorf("expected 3 slots, got %d", len(slots))
	}
}

// --- assignments ---

func setupUserAndSlot(t *testing.T, db *store.DB, githubID int64, login, slotID string) (store.User, store.Slot) {
	t.Helper()
	db.InviteUser(login, "user")
	u, err := db.UpsertUserOnLogin(githubID, login)
	if err != nil {
		t.Fatalf("UpsertUserOnLogin: %v", err)
	}
	sl := testSlot(slotID)
	if err := db.UpsertSlot(sl); err != nil {
		t.Fatalf("UpsertSlot: %v", err)
	}
	return u, sl
}

func TestAssignSlot(t *testing.T) {
	db := newTestDB(t)
	u, _ := setupUserAndSlot(t, db, 300, "henry", "slot-h")

	a, err := db.AssignSlot(u.ID, "slot-h")
	if err != nil {
		t.Fatalf("AssignSlot: %v", err)
	}
	if a.UserID != u.ID || a.SlotID != "slot-h" {
		t.Errorf("assignment mismatch: %+v", a)
	}
	if a.GharpState != "stopped" {
		t.Errorf("expected initial state stopped, got %q", a.GharpState)
	}

	// slot status must now be "assigned"
	sl, _, _ := db.GetSlot("slot-h")
	if sl.Status != "assigned" {
		t.Errorf("expected slot assigned, got %q", sl.Status)
	}
}

func TestGetAssignmentByUser(t *testing.T) {
	db := newTestDB(t)
	u, _ := setupUserAndSlot(t, db, 301, "ida", "slot-i")
	db.AssignSlot(u.ID, "slot-i")

	a, ok, err := db.GetAssignmentByUser(u.ID)
	if err != nil || !ok {
		t.Fatalf("GetAssignmentByUser: err=%v ok=%v", err, ok)
	}
	if a.SlotID != "slot-i" {
		t.Errorf("wrong slot: %q", a.SlotID)
	}

	_, ok2, _ := db.GetAssignmentByUser(9999)
	if ok2 {
		t.Error("expected not found for unknown user")
	}
}

func TestSetGharpState(t *testing.T) {
	db := newTestDB(t)
	u, _ := setupUserAndSlot(t, db, 302, "jack", "slot-j")
	db.AssignSlot(u.ID, "slot-j")

	if err := db.SetGharpState(u.ID, "running"); err != nil {
		t.Fatalf("SetGharpState: %v", err)
	}
	a, _, _ := db.GetAssignmentByUser(u.ID)
	if a.GharpState != "running" {
		t.Errorf("expected running, got %q", a.GharpState)
	}
}

func TestAssignFreeSlot(t *testing.T) {
	db := newTestDB(t)
	db.UpsertSlot(testSlot("slot-f1"))
	db.UpsertSlot(testSlot("slot-f2"))

	_, _ = db.InviteUser("kim", "user")
	u, _ := db.UpsertUserOnLogin(400, "kim")

	a, err := db.AssignFreeSlot(u.ID)
	if err != nil {
		t.Fatalf("AssignFreeSlot: %v", err)
	}
	if a.SlotID != "slot-f1" && a.SlotID != "slot-f2" {
		t.Errorf("unexpected slot: %q", a.SlotID)
	}
}

func TestAssignFreeSlot_NoSlots(t *testing.T) {
	db := newTestDB(t)
	_, _ = db.InviteUser("lee", "user")
	u, _ := db.UpsertUserOnLogin(500, "lee")

	_, err := db.AssignFreeSlot(u.ID)
	if !errors.Is(err, store.ErrNoFreeSlot) {
		t.Errorf("expected ErrNoFreeSlot, got %v", err)
	}
}

// TestAssignFreeSlot_Concurrent verifies no two goroutines receive the same slot.
func TestAssignFreeSlot_Concurrent(t *testing.T) {
	db := newTestDB(t)

	const numSlots = 3
	const numUsers = 6

	for i := 1; i <= numSlots; i++ {
		db.UpsertSlot(testSlot(fmt.Sprintf("slot-%d", i)))
	}

	userIDs := make([]int64, numUsers)
	for i := 0; i < numUsers; i++ {
		login := fmt.Sprintf("cu%d", i)
		db.InviteUser(login, "user")
		u, err := db.UpsertUserOnLogin(int64(600+i), login)
		if err != nil {
			t.Fatalf("setup user %d: %v", i, err)
		}
		userIDs[i] = u.ID
	}

	type result struct {
		a   store.Assignment
		err error
	}
	results := make([]result, numUsers)
	var wg sync.WaitGroup
	for i := 0; i < numUsers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			a, err := db.AssignFreeSlot(userIDs[i])
			results[i] = result{a, err}
		}(i)
	}
	wg.Wait()

	assigned := map[string]int{}
	successes := 0
	for _, r := range results {
		if r.err == nil {
			successes++
			assigned[r.a.SlotID]++
		} else if !errors.Is(r.err, store.ErrNoFreeSlot) {
			t.Errorf("unexpected error: %v", r.err)
		}
	}
	if successes != numSlots {
		t.Errorf("expected %d successful assignments, got %d", numSlots, successes)
	}
	for slotID, count := range assigned {
		if count != 1 {
			t.Errorf("slot %s assigned %d times (want 1) — concurrency bug", slotID, count)
		}
	}
}

// --- sessions ---

func TestCreateAndGetSession(t *testing.T) {
	db := newTestDB(t)
	_, _ = db.InviteUser("mia", "user")
	u, _ := db.UpsertUserOnLogin(700, "mia")

	sess, err := db.CreateSession(u.ID, 24*time.Hour)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.Token == "" || sess.CSRF == "" {
		t.Error("token and csrf must be non-empty")
	}
	if sess.UserID != u.ID {
		t.Errorf("wrong user: %d", sess.UserID)
	}

	got, ok, err := db.GetSession(sess.Token)
	if err != nil || !ok {
		t.Fatalf("GetSession: err=%v ok=%v", err, ok)
	}
	if got.CSRF != sess.CSRF {
		t.Errorf("CSRF mismatch")
	}
}

func TestGetSession_Expired(t *testing.T) {
	db := newTestDB(t)
	_, _ = db.InviteUser("ned", "user")
	u, _ := db.UpsertUserOnLogin(800, "ned")

	sess, _ := db.CreateSession(u.ID, -time.Second) // already expired

	_, ok, err := db.GetSession(sess.Token)
	if err != nil {
		t.Fatalf("GetSession expired: %v", err)
	}
	if ok {
		t.Error("expected expired session to return ok=false")
	}
}

func TestDeleteSession(t *testing.T) {
	db := newTestDB(t)
	_, _ = db.InviteUser("olivia", "user")
	u, _ := db.UpsertUserOnLogin(900, "olivia")
	sess, _ := db.CreateSession(u.ID, time.Hour)

	if err := db.DeleteSession(sess.Token); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	_, ok, _ := db.GetSession(sess.Token)
	if ok {
		t.Error("expected session deleted")
	}
}

// --- audit ---

func TestAudit(t *testing.T) {
	db := newTestDB(t)
	_, _ = db.InviteUser("peter", "user")
	u, _ := db.UpsertUserOnLogin(1000, "peter")

	if err := db.Audit(u.ID, "user.invite", "alice", ""); err != nil {
		t.Fatalf("Audit: %v", err)
	}
	// audit-log has no read method in Store (admin reads via raw query in WS-E)
	// just verify insert doesn't error
}

// TestAssignSlot_Reassign locks the fix for the codex P2: reassigning an
// already-assigned user must free the old slot and succeed (a naive insert
// would hit UNIQUE(user_id)).
func TestAssignSlot_Reassign(t *testing.T) {
	db := newTestDB(t)
	db.UpsertSlot(testSlot("slot-r1"))
	db.UpsertSlot(testSlot("slot-r2"))
	db.InviteUser("rex", "user")
	u, _ := db.UpsertUserOnLogin(700, "rex")

	if _, err := db.AssignSlot(u.ID, "slot-r1"); err != nil {
		t.Fatalf("first assign: %v", err)
	}
	a, err := db.AssignSlot(u.ID, "slot-r2")
	if err != nil {
		t.Fatalf("reassign: %v", err)
	}
	if a.SlotID != "slot-r2" {
		t.Errorf("reassigned slot = %q, want slot-r2", a.SlotID)
	}
	if s1, _, _ := db.GetSlot("slot-r1"); s1.Status != "free" {
		t.Errorf("old slot status = %q, want free", s1.Status)
	}
	if s2, _, _ := db.GetSlot("slot-r2"); s2.Status != "assigned" {
		t.Errorf("new slot status = %q, want assigned", s2.Status)
	}
	as, _ := db.ListAssignments()
	n := 0
	for _, x := range as {
		if x.UserID == u.ID {
			n++
		}
	}
	if n != 1 {
		t.Errorf("assignments for user = %d, want 1", n)
	}
}
