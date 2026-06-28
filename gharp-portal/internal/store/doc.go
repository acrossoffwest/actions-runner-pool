// Package store defines the Portal's persistence contract (Store interface +
// domain structs) and provides a SQLite-backed implementation (DB).
//
// # Usage
//
//	db, err := store.Open("/path/to/portal.db")
//	if err != nil { /* startup-fatal */ }
//	defer db.Close()
//
//	// invite a user before their first login
//	u, err := db.InviteUser("alice", "user")
//
//	// bind github_id and activate on first OAuth callback
//	u, err = db.UpsertUserOnLogin(githubID, githubLogin)
//
//	// assign a slot atomically
//	a, err := db.AssignFreeSlot(u.ID)
//
// # Isolation contract for other workstreams
//
// Never import the concrete store package from other workstreams. Instead
// define a narrow local interface with only the methods you need and test
// against a hand-written mock. This keeps builds decoupled so WS-A can
// evolve the SQLite internals without breaking consumers.
//
// # Thread safety
//
// DB is safe for concurrent use. MaxOpenConns=1 serializes writes through a
// single SQLite connection; this is what makes AssignFreeSlot atomic.
//
// # Dependencies
//
//   - modernc.org/sqlite — pure-Go SQLite driver (no cgo required)
package store
