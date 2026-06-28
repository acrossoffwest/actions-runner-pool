package store

import (
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // pure-Go, cgo-free SQLite driver
)

//go:embed schema.sql
var schemaSQL string

const sqliteTimeLayout = "2006-01-02 15:04:05"

// DB is a SQLite-backed implementation of Store.
// MaxOpenConns is set to 1 so concurrent calls serialize through the pool,
// making AssignFreeSlot atomic without requiring BEGIN IMMEDIATE.
type DB struct {
	db *sql.DB
}

// sentinel errors

// ErrNotAllowed is returned by UpsertUserOnLogin when the GitHub login has
// no invited or existing row in the users table.
var ErrNotAllowed = errors.New("store: user not in allowlist")

// ErrNoFreeSlot is returned by AssignFreeSlot when no free slot exists.
var ErrNoFreeSlot = errors.New("store: no free slot available")

// compile-time interface check
var _ Store = (*DB)(nil)

// Open opens (or creates) the SQLite database at dsn and applies the schema.
func Open(dsn string) (*DB, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %q: %w", dsn, err)
	}
	// Single connection serializes writes; prevents concurrent AssignFreeSlot
	// from reading stale slot state before a competing transaction commits.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	pragmas := []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
		"PRAGMA busy_timeout = 5000",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			db.Close()
			return nil, fmt.Errorf("store: pragma %q: %w", p, err)
		}
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: schema: %w", err)
	}
	return &DB{db: db}, nil
}

// Close closes the underlying database.
func (s *DB) Close() error { return s.db.Close() }

// --- helpers ---

func formatTime(t time.Time) string { return t.UTC().Format(sqliteTimeLayout) }

// timeScanner scans SQLite datetime text into time.Time.
type timeScanner struct{ time.Time }

func (ts *timeScanner) Scan(v any) error {
	switch val := v.(type) {
	case time.Time:
		// modernc.org/sqlite returns time.Time directly for DATETIME columns.
		ts.Time = val.UTC()
		return nil
	case string:
		for _, layout := range []string{sqliteTimeLayout, time.RFC3339} {
			if t, err := time.ParseInLocation(layout, val, time.UTC); err == nil {
				ts.Time = t.UTC()
				return nil
			}
		}
		return fmt.Errorf("store: cannot parse time %q", val)
	case []byte:
		return ts.Scan(string(val))
	case nil:
		return nil
	default:
		return fmt.Errorf("store: unsupported time type %T", v)
	}
}

func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("store: generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface{ Scan(dest ...any) error }

func scanSlot(s scanner) (Slot, error) {
	var sl Slot
	err := s.Scan(&sl.ID, &sl.OSUser, &sl.DockerHost, &sl.Network,
		&sl.BaseURL, &sl.InternalAddr, &sl.CPULimit, &sl.MemLimit,
		&sl.MaxRunners, &sl.Status, &sl.AdminToken)
	return sl, err
}

const slotCols = "id, os_user, docker_host, network, base_url, internal_addr, cpu_limit, mem_limit, max_runners, status, admin_token"

// --- users ---

func (s *DB) UpsertUserOnLogin(githubID int64, login string) (User, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return User{}, fmt.Errorf("store: begin: %w", err)
	}
	defer tx.Rollback()

	now := formatTime(time.Now())

	// 1. Existing user matched by github_id.
	var u User
	var ca, ua timeScanner
	err = tx.QueryRow(
		"SELECT id, github_id, github_login, role, status, created_at, updated_at FROM users WHERE github_id = ?",
		githubID,
	).Scan(&u.ID, &u.GitHubID, &u.Login, &u.Role, &u.Status, &ca, &ua)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return User{}, fmt.Errorf("store: lookup by github_id: %w", err)
	}
	if err == nil {
		newStatus := u.Status
		if newStatus == "invited" {
			newStatus = "active"
		}
		if _, err := tx.Exec(
			"UPDATE users SET github_login = ?, status = ?, updated_at = ? WHERE id = ?",
			login, newStatus, now, u.ID,
		); err != nil {
			return User{}, fmt.Errorf("store: update existing user: %w", err)
		}
		u.Login, u.Status = login, newStatus
		u.CreatedAt, u.UpdatedAt = ca.Time, time.Now().UTC()
		return u, tx.Commit()
	}

	// 2. Invited user matched by login (case-insensitive).
	var inv User
	var ic, iu timeScanner
	err = tx.QueryRow(
		"SELECT id, github_id, github_login, role, status, created_at, updated_at FROM users WHERE LOWER(github_login) = LOWER(?) AND status = 'invited'",
		login,
	).Scan(&inv.ID, &inv.GitHubID, &inv.Login, &inv.Role, &inv.Status, &ic, &iu)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return User{}, fmt.Errorf("store: lookup by login: %w", err)
	}
	if err == nil {
		if _, err := tx.Exec(
			"UPDATE users SET github_id = ?, github_login = ?, status = 'active', updated_at = ? WHERE id = ?",
			githubID, login, now, inv.ID,
		); err != nil {
			return User{}, fmt.Errorf("store: bind invited user: %w", err)
		}
		inv.GitHubID, inv.Login, inv.Status = githubID, login, "active"
		inv.CreatedAt, inv.UpdatedAt = ic.Time, time.Now().UTC()
		return inv, tx.Commit()
	}

	return User{}, ErrNotAllowed
}

func (s *DB) GetUserByGitHubID(id int64) (User, bool, error) {
	var u User
	var ca, ua timeScanner
	err := s.db.QueryRow(
		"SELECT id, github_id, github_login, role, status, created_at, updated_at FROM users WHERE github_id = ?",
		id,
	).Scan(&u.ID, &u.GitHubID, &u.Login, &u.Role, &u.Status, &ca, &ua)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, fmt.Errorf("store: get user by github_id: %w", err)
	}
	u.CreatedAt, u.UpdatedAt = ca.Time, ua.Time
	return u, true, nil
}

func (s *DB) GetUserByID(id int64) (User, bool, error) {
	var u User
	var ca, ua timeScanner
	err := s.db.QueryRow(
		"SELECT id, github_id, github_login, role, status, created_at, updated_at FROM users WHERE id = ?",
		id,
	).Scan(&u.ID, &u.GitHubID, &u.Login, &u.Role, &u.Status, &ca, &ua)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, fmt.Errorf("store: get user by id: %w", err)
	}
	u.CreatedAt, u.UpdatedAt = ca.Time, ua.Time
	return u, true, nil
}

// InviteUser creates an invited user row. If the login already exists (any
// status), the existing row is returned unchanged (idempotent).
//
// Invited rows carry a negative sentinel github_id (= -(rowid)) because
// github_id is NOT NULL UNIQUE and the real id is unknown before first login.
// UpsertUserOnLogin replaces the sentinel with the real id on first login.
func (s *DB) InviteUser(login, role string) (User, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return User{}, fmt.Errorf("store: begin: %w", err)
	}
	defer tx.Rollback()

	// Idempotent: return existing row.
	var existing User
	var ec, eu timeScanner
	err = tx.QueryRow(
		"SELECT id, github_id, github_login, role, status, created_at, updated_at FROM users WHERE LOWER(github_login) = LOWER(?)",
		login,
	).Scan(&existing.ID, &existing.GitHubID, &existing.Login, &existing.Role, &existing.Status, &ec, &eu)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return User{}, fmt.Errorf("store: check existing: %w", err)
	}
	if err == nil {
		existing.CreatedAt, existing.UpdatedAt = ec.Time, eu.Time
		return existing, tx.Commit()
	}

	now := formatTime(time.Now())
	// Insert with temporary github_id=0, then update to -rowid.
	// MaxOpenConns=1 ensures no concurrent row holds github_id=0 simultaneously.
	res, err := tx.Exec(
		"INSERT INTO users (github_id, github_login, role, status, created_at, updated_at) VALUES (0, ?, ?, 'invited', ?, ?)",
		login, role, now, now,
	)
	if err != nil {
		return User{}, fmt.Errorf("store: insert invited: %w", err)
	}
	id, _ := res.LastInsertId()
	if _, err := tx.Exec("UPDATE users SET github_id = ? WHERE id = ?", -id, id); err != nil {
		return User{}, fmt.Errorf("store: set sentinel github_id: %w", err)
	}
	u := User{
		ID: id, GitHubID: -id, Login: login, Role: role, Status: "invited",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	return u, tx.Commit()
}

func (s *DB) ListUsers() ([]User, error) {
	rows, err := s.db.Query(
		"SELECT id, github_id, github_login, role, status, created_at, updated_at FROM users ORDER BY id",
	)
	if err != nil {
		return nil, fmt.Errorf("store: list users: %w", err)
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		var u User
		var ca, ua timeScanner
		if err := rows.Scan(&u.ID, &u.GitHubID, &u.Login, &u.Role, &u.Status, &ca, &ua); err != nil {
			return nil, fmt.Errorf("store: scan user: %w", err)
		}
		u.CreatedAt, u.UpdatedAt = ca.Time, ua.Time
		users = append(users, u)
	}
	return users, rows.Err()
}

func (s *DB) SetUserStatus(id int64, status string) error {
	_, err := s.db.Exec(
		"UPDATE users SET status = ?, updated_at = ? WHERE id = ?",
		status, formatTime(time.Now()), id,
	)
	return err
}

// --- slots ---

func (s *DB) ListSlots() ([]Slot, error) {
	rows, err := s.db.Query("SELECT " + slotCols + " FROM slots ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("store: list slots: %w", err)
	}
	defer rows.Close()
	var slots []Slot
	for rows.Next() {
		sl, err := scanSlot(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan slot: %w", err)
		}
		slots = append(slots, sl)
	}
	return slots, rows.Err()
}

func (s *DB) GetSlot(id string) (Slot, bool, error) {
	row := s.db.QueryRow("SELECT "+slotCols+" FROM slots WHERE id = ?", id)
	sl, err := scanSlot(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Slot{}, false, nil
	}
	if err != nil {
		return Slot{}, false, fmt.Errorf("store: get slot: %w", err)
	}
	return sl, true, nil
}

// UpsertSlot inserts a slot or updates its configuration fields on conflict.
// The status column is NOT updated on conflict — it is managed exclusively by
// AssignFreeSlot / AssignSlot and operator admin actions. This ensures that a
// registry reload (admin "reload slots") never reverts an assigned or disabled
// slot back to free.
func (s *DB) UpsertSlot(sl Slot) error {
	status := sl.Status
	if status == "" {
		status = "free"
	}
	_, err := s.db.Exec(`
		INSERT INTO slots (`+slotCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			os_user=excluded.os_user,
			docker_host=excluded.docker_host,
			network=excluded.network,
			base_url=excluded.base_url,
			internal_addr=excluded.internal_addr,
			cpu_limit=excluded.cpu_limit,
			mem_limit=excluded.mem_limit,
			max_runners=excluded.max_runners,
			admin_token=excluded.admin_token`,
		sl.ID, sl.OSUser, sl.DockerHost, sl.Network, sl.BaseURL, sl.InternalAddr,
		sl.CPULimit, sl.MemLimit, sl.MaxRunners, status, sl.AdminToken,
	)
	if err != nil {
		return fmt.Errorf("store: upsert slot: %w", err)
	}
	return nil
}

// --- assignments ---

// AssignFreeSlot atomically picks a free slot and binds it to userID.
// With SetMaxOpenConns(1), competing goroutines serialize at the pool level:
// the second caller sees committed state from the first, so it can never
// receive a slot that was already assigned.
func (s *DB) AssignFreeSlot(userID int64) (Assignment, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Assignment{}, fmt.Errorf("store: begin: %w", err)
	}
	defer tx.Rollback()

	var slotID string
	if err := tx.QueryRow(
		"SELECT id FROM slots WHERE status = 'free' ORDER BY id LIMIT 1",
	).Scan(&slotID); errors.Is(err, sql.ErrNoRows) {
		return Assignment{}, ErrNoFreeSlot
	} else if err != nil {
		return Assignment{}, fmt.Errorf("store: find free slot: %w", err)
	}

	return s.doAssign(tx, userID, slotID)
}

// AssignSlot binds userID to a specific slotID. If the user already has an
// assignment, it is freed first (old slot returned to "free", old row removed)
// so reassignment works — assignments has UNIQUE(user_id), so a plain insert
// would otherwise fail for an already-assigned user.
func (s *DB) AssignSlot(userID int64, slotID string) (Assignment, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Assignment{}, fmt.Errorf("store: begin: %w", err)
	}
	defer tx.Rollback()
	if err := freeExistingAssignment(tx, userID); err != nil {
		return Assignment{}, err
	}
	return s.doAssign(tx, userID, slotID)
}

// freeExistingAssignment removes the user's current assignment (if any) and
// returns its slot to "free", within the given transaction.
func freeExistingAssignment(tx *sql.Tx, userID int64) error {
	var oldSlot string
	err := tx.QueryRow("SELECT slot_id FROM assignments WHERE user_id = ?", userID).Scan(&oldSlot)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("store: lookup existing assignment: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM assignments WHERE user_id = ?", userID); err != nil {
		return fmt.Errorf("store: delete old assignment: %w", err)
	}
	if _, err := tx.Exec("UPDATE slots SET status = 'free' WHERE id = ?", oldSlot); err != nil {
		return fmt.Errorf("store: free old slot: %w", err)
	}
	return nil
}

func (s *DB) doAssign(tx *sql.Tx, userID int64, slotID string) (Assignment, error) {
	now := formatTime(time.Now())
	if _, err := tx.Exec(
		"INSERT INTO assignments (user_id, slot_id, gharp_state, assigned_at) VALUES (?, ?, 'stopped', ?)",
		userID, slotID, now,
	); err != nil {
		return Assignment{}, fmt.Errorf("store: insert assignment: %w", err)
	}
	if _, err := tx.Exec(
		"UPDATE slots SET status = 'assigned' WHERE id = ?", slotID,
	); err != nil {
		return Assignment{}, fmt.Errorf("store: mark slot assigned: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Assignment{}, fmt.Errorf("store: commit assignment: %w", err)
	}
	return Assignment{UserID: userID, SlotID: slotID, GharpState: "stopped", AssignedAt: time.Now().UTC()}, nil
}

func (s *DB) GetAssignmentByUser(userID int64) (Assignment, bool, error) {
	var a Assignment
	var at timeScanner
	err := s.db.QueryRow(
		"SELECT user_id, slot_id, gharp_state, assigned_at FROM assignments WHERE user_id = ?",
		userID,
	).Scan(&a.UserID, &a.SlotID, &a.GharpState, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return Assignment{}, false, nil
	}
	if err != nil {
		return Assignment{}, false, fmt.Errorf("store: get assignment: %w", err)
	}
	a.AssignedAt = at.Time
	return a, true, nil
}

func (s *DB) ListAssignments() ([]Assignment, error) {
	rows, err := s.db.Query("SELECT user_id, slot_id, gharp_state, assigned_at FROM assignments ORDER BY user_id")
	if err != nil {
		return nil, fmt.Errorf("store: list assignments: %w", err)
	}
	defer rows.Close()
	var as []Assignment
	for rows.Next() {
		var a Assignment
		var at timeScanner
		if err := rows.Scan(&a.UserID, &a.SlotID, &a.GharpState, &at); err != nil {
			return nil, fmt.Errorf("store: scan assignment: %w", err)
		}
		a.AssignedAt = at.Time
		as = append(as, a)
	}
	return as, rows.Err()
}

func (s *DB) SetGharpState(userID int64, state string) error {
	_, err := s.db.Exec(
		"UPDATE assignments SET gharp_state = ? WHERE user_id = ?", state, userID,
	)
	return err
}

// --- sessions ---

func (s *DB) CreateSession(userID int64, ttl time.Duration) (Session, error) {
	token, err := generateToken()
	if err != nil {
		return Session{}, err
	}
	csrf, err := generateToken()
	if err != nil {
		return Session{}, err
	}
	expiresAt := time.Now().UTC().Add(ttl)
	if _, err := s.db.Exec(
		"INSERT INTO sessions (token, user_id, csrf, expires_at) VALUES (?, ?, ?, ?)",
		token, userID, csrf, formatTime(expiresAt),
	); err != nil {
		return Session{}, fmt.Errorf("store: create session: %w", err)
	}
	return Session{Token: token, UserID: userID, CSRF: csrf, ExpiresAt: expiresAt}, nil
}

func (s *DB) GetSession(token string) (Session, bool, error) {
	var sess Session
	var exp timeScanner
	err := s.db.QueryRow(
		"SELECT token, user_id, csrf, expires_at FROM sessions WHERE token = ?",
		token,
	).Scan(&sess.Token, &sess.UserID, &sess.CSRF, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, fmt.Errorf("store: get session: %w", err)
	}
	sess.ExpiresAt = exp.Time
	if time.Now().UTC().After(sess.ExpiresAt) {
		_ = s.DeleteSession(token)
		return Session{}, false, nil
	}
	return sess, true, nil
}

func (s *DB) DeleteSession(token string) error {
	_, err := s.db.Exec("DELETE FROM sessions WHERE token = ?", token)
	return err
}

// --- audit ---

func (s *DB) Audit(actorID int64, action, target, detail string) error {
	_, err := s.db.Exec(
		"INSERT INTO audit_log (actor_id, action, target, detail) VALUES (?, ?, ?, ?)",
		actorID, action, target, detail,
	)
	return err
}

func (s *DB) ListAuditLog(limit int) ([]AuditEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(
		"SELECT id, actor_id, action, target, detail, at FROM audit_log ORDER BY id DESC LIMIT ?",
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list audit log: %w", err)
	}
	defer rows.Close()
	var es []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var actor sql.NullInt64
		var at timeScanner
		if err := rows.Scan(&e.ID, &actor, &e.Action, &e.Target, &e.Detail, &at); err != nil {
			return nil, fmt.Errorf("store: scan audit entry: %w", err)
		}
		e.ActorID = actor.Int64
		e.At = at.Time
		es = append(es, e)
	}
	return es, rows.Err()
}
