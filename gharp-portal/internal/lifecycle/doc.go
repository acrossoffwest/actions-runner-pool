// Package lifecycle controls the start/stop/health of per-user gharp instances.
//
// # What it does
//
// Manager drives the gharp_state field in the assignments table through its
// allowed transitions (stopped → starting → running / error) by invoking a
// single, hard-coded, allow-listed OS command per operation:
//
//   - Start: "start-gharp <slot-id>"
//   - Stop:  "stop-gharp <slot-id>"
//
// After Start invokes the command it polls GET <slot.internal_addr>/healthz
// until the instance is up (default 30 s timeout, 1 s interval) and then sets
// state to "running". Any failure leaves state at "error".
//
// Health() polls /healthz for all running/starting slots and sets state to
// "error" on failure; it is intended to be called on a periodic schedule by
// the HTTP API layer.
//
// # How to use
//
//	mgr := lifecycle.New(storeAdapter, lifecycle.OSCommandRunner{}, &http.Client{Timeout: 5*time.Second})
//	// optional: mgr.WithPollConfig(30*time.Second, time.Second)
//	if err := mgr.Start(ctx, userID); err != nil { ... }
//	if err := mgr.Stop(ctx, userID); err != nil { ... }
//	states, err := mgr.Health(ctx)
//
// # Dependencies
//
//   - Store interface (this package): GetAssignmentByUser, GetSlot, SetGharpState,
//     ListAssignments. The concrete WS-A store adapter satisfies this interface.
//     NOTE: WS-A must add ListAssignments() to the shared store.Store interface
//     (it is absent from the §6 contract) before Health() can be wired end-to-end.
//
//   - CommandRunner interface (this package): production code passes OSCommandRunner;
//     tests pass a hand-written mock. The runner MUST receive ONLY the allow-listed
//     command name and a validated slot-id (alphanumeric + dashes, ≤ 63 chars).
//     No shell expansion, no concatenated strings, no arbitrary user input.
//
//   - *http.Client: passed by the caller; use a client with a short timeout
//     (e.g. 5 s) in production.
package lifecycle
