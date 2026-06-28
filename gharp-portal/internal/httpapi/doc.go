// Package httpapi implements the gharp Portal HTTP router, handlers, and
// server-rendered UI templates (WS-E).
//
// # Overview
//
// Call NewRouter(Deps) to obtain an http.Handler that covers all routes
// defined in spec §10. The Deps struct accepts narrow local interfaces for
// auth, store, lifecycle, proxy, and slot-reloading — real implementations
// are wired by the lead in cmd/portal/main.go; tests supply hand-written mocks.
//
// # Route map
//
//	Public:
//	  GET /                 → login page or redirect by role
//	  GET /login            → login page
//	  GET /healthz          → portal health check
//	  POST /auth/logout     → clear session + redirect
//	  GET /auth/start       → (WS-B mounts)
//	  GET /auth/callback    → (WS-B mounts)
//
//	User (RequireUser middleware):
//	  GET /app              → user proxy shell (start/stop controls + dashboard)
//	  POST /app/start       → Lifecycle.Start(userID)        [CSRF required]
//	  POST /app/stop        → Lifecycle.Stop(userID)         [CSRF required]
//	  ANY /app/*            → reverse proxy to user's gharp
//
//	Admin (RequireAdmin middleware):
//	  GET  /admin                          → console (users + slots + audit tail)
//	  POST /admin/users                    → invite user         [CSRF required]
//	  POST /admin/users/{id}/status        → enable/disable      [CSRF required]
//	  POST /admin/users/{id}/assign        → assign slot         [CSRF required]
//	  POST /admin/slots/reload             → reload slots.yaml   [CSRF required]
//	  GET  /admin/audit                    → full audit log
//
// # Context contract with WS-B
//
// WS-B's RequireUser and RequireAdmin middleware must call
// httpapi.SetUserInContext and httpapi.SetCSRFInContext before invoking the
// next handler. Handlers retrieve these via UserFromContext / CSRFFromContext.
//
// # CSRF
//
// State-changing methods (POST/PUT/PATCH/DELETE) are wrapped with requireCSRF,
// which reads the expected token from context and compares it to the
// X-CSRF-Token request header or the _csrf form field.
//
// # Design tokens
//
// templates/static/tokens.css is the shared CSS design system (color, space,
// type, motion tokens). WS-F's gharp dashboard redesign must import this file
// and use the same variable names documented at the top of that file.
//
// # Dependencies
//
//   - Standard library only (net/http, html/template, embed, io/fs).
//   - No external packages.
package httpapi
