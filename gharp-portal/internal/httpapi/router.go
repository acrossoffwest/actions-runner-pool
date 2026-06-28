package httpapi

import (
	"net/http"
)

// NewRouter constructs the Portal HTTP handler with all routes wired.
// Callers (cmd/portal) provide real implementations via Deps;
// tests supply mocks.
func NewRouter(d Deps) http.Handler {
	mux := http.NewServeMux()

	// Static assets (design tokens, future JS).
	mux.Handle("/static/", staticHandler())

	// ---- Public routes ----
	mux.HandleFunc("GET /{$}", handleRoot(d)) // exact "/" only
	mux.HandleFunc("GET /login", handleLogin)
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.HandleFunc("POST /auth/logout", handleLogout(d))

	// ---- Auth routes — mounted by WS-B in main.go ----
	// /auth/start and /auth/callback are registered by the auth package
	// and passed in via Deps.AuthMux if needed; for now we leave that gap
	// for WS-B to fill at integration time.

	// ---- User routes (RequireUser) ----
	requireUser := d.RequireUser

	mux.Handle("GET /app", requireUser(handleAppShell(d)))
	mux.Handle("POST /app/start", requireUser(requireCSRF(handleAppStart(d))))
	mux.Handle("POST /app/stop", requireUser(requireCSRF(handleAppStop(d))))

	// Proxy catch-all: strip /app prefix before forwarding.
	// Registered last so exact routes above take precedence.
	mux.Handle("/app/", requireUser(http.StripPrefix("/app", d.Proxy)))

	// ---- Admin routes (RequireAdmin) ----
	requireAdmin := d.RequireAdmin

	mux.Handle("GET /admin", requireAdmin(handleAdminDashboard(d)))
	mux.Handle("POST /admin/users", requireAdmin(requireCSRF(handleAdminInvite(d))))
	mux.Handle("POST /admin/users/{id}/status", requireAdmin(requireCSRF(handleAdminUserStatus(d))))
	mux.Handle("POST /admin/users/{id}/assign", requireAdmin(requireCSRF(handleAdminAssignSlot(d))))
	mux.Handle("POST /admin/slots/reload", requireAdmin(requireCSRF(handleAdminSlotsReload(d))))
	mux.Handle("GET /admin/audit", requireAdmin(handleAdminAudit(d)))

	return mux
}
