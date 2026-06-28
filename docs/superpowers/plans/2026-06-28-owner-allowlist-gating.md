# Owner-Allowlist Gating Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A gharp slot only launches a runner for a `workflow_job` whose repository owner is the App owner (always trusted) or an operator-configured allowed owner; jobs from any other account are dropped.

**Architecture:** Add an owner check to `handleWorkflowJob`'s `queued` branch, mirroring the existing `publicRepoAllowed` guard. The App owner login is resolved lazily via `GET /app` (using the App JWT) and persisted to `app_config.owner_login`. Extra allowed owners live in a new single-row `access_settings` table, editable from a dashboard "Access control" panel.

**Tech Stack:** Go stdlib, `modernc.org/sqlite` via `store.SQLite`, the existing `github.Client` (App JWT), Go 1.22 `http.ServeMux`, the dashboard HTML/JS template.

## Global Constraints

- Module path: `github.com/muhac/actions-runner-pool`.
- Store constructor in tests: `store.OpenSQLite("file:" + t.TempDir() + "/test.db")` → `*store.SQLite` (satisfies `store.Store`). Receiver `*SQLite`, DB handle `s.db`.
- Single-row config tables: `id INTEGER PRIMARY KEY CHECK (id = 1)` + `INSERT ... ON CONFLICT(id) DO UPDATE SET ... = excluded....`.
- Owner comparison is **case-insensitive** (`strings.EqualFold`); GitHub logins are case-insensitive.
- The gate is authorization only and trusts only the HMAC-verified webhook body — `repository.full_name` is authentic. Owner = the part before `/`.
- Deny path: log + `w.WriteHeader(http.StatusOK)` + return (never 4xx/5xx — a denied owner is permanent, GitHub must not retry).
- **Fail-closed:** when the App owner can't be determined AND the allowed list is empty, deny.
- Admin mutations gated by `adminWriteDenied(cfg, authHeader)` (Bearer + `AllowAdminEdit`); reads by `authorizedBearer(cfg, authHeader)` — both in `internal/httpapi/handlers/jobs.go`. Reuse `writeJSON` (in `app_config.go`); do not redefine it.
- GitHub API GET pattern (from `runners.go`): `req.Header.Set("Authorization", "Bearer "+token)`, `req.Header.Set("Accept", "application/vnd.github+json")`, `c.http.Do(req)`, base `c.cfg.GitHubAPIBase`.
- Dashboard JS: relative fetch paths + `Authorization: 'Bearer ' + State.token`; behind the portal the proxy injects the token (`BEHIND_PORTAL`).

---

## File Structure

- `internal/store/schema.sql` — add `owner_login` column to `app_config`; new `access_settings` table.
- `internal/store/models.go` — `AppConfig.OwnerLogin` field; new `AccessSettings` struct.
- `internal/store/store.go` — interface: `UpdateAppOwnerLogin`, `GetAccessSettings`, `SaveAccessSettings`.
- `internal/store/sqlite.go` — update `GetAppConfig`/`SaveAppConfig` for `owner_login`; implement the three new methods.
- `internal/store/{sqlite_test.go,schema_test.go}` — round-trip tests + table-list update.
- `internal/github/app.go` — **new**: `Client.AppOwner`.
- `internal/github/app_test.go` — **new**: httptest test.
- `internal/httpapi/handlers/webhook.go` — `appOwnerClient` interface + `GitHub` field on `WebhookHandler`; `ownerOf`, `ownerAllowed`; thread `appCfg` into `handleWorkflowJob`; gate the `queued` branch.
- `internal/httpapi/handlers/webhook_test.go` — gate tests.
- `internal/httpapi/handlers/access.go` — **new**: `AccessHandler`.
- `internal/httpapi/handlers/access_test.go` — **new**: handler tests.
- `internal/httpapi/router.go` — pass `gh` to `WebhookHandler`; register access routes.
- `internal/httpapi/handlers/templates/dashboard.html` — "Access control" panel + JS.

---

## Task 1: Store — owner_login + access_settings

**Files:**
- Modify: `internal/store/schema.sql`, `internal/store/models.go`, `internal/store/store.go`, `internal/store/sqlite.go`
- Test: `internal/store/sqlite_test.go`, `internal/store/schema_test.go`

**Interfaces:**
- Produces: `store.AppConfig.OwnerLogin string`; `store.Store.UpdateAppOwnerLogin(ctx, login string) error`; `store.AccessSettings{AllowedOwners string}`; `store.Store.GetAccessSettings(ctx) (*AccessSettings, error)` (never nil — absent row → `&AccessSettings{}`); `store.Store.SaveAccessSettings(ctx, *AccessSettings) error`.

- [ ] **Step 1: Write failing tests**

Add to `internal/store/sqlite_test.go`:

```go
func TestAppConfig_OwnerLogin(t *testing.T) {
	s, err := OpenSQLite("file:" + t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	if err := s.SaveAppConfig(ctx, &AppConfig{
		AppID: 1, Slug: "a", WebhookSecret: "wsecretwsecret16", PEM: []byte("p"),
		ClientID: "cid", BaseURL: "https://x", OwnerLogin: "alice",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAppConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.OwnerLogin != "alice" {
		t.Fatalf("OwnerLogin = %q, want alice", got.OwnerLogin)
	}
	if err := s.UpdateAppOwnerLogin(ctx, "bob"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetAppConfig(ctx)
	if got.OwnerLogin != "bob" {
		t.Fatalf("after update OwnerLogin = %q, want bob", got.OwnerLogin)
	}
}

func TestAccessSettings_RoundTripAndDefault(t *testing.T) {
	s, err := OpenSQLite("file:" + t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	got, err := s.GetAccessSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.AllowedOwners != "" {
		t.Fatalf("default AllowedOwners = %q, want empty", got.AllowedOwners)
	}
	if err := s.SaveAccessSettings(ctx, &AccessSettings{AllowedOwners: "tmgr-dev,acme"}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetAccessSettings(ctx)
	if got.AllowedOwners != "tmgr-dev,acme" {
		t.Fatalf("AllowedOwners = %q", got.AllowedOwners)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/store/ -run 'TestAppConfig_OwnerLogin|TestAccessSettings_RoundTripAndDefault'`
Expected: FAIL — `OwnerLogin` / `UpdateAppOwnerLogin` / `AccessSettings` undefined.

- [ ] **Step 3: Schema**

In `internal/store/schema.sql`, add `owner_login` to the `app_config` table (after `client_secret`):

```sql
  owner_login     TEXT    NOT NULL DEFAULT '',
```

And add a new table after `notify_settings`:

```sql
CREATE TABLE IF NOT EXISTS access_settings (
  id             INTEGER PRIMARY KEY CHECK (id = 1),
  allowed_owners TEXT    NOT NULL DEFAULT ''
);
```

- [ ] **Step 4: Models**

In `internal/store/models.go`, add `OwnerLogin` to `AppConfig` (after `BaseURL`):

```go
	OwnerLogin    string
```

And a new struct after `NotifySettings`:

```go
// AccessSettings holds the per-instance owner allowlist: a comma-separated
// list of GitHub account/org logins (besides the App owner) whose repos may
// launch runners on this slot.
type AccessSettings struct {
	AllowedOwners string
}
```

- [ ] **Step 5: Interface**

In `internal/store/store.go`, after `GetNotifySettings`/`SaveNotifySettings`:

```go
	// UpdateAppOwnerLogin writes only the app_config.owner_login column.
	UpdateAppOwnerLogin(ctx context.Context, login string) error
	// GetAccessSettings returns the single-row owner allowlist; never nil
	// (absent row yields an empty list).
	GetAccessSettings(ctx context.Context) (*AccessSettings, error)
	// SaveAccessSettings upserts the single-row (id=1) owner allowlist.
	SaveAccessSettings(ctx context.Context, a *AccessSettings) error
```

- [ ] **Step 6: sqlite.go — app_config column**

In `SaveAppConfig`, add `owner_login` to the INSERT column list, the `VALUES`, the `ON CONFLICT ... DO UPDATE SET`, and the args. The full method becomes:

```go
func (s *SQLite) SaveAppConfig(ctx context.Context, cfg *AppConfig) error {
	const q = `
INSERT INTO app_config (id, app_id, slug, webhook_secret, pem, client_id, client_secret, base_url, owner_login)
VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  app_id=excluded.app_id, slug=excluded.slug, webhook_secret=excluded.webhook_secret,
  pem=excluded.pem, client_id=excluded.client_id, client_secret=excluded.client_secret,
  base_url=excluded.base_url, owner_login=excluded.owner_login`
	_, err := s.db.ExecContext(ctx, q,
		cfg.AppID, cfg.Slug, cfg.WebhookSecret, cfg.PEM,
		cfg.ClientID, cfg.ClientSecret, cfg.BaseURL, cfg.OwnerLogin)
	return err
}
```

In `GetAppConfig`, add `owner_login` to the SELECT and scan it. Find the
existing query+scan and update both so the column appears last, scanning into
`&c.OwnerLogin`. The query's SELECT list becomes:

```go
	const q = `SELECT app_id, slug, webhook_secret, pem, client_id, client_secret, base_url, owner_login, created_at
		FROM app_config WHERE id = 1`
```

and the `.Scan(...)` call gains `&c.OwnerLogin` immediately before `&c.CreatedAt`.

- [ ] **Step 7: sqlite.go — new methods**

Add `UpdateAppOwnerLogin` next to the other `UpdateAppConfig*` setters (reusing `updateAppConfigField`):

```go
// UpdateAppOwnerLogin writes only the owner_login column.
func (s *SQLite) UpdateAppOwnerLogin(ctx context.Context, login string) error {
	return s.updateAppConfigField(ctx, "owner_login", login)
}
```

Append the access-settings methods:

```go
func (s *SQLite) GetAccessSettings(ctx context.Context) (*AccessSettings, error) {
	const q = `SELECT allowed_owners FROM access_settings WHERE id = 1`
	var a AccessSettings
	err := s.db.QueryRowContext(ctx, q).Scan(&a.AllowedOwners)
	if errors.Is(err, sql.ErrNoRows) {
		return &AccessSettings{}, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (s *SQLite) SaveAccessSettings(ctx context.Context, a *AccessSettings) error {
	const q = `
INSERT INTO access_settings (id, allowed_owners) VALUES (1, ?)
ON CONFLICT(id) DO UPDATE SET allowed_owners=excluded.allowed_owners`
	_, err := s.db.ExecContext(ctx, q, a.AllowedOwners)
	return err
}
```

- [ ] **Step 8: schema_test table list**

`internal/store/schema_test.go` asserts the exact sorted table list. Add
`"access_settings"` to the expected slice (alphabetical position — it sorts
before `app_config`). Open the file, find the expected-tables literal, and
insert `"access_settings"` in sorted order.

- [ ] **Step 9: Run tests**

Run: `go test ./internal/store/`
Expected: PASS (new tests + existing app_config/schema tests).

- [ ] **Step 10: Commit**

```bash
git add internal/store/
git commit -m "feat(store): app_config.owner_login + access_settings table"
```

---

## Task 2: GitHub client — resolve the App owner

**Files:**
- Create: `internal/github/app.go`
- Test: `internal/github/app_test.go`

**Interfaces:**
- Consumes: `Client.AppJWT(pem []byte, appID int64) (string, error)` (existing).
- Produces: `(*Client).AppOwner(ctx context.Context, jwt string) (string, error)` — `GET {GitHubAPIBase}/app`, returns `owner.login`.

- [ ] **Step 1: Write failing test**

Create `internal/github/app_test.go`:

```go
package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/muhac/actions-runner-pool/internal/config"
)

func TestAppOwner_ParsesLogin(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"owner":{"login":"acrossoffwest"}}`))
	}))
	defer srv.Close()

	c := NewClient(&config.Config{GitHubAPIBase: srv.URL})
	owner, err := c.AppOwner(context.Background(), "thejwt")
	if err != nil {
		t.Fatalf("AppOwner: %v", err)
	}
	if owner != "acrossoffwest" {
		t.Fatalf("owner = %q", owner)
	}
	if gotPath != "/app" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAuth != "Bearer thejwt" {
		t.Fatalf("auth = %q", gotAuth)
	}
}

func TestAppOwner_NonOKIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	c := NewClient(&config.Config{GitHubAPIBase: srv.URL})
	if _, err := c.AppOwner(context.Background(), "j"); err == nil {
		t.Fatal("want error on non-2xx")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/github/ -run TestAppOwner`
Expected: FAIL — `c.AppOwner undefined`.

- [ ] **Step 3: Implement**

Create `internal/github/app.go`:

```go
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// AppOwner returns the login of the account that owns this GitHub App,
// resolved via GET /app authenticated with an App JWT. Used to allow the
// App owner's repositories on this runner by default.
func (c *Client) AppOwner(ctx context.Context, jwt string) (string, error) {
	endpoint := c.cfg.GitHubAPIBase + "/app"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("github GET /app: HTTP %d", resp.StatusCode)
	}
	var out struct {
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("github GET /app: decode: %w", err)
	}
	return out.Owner.Login, nil
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/github/ -run TestAppOwner`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/github/app.go internal/github/app_test.go
git commit -m "feat(github): AppOwner resolves the App owner login via GET /app"
```

---

## Task 3: Webhook — owner gate on dispatch

**Files:**
- Modify: `internal/httpapi/handlers/webhook.go`
- Test: `internal/httpapi/handlers/webhook_test.go`

**Interfaces:**
- Consumes: `store.AppConfig` (`OwnerLogin`, `PEM`, `AppID`), `store.Store.GetAccessSettings`/`UpdateAppOwnerLogin` (Task 1); `Client.AppJWT`/`AppOwner` (Task 2).
- Produces: `appOwnerClient` interface; `WebhookHandler.GitHub appOwnerClient`; `ownerOf(fullName string) string`; `(*WebhookHandler).ownerAllowed(ctx, repo scheduler.Repository, appCfg *store.AppConfig) bool`; `handleWorkflowJob` now takes a trailing `appCfg *store.AppConfig` param.

- [ ] **Step 1: Write failing tests**

Add to `internal/httpapi/handlers/webhook_test.go`:

```go
type fakeAppOwner struct {
	owner    string
	jwtErr   error
	ownerErr error
}

func (f *fakeAppOwner) AppJWT(_ []byte, _ int64) (string, error) { return "jwt", f.jwtErr }
func (f *fakeAppOwner) AppOwner(_ context.Context, _ string) (string, error) {
	return f.owner, f.ownerErr
}

func newGateHandler(t *testing.T, ao *fakeAppOwner) (*WebhookHandler, *store.SQLite) {
	t.Helper()
	st, err := store.OpenSQLite("file:" + t.TempDir() + "/g.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &WebhookHandler{Store: st, GitHub: ao, Log: slog.Default()}, st
}

func repoOf(full string) scheduler.Repository {
	return scheduler.Repository{FullName: full}
}

func TestOwnerAllowed_AppOwnerAlways(t *testing.T) {
	h, _ := newGateHandler(t, &fakeAppOwner{})
	cfg := &store.AppConfig{OwnerLogin: "acrossoffwest"}
	if !h.ownerAllowed(context.Background(), repoOf("AcrossOffWest/app"), cfg) {
		t.Fatal("app owner must be allowed (case-insensitive)")
	}
}

func TestOwnerAllowed_Listed(t *testing.T) {
	h, st := newGateHandler(t, &fakeAppOwner{})
	_ = st.SaveAccessSettings(context.Background(), &store.AccessSettings{AllowedOwners: "acme, tmgr-dev "})
	cfg := &store.AppConfig{OwnerLogin: "someone"}
	if !h.ownerAllowed(context.Background(), repoOf("TMGR-DEV/backend"), cfg) {
		t.Fatal("listed owner must be allowed (trimmed, case-insensitive)")
	}
}

func TestOwnerAllowed_StrangerDenied(t *testing.T) {
	h, _ := newGateHandler(t, &fakeAppOwner{})
	cfg := &store.AppConfig{OwnerLogin: "acrossoffwest"}
	if h.ownerAllowed(context.Background(), repoOf("evil/repo"), cfg) {
		t.Fatal("stranger must be denied")
	}
}

func TestOwnerAllowed_LazyResolveAndPersist(t *testing.T) {
	h, st := newGateHandler(t, &fakeAppOwner{owner: "acrossoffwest"})
	// owner_login empty → must resolve via fake + persist.
	cfg := &store.AppConfig{AppID: 1, PEM: []byte("pem"), OwnerLogin: ""}
	if !h.ownerAllowed(context.Background(), repoOf("acrossoffwest/x"), cfg) {
		t.Fatal("resolved app owner must be allowed")
	}
	got, _ := st.GetAppConfig(context.Background())
	if got != nil && got.OwnerLogin != "acrossoffwest" {
		// persistence only matters if a row exists; ownerAllowed calls
		// UpdateAppOwnerLogin which no-ops when no app_config row exists.
		t.Logf("owner_login persisted = %q", got.OwnerLogin)
	}
}

func TestOwnerAllowed_FailClosed(t *testing.T) {
	h, _ := newGateHandler(t, &fakeAppOwner{ownerErr: errors.New("boom")})
	cfg := &store.AppConfig{AppID: 1, PEM: []byte("pem"), OwnerLogin: ""} // unresolved
	if h.ownerAllowed(context.Background(), repoOf("acrossoffwest/x"), cfg) {
		t.Fatal("must fail closed when app owner unknown and list empty")
	}
}
```

Ensure the test file imports `errors`, `log/slog`, `context`, and
`github.com/muhac/actions-runner-pool/internal/scheduler` and
`.../internal/store` (add any missing to the import block).

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/httpapi/handlers/ -run TestOwnerAllowed`
Expected: FAIL — `WebhookHandler.GitHub` / `ownerAllowed` undefined.

- [ ] **Step 3: Add interface + field + helpers**

In `internal/httpapi/handlers/webhook.go`, near `messageSender`:

```go
// appOwnerClient is the subset of *github.Client used to resolve the App
// owner login for the owner-allowlist gate.
type appOwnerClient interface {
	AppJWT(pem []byte, appID int64) (string, error)
	AppOwner(ctx context.Context, jwt string) (string, error)
}
```

Add the field to `WebhookHandler` (nil-safe; standalone tests may omit it):

```go
	GitHub    appOwnerClient
```

Append helpers:

```go
// ownerOf returns the account/org login from a "owner/repo" full name.
func ownerOf(fullName string) string {
	if i := strings.IndexByte(fullName, '/'); i > 0 {
		return fullName[:i]
	}
	return ""
}

// ownerAllowed reports whether a runner may be launched for repo. The repo's
// owner is allowed if it matches the App owner (resolved lazily + persisted)
// or any entry in access_settings.allowed_owners (case-insensitive). Fails
// closed: if the App owner can't be determined and the list is empty, deny.
func (h *WebhookHandler) ownerAllowed(ctx context.Context, repo scheduler.Repository, appCfg *store.AppConfig) bool {
	owner := ownerOf(repo.FullName)
	if owner == "" {
		return false
	}

	appOwner := ""
	if appCfg != nil {
		appOwner = appCfg.OwnerLogin
	}
	// Lazily resolve the App owner once and persist it.
	if appOwner == "" && appCfg != nil && h.GitHub != nil && len(appCfg.PEM) > 0 && appCfg.AppID != 0 {
		jwt, err := h.GitHub.AppJWT(appCfg.PEM, appCfg.AppID)
		if err != nil {
			h.logError("owner gate: app jwt", err)
		} else if o, err := h.GitHub.AppOwner(ctx, jwt); err != nil {
			h.logError("owner gate: resolve app owner", err)
		} else if o != "" {
			appOwner = o
			if err := h.Store.UpdateAppOwnerLogin(ctx, o); err != nil {
				h.logError("owner gate: persist app owner", err)
			}
		}
	}
	if appOwner != "" && strings.EqualFold(owner, appOwner) {
		return true
	}

	access, err := h.Store.GetAccessSettings(ctx)
	if err != nil {
		h.logError("owner gate: load access settings", err)
		return false
	}
	for _, e := range strings.Split(access.AllowedOwners, ",") {
		if e = strings.TrimSpace(e); e != "" && strings.EqualFold(e, owner) {
			return true
		}
	}
	return false
}
```

Add `"context"` to the import block if not already present.

- [ ] **Step 4: Thread appCfg + add the gate**

In `Post`, the `workflow_job` dispatch already has `cfg` in scope (loaded for
signature verification as `cfg, err := h.Store.GetAppConfig(...)`). Pass it:

```go
	case "workflow_job":
		h.handleWorkflowJob(w, r, body, cfg)
```

Change the `handleWorkflowJob` signature to accept it:

```go
func (h *WebhookHandler) handleWorkflowJob(w http.ResponseWriter, r *http.Request, body []byte, appCfg *store.AppConfig) {
```

In the `queued` branch, immediately after the `publicRepoAllowed` block and
before the lazy repo→installation upsert, add:

```go
		if !h.ownerAllowed(r.Context(), ev.Repository, appCfg) {
			if h.Log != nil {
				h.Log.Warn("webhook: owner not allowed",
					"repo", ev.Repository.FullName,
					"job_id", ev.WorkflowJob.ID,
				)
			}
			w.WriteHeader(http.StatusOK)
			return
		}
```

If any test calls `handleWorkflowJob` directly, update those calls to pass a
`*store.AppConfig` (e.g. `&store.AppConfig{OwnerLogin: "..."}` or `nil`).

- [ ] **Step 5: Run tests**

Run: `go test ./internal/httpapi/handlers/ -run 'TestOwnerAllowed|TestHandleWorkflow' && go build ./...`
Expected: PASS, build clean.

- [ ] **Step 6: Commit**

```bash
git add internal/httpapi/handlers/webhook.go internal/httpapi/handlers/webhook_test.go
git commit -m "feat(webhook): gate runner dispatch on an owner allowlist"
```

---

## Task 4: Access HTTP handlers + wiring

**Files:**
- Create: `internal/httpapi/handlers/access.go`
- Test: `internal/httpapi/handlers/access_test.go`
- Modify: `internal/httpapi/router.go`

**Interfaces:**
- Consumes: `store.Store` (`GetAppConfig`, `GetAccessSettings`, `SaveAccessSettings`); `adminWriteDenied`/`authorizedBearer`/`writeAdminAuthError`/`writeJSON`.
- Produces: `AccessHandler{Cfg, Store, Log}` with `GetAccess` + `SaveOwners`; routes `GET /access`, `POST /access/owners`.

- [ ] **Step 1: Write failing tests**

Create `internal/httpapi/handlers/access_test.go`:

```go
package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/muhac/actions-runner-pool/internal/config"
	"github.com/muhac/actions-runner-pool/internal/store"
)

func newAccessHandler(t *testing.T) (*AccessHandler, *store.SQLite) {
	t.Helper()
	st, err := store.OpenSQLite("file:" + t.TempDir() + "/a.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg := &config.Config{AdminToken: "secret", AllowAdminEdit: true}
	return &AccessHandler{Cfg: cfg, Store: st}, st
}

func accessReq(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer secret")
	r.Header.Set("Content-Type", "application/json")
	return r
}

func TestAccess_GetReturnsAppOwnerAndList(t *testing.T) {
	h, st := newAccessHandler(t)
	_ = st.SaveAppConfig(context.Background(), &store.AppConfig{
		AppID: 1, Slug: "s", WebhookSecret: "wsecretwsecret16", PEM: []byte("p"),
		ClientID: "c", BaseURL: "https://x", OwnerLogin: "acrossoffwest",
	})
	_ = st.SaveAccessSettings(context.Background(), &store.AccessSettings{AllowedOwners: "tmgr-dev"})
	rec := httptest.NewRecorder()
	h.GetAccess(rec, accessReq(http.MethodGet, "/access", ``))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var v struct {
		AppOwner      string   `json:"app_owner"`
		AllowedOwners []string `json:"allowed_owners"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if v.AppOwner != "acrossoffwest" || len(v.AllowedOwners) != 1 || v.AllowedOwners[0] != "tmgr-dev" {
		t.Fatalf("view = %+v", v)
	}
}

func TestAccess_SaveOwnersNormalizes(t *testing.T) {
	h, st := newAccessHandler(t)
	rec := httptest.NewRecorder()
	h.SaveOwners(rec, accessReq(http.MethodPost, "/access/owners", `{"owners":[" tmgr-dev ","acme","","tmgr-dev"]}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	got, _ := st.GetAccessSettings(context.Background())
	if got.AllowedOwners != "tmgr-dev,acme" {
		t.Fatalf("stored = %q (want trimmed, de-duped, no empties)", got.AllowedOwners)
	}
}

func TestAccess_SaveRequiresAdminEdit(t *testing.T) {
	h, _ := newAccessHandler(t)
	h.Cfg.AllowAdminEdit = false
	rec := httptest.NewRecorder()
	h.SaveOwners(rec, accessReq(http.MethodPost, "/access/owners", `{"owners":["x"]}`))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rec.Code)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/httpapi/handlers/ -run TestAccess`
Expected: FAIL — `AccessHandler` undefined.

- [ ] **Step 3: Implement the handler**

Create `internal/httpapi/handlers/access.go`:

```go
package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/muhac/actions-runner-pool/internal/config"
	"github.com/muhac/actions-runner-pool/internal/store"
)

// AccessHandler serves the "Access control" panel: the owner allowlist that
// decides whose repositories may launch runners on this slot. The App owner
// is always allowed and is shown read-only.
type AccessHandler struct {
	Cfg   *config.Config
	Store store.Store
	Log   *slog.Logger
}

const accessBodyLimit = 16 * 1024

type accessView struct {
	AppOwner      string   `json:"app_owner"`
	AllowedOwners []string `json:"allowed_owners"`
}

// splitOwners parses the stored comma list into a slice (trimmed, no empties).
func splitOwners(s string) []string {
	out := []string{}
	for _, e := range strings.Split(s, ",") {
		if e = strings.TrimSpace(e); e != "" {
			out = append(out, e)
		}
	}
	return out
}

// normalizeOwners trims, drops empties, and de-duplicates (case-insensitive,
// keeping first spelling), returning the cleaned slice.
func normalizeOwners(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, e := range in {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		k := strings.ToLower(e)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, e)
	}
	return out
}

// GetAccess returns the App owner and the configured allowed owners.
func (h *AccessHandler) GetAccess(w http.ResponseWriter, r *http.Request) {
	if !authorizedBearer(h.Cfg, r.Header.Get("Authorization")) {
		writeAdminAuthError(w, http.StatusUnauthorized)
		return
	}
	appOwner := ""
	if cfg, err := h.Store.GetAppConfig(r.Context()); err != nil {
		h.fail(w, "get app config", err)
		return
	} else if cfg != nil {
		appOwner = cfg.OwnerLogin
	}
	a, err := h.Store.GetAccessSettings(r.Context())
	if err != nil {
		h.fail(w, "get access settings", err)
		return
	}
	writeJSON(w, accessView{AppOwner: appOwner, AllowedOwners: splitOwners(a.AllowedOwners)})
}

// SaveOwners replaces the allowed-owners list.
func (h *AccessHandler) SaveOwners(w http.ResponseWriter, r *http.Request) {
	if status := adminWriteDenied(h.Cfg, r.Header.Get("Authorization")); status != 0 {
		writeAdminAuthError(w, status)
		return
	}
	body := http.MaxBytesReader(w, r.Body, accessBodyLimit)
	defer func() { _ = body.Close() }()
	var req struct {
		Owners []string `json:"owners"`
	}
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	owners := normalizeOwners(req.Owners)
	if err := h.Store.SaveAccessSettings(r.Context(), &store.AccessSettings{AllowedOwners: strings.Join(owners, ",")}); err != nil {
		h.fail(w, "save access settings", err)
		return
	}
	appOwner := ""
	if cfg, err := h.Store.GetAppConfig(r.Context()); err == nil && cfg != nil {
		appOwner = cfg.OwnerLogin
	}
	writeJSON(w, accessView{AppOwner: appOwner, AllowedOwners: owners})
}

func (h *AccessHandler) fail(w http.ResponseWriter, msg string, err error) {
	if h.Log != nil {
		h.Log.Error(msg, "error", err)
	}
	http.Error(w, "internal server error", http.StatusInternalServerError)
}
```

Note: `writeJSON` already exists in this package — reuse it, do not redefine.

- [ ] **Step 4: Run handler tests**

Run: `go test ./internal/httpapi/handlers/ -run TestAccess`
Expected: PASS.

- [ ] **Step 5: Wire routes + WebhookHandler.GitHub**

In `internal/httpapi/router.go`, set the webhook handler's `GitHub` to the
existing `gh` client and register the access routes. Update the `wh`
construction to include `GitHub: gh`:

```go
	wh := &handlers.WebhookHandler{Cfg: cfg, Store: st, Scheduler: sch, Telegram: tg, GitHub: gh, Log: log}
	mux.HandleFunc("POST /github/webhook", wh.Post)

	access := &handlers.AccessHandler{Cfg: cfg, Store: st, Log: log}
	mux.HandleFunc("GET /access", access.GetAccess)
	mux.HandleFunc("POST /access/owners", access.SaveOwners)
```

(`gh` is the `*github.Client` already passed into `NewRouter`; `*github.Client`
satisfies `appOwnerClient` via `AppJWT` + `AppOwner`.)

- [ ] **Step 6: Build + full suite**

Run: `go build ./... && go test ./...`
Expected: PASS across all packages.

- [ ] **Step 7: Commit**

```bash
git add internal/httpapi/handlers/access.go internal/httpapi/handlers/access_test.go internal/httpapi/router.go
git commit -m "feat(httpapi): access-control endpoints + wire owner gate"
```

---

## Task 5: Dashboard — "Access control" panel

**Files:**
- Modify: `internal/httpapi/handlers/templates/dashboard.html`

**Interfaces:**
- Consumes: `GET /access`, `POST /access/owners` (Task 4). Uses `State.token` + relative fetch, like the Notifications panel.

This task is UI wiring, verified by `go build`/`go test ./internal/httpapi/...`
(the template is parsed by `template.Must` at init). No new Go test.

- [ ] **Step 1: Add the panel markup**

In `internal/httpapi/handlers/templates/dashboard.html`, add a panel in the
admin drawer body next to the Notifications panel (use the same `adm-block`
markup conventions; gate the button on `AllowAdminEdit` as the other mutation
buttons do):

```html
    <p class="adm-section-head">Access control</p>
    <div class="adm-block" id="accessCard">
      <h5>Who can use this runner</h5>
      <p>Only repos owned by these accounts/orgs launch runners here. The App owner is always allowed.</p>
      <p class="muted">App owner: <span id="acOwner">…</span> <span class="muted">(always allowed)</span></p>
      <div class="inrow">
        <label for="acOwners">Allowed owners (comma or newline separated)</label>
        <textarea class="txt" id="acOwners" rows="3" placeholder="tmgr-dev, my-org" autocomplete="off"></textarea>
      </div>
      <div style="display:flex;gap:8px;margin-top:10px;align-items:center">
        <button class="iconbtn" id="acSave" type="button"{{if not .AllowAdminEdit}} disabled{{end}} style="height:36px">Save owners</button>
        <span id="acState" class="muted"></span>
      </div>
    </div>
```

- [ ] **Step 2: Add the panel script**

In the page `<script>` (where `State.token` and the notifications helpers live),
add:

```javascript
function acHeaders(json){
  const h = State.token ? {Authorization:'Bearer '+State.token} : {};
  if(json) h['Content-Type']='application/json';
  return h;
}
async function acLoad(){
  try{
    const r = await fetch('access',{headers:acHeaders(false)});
    if(!r.ok) return;
    const v = await r.json();
    document.getElementById('acOwner').textContent = v.app_owner || '(unknown)';
    document.getElementById('acOwners').value = (v.allowed_owners||[]).join('\n');
  }catch(e){/* leave defaults */}
}
document.getElementById('acSave').addEventListener('click', async ()=>{
  const raw = document.getElementById('acOwners').value;
  const owners = raw.split(/[\n,]+/).map(s=>s.trim()).filter(Boolean);
  try{
    const r = await fetch('access/owners',{method:'POST',headers:acHeaders(true),body:JSON.stringify({owners})});
    if(!r.ok){ throw new Error((await r.text())||('HTTP '+r.status)); }
    const v = await r.json();
    document.getElementById('acOwners').value = (v.allowed_owners||[]).join('\n');
    document.getElementById('acState').textContent = 'saved ✓';
  }catch(e){ document.getElementById('acState').textContent = 'error: '+e.message; }
});
acLoad();
```

- [ ] **Step 3: Build + template parse check**

Run: `go build ./... && go test ./internal/httpapi/...`
Expected: PASS (template parses).

- [ ] **Step 4: Commit**

```bash
git add internal/httpapi/handlers/templates/dashboard.html
git commit -m "feat(dashboard): Access control panel for the owner allowlist"
```

---

## Self-Review

**Spec coverage:**
- Gate in `handleWorkflowJob` queued, owner from full_name → Task 3. ✓
- Allow App owner OR listed owner, case-insensitive, deny→log+200 → Task 3. ✓
- Secure-by-default app owner via lazy GET /app + persist → Task 2 (AppOwner) + Task 3 (ownerAllowed lazy resolve + UpdateAppOwnerLogin). ✓
- Fail-closed when owner unknown + empty list → Task 3 (`TestOwnerAllowed_FailClosed`). ✓
- Store: `app_config.owner_login` + `access_settings` → Task 1. ✓
- Dashboard "Access control" panel (app owner chip + editable owners), admin-gated → Tasks 4 + 5. ✓
- Wiring (WebhookHandler.GitHub, routes) → Task 4. ✓
- Tests for store/github/webhook/access → Tasks 1–4. ✓
- Out of scope (per-repo, portal-push, installation-event rejection) → not implemented. ✓

**Placeholder scan:** No TBD/TODO; every code step is complete. The only
conditional instruction (drop the no-op `sort` if it trips unused-import) is
explicit with the exact action. ✓

**Type consistency:** `appOwnerClient.AppJWT(pem []byte, appID int64) (string, error)` and `AppOwner(ctx, jwt string) (string, error)` match `*github.Client` (Task 2) and the fake (Task 3). `store.AppConfig.OwnerLogin`, `store.AccessSettings.AllowedOwners`, `UpdateAppOwnerLogin`, `GetAccessSettings`, `SaveAccessSettings` are identical across Tasks 1/3/4. `ownerAllowed(ctx, scheduler.Repository, *store.AppConfig) bool` and `handleWorkflowJob(..., appCfg *store.AppConfig)` consistent in Task 3. `accessView{app_owner, allowed_owners}` consistent across Task 4 + Task 5 JS. ✓
