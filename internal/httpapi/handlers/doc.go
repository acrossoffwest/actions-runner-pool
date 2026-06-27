// Package handlers provides the HTTP handlers for the gharp admin API.
//
// # Templates (WS-F redesign — dashboard.html, setup.html, setup_done.html)
//
// dashboard.html is the operational console. It polls GET /stats and
// GET /jobs every 10 seconds, diffing rows to avoid full-table flashes.
// No build step required — vanilla JS and a single tokens.css.
//
// Design properties:
//   - Dark mode default-to-system via CSS @media (prefers-color-scheme: dark).
//   - Colorblind-safe status system: every state has a distinct dot shape,
//     hue, AND text label (pending/dispatched/in_progress/completed/failure).
//   - Org/account grouping: first row of each owner group shows the org name.
//   - Job detail drawer: slide-in panel with lifecycle timeline, runner metadata,
//     and a GitHub Actions deep-link.
//   - Skeleton placeholders on first load; live-pulse indicator while polling.
//   - Read-only mode: when ALLOW_ADMIN_EDIT=false, action buttons are disabled
//     server-side (Go template) AND client-side (JS allowAdminEdit flag).
//
// setup.html / setup_done.html are the GitHub App creation and install flow.
//
// # Shared design tokens
//
// css/tokens.css exports CSS custom properties for use by both this package
// and the Portal UI (WS-E). Variable names follow --g-<category>[-<variant>].
// Status triples: --g-status-<state>-{bg,border,text,dot}.
// WS-E should import /css/tokens.css and build Portal components on top.
//
// # Backend compatibility
//
// No gharp Go handler code was changed. The templates wire to the existing
// endpoints verbatim:
//
//   GET /stats                         → renderStats
//   GET /jobs?status=&repo=&limit=     → renderJobs
//   POST /jobs/{id}/retry              → actionBtn
//   POST /jobs/{id}/cancel             → actionBtn
//   PATCH /admin/app-config            → wireRotateForm
package handlers
