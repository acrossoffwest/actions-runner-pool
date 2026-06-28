# gharp — owner-allowlist gating for runner dispatch

**Status:** design approved · **Date:** 2026-06-28

## Goal

Stop a gharp slot from spinning runners for **arbitrary** GitHub accounts. When
the slot's GitHub App is **public**, any stranger can install it on their repo;
their `workflow_job` events then reach this slot's webhook and would launch
runners that execute *their* code on the owner's machine. Gate dispatch so a
runner only starts for repositories owned by an **allowed account/org**.

This lives entirely inside gharp (the per-slot instance). The portal is not
modified.

## Threat model

- Webhook deliveries are HMAC-signed by GitHub, so the payload (including
  `repository.full_name`) is authentic — the owner login cannot be forged.
- The risk is purely *authorization*: gharp currently dispatches a runner for
  any installation that reaches it. We add an owner check before dispatch.

## Gating rule

In `handleWorkflowJob`, action `queued` (the same place the existing
`publicRepoAllowed` guard already drops disallowed public repos), derive:

```
owner = the part of repository.full_name before "/"   // "tmgr-dev/backend" → "tmgr-dev"
```

Allow the job (proceed to dispatch) iff **either**:

1. `owner` case-insensitively equals the **App owner login** (secure default —
   the account that registered the App is always trusted), **or**
2. `owner` is in the operator-configured **allowed-owners** list.

Otherwise **drop**: log `webhook: owner not allowed` (with repo + job id),
return `200`, do **not** insert the job or launch a runner. Returning 200 keeps
GitHub from retrying — a denied owner is a permanent decision, not a transient
failure.

Gate only the `queued` action. `in_progress` / `completed` for jobs this slot
already admitted are processed normally (they no-op for jobs never inserted).

## Secure-by-default: the App owner

The App owner login is resolved once and cached, so a stranger (owner ≠ app
owner, not in the list) is denied **automatically, with zero configuration**.

- **New installs:** populate `app_config.owner_login` at setup time from the
  manifest conversion response (`ConvertCode`), which includes the app's
  `owner.login`.
- **Existing app (owner_login empty):** lazily resolve via the GitHub App API —
  mint a JWT with the stored PEM + App ID (`Client.AppJWT`), call
  `GET /app`, read `owner.login`, and persist it to `app_config.owner_login` so
  the lookup happens at most once.
- **Resolution failure** (transient API error, owner_login still empty): treat
  the App-owner check as *unsatisfied* for that request and fall back to the
  configured list only. If the list is also empty → **deny** (fail-closed) and
  log a clear warning. Operators are advised to also add their own login to the
  allowed-owners list as belt-and-suspenders; once `owner_login` is persisted,
  this path is never hit again.

## Components

### 1. Store — App owner + allowed owners

- `internal/store/schema.sql`:
  - Add column `owner_login TEXT NOT NULL DEFAULT ''` to `app_config`.
  - New single-row table `access_settings (id INTEGER PRIMARY KEY CHECK (id=1), allowed_owners TEXT NOT NULL DEFAULT '')` (comma-separated logins; mirrors the `notify_settings` pattern).
- Store API:
  - `UpdateAppOwnerLogin(ctx, login string) error` — single-column update on `app_config` id=1 (mirrors the other `UpdateAppConfig*` setters).
  - `GetAccessSettings(ctx) (*AccessSettings, error)` — never nil; absent row → empty.
  - `SaveAccessSettings(ctx, *AccessSettings) error` — upsert id=1.
  - `AppConfig` gains an `OwnerLogin string` field; `SaveAppConfig` writes it (ConvertCode sets it for new apps).

### 2. GitHub client — resolve the App owner

`internal/github/`: add `Client.AppOwner(ctx, jwt string) (string, error)` —
`GET /app` with the JWT bearer, decode `{ "owner": { "login": "" } }`, return
the login. Used only for the existing-app backfill.

### 3. Webhook handler — the gate

`internal/httpapi/handlers/webhook.go`:

- `WebhookHandler` gains what it needs to resolve + cache the App owner: a
  GitHub client (to mint a JWT + call `AppOwner`) and access to `app_config`
  (already via `Store`). Cache the resolved owner login in memory after first
  success to avoid a DB read per event.
- `ownerAllowed(ctx, repo) bool` implements the rule above:
  - load app owner (cached → store `owner_login` → lazy `GET /app` + persist),
  - load `access_settings.allowed_owners` (parse comma list, trim, drop empties),
  - return true if `owner` matches the app owner or any list entry (case-insensitive).
- Call it in the `queued` branch, before the lazy repo→installation upsert and
  `InsertJobIfNew`/`Enqueue`. On deny: log + `200` + return.

### 4. Dashboard — "Access control" panel

`internal/httpapi/handlers/` + `templates/dashboard.html` (a new
`adm-block`, like the Notifications panel; admin-gated; hidden/auth handled the
same way as other mutations, BEHIND_PORTAL-aware):

- Shows the **App owner** as a fixed, non-editable chip labelled "always allowed".
- A field for **additional allowed owners** (accounts/orgs), comma- or
  newline-separated; **Save**.
- Endpoints (gated by `adminWriteDenied`, read by `authorizedBearer`):
  - `GET /access` → `{ app_owner, allowed_owners: [...] }`.
  - `POST /access/owners` → save the list; returns the normalized list.
- JS: relative paths + `State.token` bearer (proxy injects behind the portal),
  consistent with the Notifications panel.

### 5. Wiring

`internal/httpapi/router.go`: pass the GitHub client into `WebhookHandler`;
construct and register the `AccessHandler` routes. Existing `WebhookHandler`
construction grows one field — keep nil-safe so unrelated tests still compile.

## Security & correctness notes

- Case-insensitive owner comparison (GitHub logins are case-insensitive).
- The gate is **authorization only**; it never trusts client-supplied auth — the
  owner comes from the HMAC-verified webhook body.
- `allowed_owners` entries are logins, never secrets — safe to render and store.
- Fail-closed when the App owner is unknown *and* no owners are configured, so a
  misconfigured slot does not silently serve strangers.

## Testing

- **store**: `app_config.owner_login` round-trip via Save + UpdateAppOwnerLogin;
  `Get/SaveAccessSettings` round-trip + absent-row default.
- **github**: `AppOwner` against an httptest server (path `/app`, parses owner).
- **webhook** (`ownerAllowed` / `handleWorkflowJob`):
  - owner == app owner → allowed (dispatch proceeds);
  - owner in `allowed_owners` (case-insensitive, with spaces) → allowed;
  - stranger owner → dropped (no insert/enqueue), 200, logged;
  - app owner unknown + empty list → denied + 200 + warning;
  - gate applies only to `queued`.
- **access handlers**: GET returns app owner + list; POST saves; admin gate (403
  when `AllowAdminEdit` false / 401 without bearer).

## Out of scope (v1)

- Per-repo allowlisting (owner-level only; the existing `RepoAllowlist` is a
  separate public-repo concern).
- Portal-driven allowlist push (slot stays self-contained; revisit if needed).
- Auto-uninstall / rejecting the `installation` event for disallowed accounts
  (we only gate dispatch; a stranger's install is harmless until it sends jobs,
  which are dropped).
