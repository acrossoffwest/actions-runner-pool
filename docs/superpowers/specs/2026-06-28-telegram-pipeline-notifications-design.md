# gharp — Telegram pipeline notifications

**Status:** design approved · **Date:** 2026-06-28

## Goal

Notify a Telegram chat when a GitHub Actions pipeline (whole workflow run)
finishes — success, failure, or cancelled. Configurable per gharp instance
entirely from the dashboard UI (no SSH, no file editing).

## Scope & placement

The feature lives **inside gharp** (the per-tenant instance running in a slot),
not in the portal. Rationale:

- gharp is the component that receives the GitHub App webhooks; the portal never
  sees job/run events.
- The bot token is a secret. Storing it in the gharp instance DB keeps it in the
  same trust boundary as the GitHub App private key and client secret, and
  preserves per-tenant isolation — one tenant's bot token never reaches the
  portal DB or another tenant's slot.

The portal is **not modified** by this work.

## Granularity

One message per **workflow run** (`workflow_run` webhook, `action == "completed"`),
not per job. This matches the user's mental model of "the pipeline passed / failed".

## Architecture

```
GitHub App ──workflow_run (completed)──▶ gharp /github/webhook
                                              │
                                  WebhookHandler.handleWorkflowRun
                                              │  (load notify_settings)
                                              ▼
                                     internal/notify.Client
                                              │  POST sendMessage
                                              ▼
                                   api.telegram.org/bot<token>

Dashboard ("Notifications" section) ──▶ settings handlers ──▶ store.NotifySettings
   Connect-chat button ──▶ notify.Client.GetUpdates ──▶ resolve chat_id
```

## Components

### 1. GitHub App — subscribe to `workflow_run`

`internal/github/manifest.go` — `BuildManifest`: add `"workflow_run"` to
`DefaultEvents` (currently `["workflow_job"]`).

- **New installs** receive the subscription automatically.
- **Existing installs** (already-provisioned tenants): the App owner ticks the
  "Workflow run" checkbox under *App settings → Permissions & events → Subscribe
  to events*. No re-install / re-authorization is required because the
  `actions: read` permission `workflow_run` needs is **already granted** by the
  current manifest. This one-checkbox step is documented in the setup guide.

### 2. Webhook handler — handle `workflow_run`

`internal/httpapi/handlers/webhook.go`:

- Add `case "workflow_run": h.handleWorkflowRun(w, r, body)` to the event switch.
- `handleWorkflowRun`:
  1. Unmarshal into a `workflowRunEvent` struct: `action`,
     `workflow_run { name, conclusion, html_url, head_branch, run_number, event }`,
     `repository { full_name }`, `sender { login }`.
  2. If `action != "completed"` → `200`, return (ignore queued/in_progress/requested).
  3. Load `notify_settings`. If `!enabled` or token/chat empty → `200`, return.
  4. If `mode == "failures"` and `conclusion == "success"` → `200`, return (skip).
  5. Build the message (see Format) and call `notify.Client.SendMessage`.
     Send is **best-effort**: on error, log and still return `200` so GitHub
     does not retry a transient Telegram outage as a webhook failure.

The existing HMAC signature verification path is unchanged and still runs first.

### 3. Telegram client — `internal/notify`

New package, single dependency-free HTTP client:

- `type Client struct { HTTP *http.Client }` (timeout ~10s).
- `SendMessage(ctx, token, chatID, text string) error` →
  `POST https://api.telegram.org/bot<token>/sendMessage` with form/JSON body
  `{chat_id, text, disable_web_page_preview:true}`. Non-2xx → error including
  Telegram's `description` field (but the token is never logged).
- `GetUpdates(ctx, token string) ([]Update, error)` →
  `GET https://api.telegram.org/bot<token>/getUpdates`. Used only by the
  connect-chat flow to resolve a `chat_id` from the most recent message.

**Egress:** the gharp slot process can reach `api.telegram.org`. The nftables
egress rules only drop cloud-metadata addresses (`169.254.169.254/.253`) for the
runner container UID; they do not restrict the gharp service's outbound HTTPS.

### 4. Store — `notify_settings`

`internal/store/schema.sql` — new single-row table (same pattern as `app_config`):

```sql
CREATE TABLE IF NOT EXISTS notify_settings (
    id           INTEGER PRIMARY KEY CHECK (id = 1),
    enabled      INTEGER NOT NULL DEFAULT 0,
    tg_bot_token TEXT    NOT NULL DEFAULT '',
    tg_chat_id   TEXT    NOT NULL DEFAULT '',
    chat_title   TEXT    NOT NULL DEFAULT '',
    mode         TEXT    NOT NULL DEFAULT 'all'   -- 'all' | 'failures'
);
```

Store API (in `store.go` interface + `sqlite.go` impl + `models.go`):

- `GetNotifySettings(ctx) (*NotifySettings, error)` — returns defaults (disabled)
  when the row is absent.
- `SaveNotifySettings(ctx, *NotifySettings) error` — upsert on `id = 1`.

### 5. Dashboard UI — "Notifications" section

`internal/httpapi/handlers/` + `templates/dashboard.html` (or a dedicated
`notifications.html` section, matching the existing dashboard layout/design
tokens):

UI elements:

- **Bot token** input — value rendered masked (e.g. `••••••1234`), never the raw
  token; a separate "Replace token" action to set a new one.
- **Connect chat** button — enabled once a token is saved. Flow: user sends
  `/start` (any message) to their bot, then clicks Connect → backend calls
  `GetUpdates`, picks the `chat_id` from the most recent update, saves it with the
  chat's title/username, and the UI shows `Connected: <chat>`. If no update is
  found → inline hint "send a message to your bot first".
- **Mode** toggle — `All runs` / `Failures only` (default: **All runs**).
- **Enabled** toggle — master on/off.
- **Send test** button — calls `SendMessage` with a sample line; surfaces success
  or the Telegram error inline.

Handlers (follow the existing dashboard handler conventions for auth/mutation —
gharp runs behind the portal proxy which injects the per-tenant identity; new
mutation endpoints reuse whatever protection the current dashboard mutations
use):

- `POST /notifications/token` — save/replace bot token.
- `POST /notifications/connect` — getUpdates → resolve & save chat_id + title.
- `POST /notifications/mode` and `/notifications/enabled` — toggles.
- `POST /notifications/test` — send a test message.
- The settings render inside the dashboard GET handler's page data.

## Message format

```
✅ CI passed — owner/repo
run #42 · branch main · @user
https://github.com/owner/repo/actions/runs/123
```

- Icon by conclusion: `✅` success · `❌` failure · `⚠️` cancelled (and a neutral
  icon for any other conclusion).
- Line 1: `<icon> <workflow name> <verb> — <repo full_name>`.
- Line 2: `run #<run_number> · branch <head_branch> · @<sender login>`.
- Line 3: the run `html_url`. `disable_web_page_preview:true` keeps it tidy.
- Plain text (no `parse_mode`) to avoid escaping pitfalls with repo/branch names.

## Security

- Bot token is a secret: stored in the gharp DB only, never logged, never echoed
  to chat, rendered masked in the UI. Telegram error messages are surfaced
  without including the token (the token is only in the URL path, which is not
  echoed).
- `getUpdates` works because gharp does not register a Telegram bot webhook. Edge
  case: if the user has set a webhook on the bot elsewhere, `getUpdates` returns
  HTTP 409 — surface a clear inline error ("this bot has a webhook set; remove it
  or enter chat_id manually" — manual entry is a possible future fallback, out of
  scope for v1).

## Testing

- **webhook_test.go**: `workflow_run` completed → notifier called with the
  expected message; non-`completed` action ignored; `enabled=false` → no send;
  `mode=failures` + `conclusion=success` → skip; failure → send. Telegram send
  error → handler still returns `200`.
- **internal/notify**: `SendMessage` / `GetUpdates` against an `httptest.Server`
  — assert URL path (`/bot<token>/sendMessage`), payload (`chat_id`, `text`),
  non-2xx → error.
- **store**: `Get/SaveNotifySettings` round-trip; absent row → disabled defaults.
- **handlers**: token save (masked render), connect (fake getUpdates → chat_id
  saved), test send, toggles.

## Out of scope (v1)

- Manual `chat_id` entry fallback (auto-connect only).
- Per-repo / per-workflow filtering, threads, message threading.
- Notification channels other than Telegram.
- Portal-level aggregation or portal UI for notifications.
