// Package proxy provides an http.Handler that reverse-proxies an authenticated
// user's HTTP requests to their assigned gharp instance.
//
// # What it does
//
//   - Reads the requesting user's ID from context (set by auth middleware via WithUserID).
//   - Looks up the user's assignment and slot from Store.
//   - Strips the "/app" path prefix before forwarding (e.g. /app/runners → /runners).
//   - Strips any client-supplied Authorization header.
//   - Injects "Authorization: Bearer <slot.AdminToken>" server-side so the user
//     never sees or handles the gharp ADMIN_TOKEN.
//   - Returns 401 if no userID is in context, 403 if no slot is assigned,
//     500 on store errors.
//   - Disables proxy response buffering (FlushInterval: -1) so SSE and gharp's
//     10 s long-poll work correctly end-to-end.
//
// # How to use
//
//	// WS-E wires this into the router under /app/*:
//	h := proxy.New(storeAdapter)
//	mux.Handle("/app/", authMiddleware(h))
//
//	// Auth middleware must inject the authenticated user's ID:
//	ctx := proxy.WithUserID(r.Context(), userID)
//	h.ServeHTTP(w, r.WithContext(ctx))
//
// # Dependencies
//
//   - Store interface (this package): GetAssignmentByUser, GetSlot.
//     NOTE: Slot.AdminToken is not yet in the §6 store.Slot struct; WS-A must add
//     it (or provide it via a companion secrets store) before production wiring.
//
//   - Auth middleware (WS-B/E): must call proxy.WithUserID before forwarding to
//     Handler.ServeHTTP.
package proxy
