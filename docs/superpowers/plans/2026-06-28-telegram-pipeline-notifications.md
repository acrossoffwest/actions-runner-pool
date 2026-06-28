# Telegram Pipeline Notifications Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Notify a Telegram chat when a GitHub Actions workflow run finishes (success / failure / cancelled), configured entirely from the gharp dashboard UI.

**Architecture:** The feature lives inside gharp (the per-tenant slot instance). GitHub sends `workflow_run` webhooks → the webhook handler loads per-instance notification settings from the store and, when enabled, posts a message to the Telegram Bot API via a small `internal/notify` client. A "Notifications" dashboard panel configures the bot token, auto-resolves the chat via `getUpdates`, and toggles mode/enabled. The portal is not modified.

**Tech Stack:** Go stdlib (`net/http`, `encoding/json`), `modernc.org/sqlite` via the existing `store.SQLite`, Go 1.22 `http.ServeMux`, the existing dashboard HTML/JS template.

## Global Constraints

- gharp module path: `github.com/muhac/actions-runner-pool`. All imports use it.
- Store constructor in tests: `store.OpenSQLite("file:" + t.TempDir() + "/test.db")` (returns `*store.SQLite`, satisfies `store.Store`).
- Store receiver is `*SQLite`, DB handle is `s.db`. Single-row config tables use `id INTEGER PRIMARY KEY CHECK (id = 1)` + `INSERT ... ON CONFLICT(id) DO UPDATE SET ... = excluded....`.
- Admin mutations are gated by `adminWriteDenied(cfg, authHeader)` (requires valid `Authorization: Bearer <cfg.AdminToken>` **and** `cfg.AllowAdminEdit == true`); read endpoints behind a token use `authorizedBearer(cfg, authHeader)`. Both live in `internal/httpapi/handlers/jobs.go`.
- Webhook HMAC verification (`verifySignature`) is unchanged and still runs before event dispatch.
- Secrets rule: the bot token is a secret. Never log it, never echo it to chat, never return it raw from an endpoint — return a masked hint only.
- Dashboard JS sends `Authorization: 'Bearer ' + State.token` and uses **relative** fetch paths (e.g. `fetch('stats', …)`), because gharp is served under a path prefix by the portal proxy.

---

## File Structure

- `internal/store/schema.sql` — add `notify_settings` table.
- `internal/store/models.go` — add `NotifySettings` struct.
- `internal/store/store.go` — add `GetNotifySettings` / `SaveNotifySettings` to the `Store` interface.
- `internal/store/sqlite.go` — implement the two methods.
- `internal/store/sqlite_test.go` — round-trip test.
- `internal/notify/client.go` — **new package**: Telegram `Client` (`SendMessage`, `GetUpdates`), `Update`/`Chat` types.
- `internal/notify/client_test.go` — **new**: httptest-based tests.
- `internal/github/manifest.go` — add `"workflow_run"` to `DefaultEvents`.
- `internal/github/manifest_test.go` — assert the new event is present.
- `internal/httpapi/handlers/webhook.go` — `workflow_run` case, `workflowRunEvent` struct, `handleWorkflowRun`, `buildRunMessage`, `messageSender` interface + `Telegram` field on `WebhookHandler`.
- `internal/httpapi/handlers/webhook_test.go` — workflow_run behavior tests.
- `internal/httpapi/handlers/notifications.go` — **new**: `NotificationsHandler` + `telegramAPI` interface + masked `settingsView`.
- `internal/httpapi/handlers/notifications_test.go` — **new**: handler tests.
- `internal/httpapi/router.go` — build a `*notify.Client`, wire it into `WebhookHandler` and `NotificationsHandler`, register notification routes.
- `internal/httpapi/handlers/templates/dashboard.html` — add the "Notifications" panel + JS.

---

## Task 1: Store — `notify_settings` table, model, and accessors

**Files:**
- Modify: `internal/store/schema.sql`
- Modify: `internal/store/models.go`
- Modify: `internal/store/store.go`
- Modify: `internal/store/sqlite.go`
- Test: `internal/store/sqlite_test.go`

**Interfaces:**
- Produces: `store.NotifySettings{Enabled bool; BotToken string; ChatID string; ChatTitle string; Mode string}`; `store.Store.GetNotifySettings(ctx) (*NotifySettings, error)` (never nil — returns `&NotifySettings{Mode:"all"}` when the row is absent); `store.Store.SaveNotifySettings(ctx, *NotifySettings) error`.

- [ ] **Step 1: Write the failing test**

Add to `internal/store/sqlite_test.go`:

```go
func TestNotifySettings_RoundTripAndDefault(t *testing.T) {
	s, err := OpenSQLite("file:" + t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	// Absent row → safe defaults (disabled, mode "all").
	got, err := s.GetNotifySettings(ctx)
	if err != nil {
		t.Fatalf("GetNotifySettings (default): %v", err)
	}
	if got.Enabled || got.Mode != "all" || got.BotToken != "" {
		t.Fatalf("default not as expected: %+v", got)
	}

	want := &NotifySettings{Enabled: true, BotToken: "123:abc", ChatID: "-100777", ChatTitle: "Team CI", Mode: "failures"}
	if err := s.SaveNotifySettings(ctx, want); err != nil {
		t.Fatalf("SaveNotifySettings: %v", err)
	}
	got, err = s.GetNotifySettings(ctx)
	if err != nil {
		t.Fatalf("GetNotifySettings: %v", err)
	}
	if *got != *want {
		t.Fatalf("round-trip mismatch: got %+v want %+v", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestNotifySettings_RoundTripAndDefault -v`
Expected: FAIL — `s.GetNotifySettings undefined` (compile error).

- [ ] **Step 3: Add the schema table**

In `internal/store/schema.sql`, after the `app_config` table block, add:

```sql
CREATE TABLE IF NOT EXISTS notify_settings (
  id           INTEGER PRIMARY KEY CHECK (id = 1),
  enabled      INTEGER NOT NULL DEFAULT 0,
  tg_bot_token TEXT    NOT NULL DEFAULT '',
  tg_chat_id   TEXT    NOT NULL DEFAULT '',
  chat_title   TEXT    NOT NULL DEFAULT '',
  mode         TEXT    NOT NULL DEFAULT 'all'
);
```

- [ ] **Step 4: Add the model**

In `internal/store/models.go`, after the `AppConfig` struct:

```go
// NotifySettings holds the per-instance Telegram notification config.
// Mode is "all" (notify on every completed run) or "failures" (skip
// successful runs). The bot token is a secret — never log it.
type NotifySettings struct {
	Enabled   bool
	BotToken  string
	ChatID    string
	ChatTitle string
	Mode      string
}
```

- [ ] **Step 5: Add the interface methods**

In `internal/store/store.go`, inside the `Store` interface, after the `UpdateAppConfigClientSecret` line:

```go
	// GetNotifySettings returns the single-row Telegram notification
	// config. It never returns nil: an absent row yields disabled
	// defaults with Mode "all".
	GetNotifySettings(ctx context.Context) (*NotifySettings, error)
	// SaveNotifySettings upserts the single-row (id=1) notification config.
	SaveNotifySettings(ctx context.Context, n *NotifySettings) error
```

- [ ] **Step 6: Implement in sqlite.go**

Append to `internal/store/sqlite.go`:

```go
func (s *SQLite) GetNotifySettings(ctx context.Context) (*NotifySettings, error) {
	const q = `SELECT enabled, tg_bot_token, tg_chat_id, chat_title, mode
		FROM notify_settings WHERE id = 1`
	var (
		n       NotifySettings
		enabled int
	)
	err := s.db.QueryRowContext(ctx, q).Scan(&enabled, &n.BotToken, &n.ChatID, &n.ChatTitle, &n.Mode)
	if errors.Is(err, sql.ErrNoRows) {
		return &NotifySettings{Mode: "all"}, nil
	}
	if err != nil {
		return nil, err
	}
	n.Enabled = enabled != 0
	return &n, nil
}

func (s *SQLite) SaveNotifySettings(ctx context.Context, n *NotifySettings) error {
	const q = `
INSERT INTO notify_settings (id, enabled, tg_bot_token, tg_chat_id, chat_title, mode)
VALUES (1, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  enabled=excluded.enabled, tg_bot_token=excluded.tg_bot_token,
  tg_chat_id=excluded.tg_chat_id, chat_title=excluded.chat_title, mode=excluded.mode`
	enabled := 0
	if n.Enabled {
		enabled = 1
	}
	_, err := s.db.ExecContext(ctx, q, enabled, n.BotToken, n.ChatID, n.ChatTitle, n.Mode)
	return err
}
```

- [ ] **Step 7: Run the test to verify it passes**

Run: `go test ./internal/store/ -run TestNotifySettings_RoundTripAndDefault -v`
Expected: PASS.

- [ ] **Step 8: Run the full store package**

Run: `go test ./internal/store/`
Expected: PASS (no other store tests broke — the `Store` interface grew, but `*SQLite` now implements it).

- [ ] **Step 9: Commit**

```bash
git add internal/store/schema.sql internal/store/models.go internal/store/store.go internal/store/sqlite.go internal/store/sqlite_test.go
git commit -m "feat(store): notify_settings table + Get/SaveNotifySettings"
```

---

## Task 2: `internal/notify` — Telegram client

**Files:**
- Create: `internal/notify/client.go`
- Test: `internal/notify/client_test.go`

**Interfaces:**
- Produces: `notify.New() *Client`; `(*Client).SendMessage(ctx, token, chatID, text string) error`; `(*Client).GetUpdates(ctx, token string) ([]Update, error)`; `notify.Update{Message struct{Chat Chat}}`; `notify.Chat{ID int64; Title, Username, Type string}`. `Client.BaseURL` is overridable in tests (defaults to `https://api.telegram.org`).

- [ ] **Step 1: Write the failing test**

Create `internal/notify/client_test.go`:

```go
package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSendMessage_PostsToBotPath(t *testing.T) {
	var gotPath, gotChat, gotText string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		var p struct {
			ChatID string `json:"chat_id"`
			Text   string `json:"text"`
		}
		_ = json.Unmarshal(body, &p)
		gotChat, gotText = p.ChatID, p.Text
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := New()
	c.BaseURL = srv.URL
	if err := c.SendMessage(context.Background(), "123:abc", "-100777", "hello"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if gotPath != "/bot123:abc/sendMessage" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotChat != "-100777" || gotText != "hello" {
		t.Fatalf("payload chat=%q text=%q", gotChat, gotText)
	}
}

func TestSendMessage_NonOKIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"description":"chat not found"}`))
	}))
	defer srv.Close()

	c := New()
	c.BaseURL = srv.URL
	err := c.SendMessage(context.Background(), "t", "c", "x")
	if err == nil || !strings.Contains(err.Error(), "chat not found") {
		t.Fatalf("want error containing description, got %v", err)
	}
}

func TestGetUpdates_ParsesChat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"result":[{"message":{"chat":{"id":42,"title":"CI","type":"group"}}}]}`))
	}))
	defer srv.Close()

	c := New()
	c.BaseURL = srv.URL
	ups, err := c.GetUpdates(context.Background(), "t")
	if err != nil {
		t.Fatalf("GetUpdates: %v", err)
	}
	if len(ups) != 1 || ups[0].Message.Chat.ID != 42 || ups[0].Message.Chat.Title != "CI" {
		t.Fatalf("parsed = %+v", ups)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/notify/ -v`
Expected: FAIL — package/`New` does not exist (compile error).

- [ ] **Step 3: Implement the client**

Create `internal/notify/client.go`:

```go
// Package notify sends messages through the Telegram Bot API.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client is a minimal Telegram Bot API client. BaseURL is overridable
// so tests can point it at an httptest server; production uses the
// default below.
type Client struct {
	HTTP    *http.Client
	BaseURL string
}

// New returns a Client with a 10s timeout against the real Telegram API.
func New() *Client {
	return &Client{
		HTTP:    &http.Client{Timeout: 10 * time.Second},
		BaseURL: "https://api.telegram.org",
	}
}

// Chat is the subset of a Telegram chat we care about.
type Chat struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	Username string `json:"username"`
	Type     string `json:"type"`
}

// Update is one entry from getUpdates (only the fields we use).
type Update struct {
	Message struct {
		Chat Chat `json:"chat"`
	} `json:"message"`
}

// apiError extracts Telegram's {ok, description} envelope.
type apiError struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
}

// SendMessage posts text to chatID using the given bot token. The token
// appears only in the URL path and is never logged here. Non-2xx (or
// ok:false) responses become an error carrying Telegram's description.
func (c *Client) SendMessage(ctx context.Context, token, chatID, text string) error {
	payload, err := json.Marshal(map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"disable_web_page_preview": true,
	})
	if err != nil {
		return err
	}
	url := c.BaseURL + "/bot" + token + "/sendMessage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode/100 != 2 {
		var ae apiError
		_ = json.Unmarshal(body, &ae)
		if ae.Description != "" {
			return fmt.Errorf("telegram sendMessage: %s", ae.Description)
		}
		return fmt.Errorf("telegram sendMessage: HTTP %d", resp.StatusCode)
	}
	return nil
}

// GetUpdates fetches recent bot updates, used to resolve a chat_id when
// the user connects a chat by messaging the bot.
func (c *Client) GetUpdates(ctx context.Context, token string) ([]Update, error) {
	url := c.BaseURL + "/bot" + token + "/getUpdates"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var env struct {
		OK          bool     `json:"ok"`
		Description string   `json:"description"`
		Result      []Update `json:"result"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("telegram getUpdates: decode: %w", err)
	}
	if !env.OK {
		return nil, fmt.Errorf("telegram getUpdates: %s", env.Description)
	}
	return env.Result, nil
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/notify/ -v`
Expected: PASS (all three tests).

- [ ] **Step 5: Commit**

```bash
git add internal/notify/client.go internal/notify/client_test.go
git commit -m "feat(notify): minimal Telegram Bot API client"
```

---

## Task 3: Webhook — handle `workflow_run` and notify

**Files:**
- Modify: `internal/github/manifest.go:58`
- Modify: `internal/github/manifest_test.go`
- Modify: `internal/httpapi/handlers/webhook.go`
- Test: `internal/httpapi/handlers/webhook_test.go`

**Interfaces:**
- Consumes: `store.Store.GetNotifySettings` (Task 1); `notify` types (Task 2).
- Produces: `messageSender` interface (`SendMessage(ctx, token, chatID, text string) error`); `WebhookHandler.Telegram messageSender` field; `buildRunMessage(*workflowRunEvent) string`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/httpapi/handlers/webhook_test.go`:

```go
// spySender records the last SendMessage call for assertions.
type spySender struct {
	calls int
	token string
	chat  string
	text  string
	err   error
}

func (s *spySender) SendMessage(_ context.Context, token, chatID, text string) error {
	s.calls++
	s.token, s.chat, s.text = token, chatID, text
	return s.err
}

func newWorkflowRunBody(t *testing.T, action, conclusion string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"action": action,
		"workflow_run": map[string]any{
			"name": "CI", "conclusion": conclusion, "html_url": "https://x/runs/1",
			"head_branch": "main", "run_number": 7,
		},
		"repository": map[string]any{"full_name": "o/r"},
		"sender":     map[string]any{"login": "alice"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newWebhookHandlerWithNotify(t *testing.T, s *spySender, settings *store.NotifySettings) *WebhookHandler {
	t.Helper()
	st, err := store.OpenSQLite("file:" + t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if settings != nil {
		if err := st.SaveNotifySettings(context.Background(), settings); err != nil {
			t.Fatal(err)
		}
	}
	return &WebhookHandler{Store: st, Telegram: s, Log: slog.Default()}
}

func TestHandleWorkflowRun_Completed_Sends(t *testing.T) {
	s := &spySender{}
	h := newWebhookHandlerWithNotify(t, s, &store.NotifySettings{
		Enabled: true, BotToken: "tok", ChatID: "99", Mode: "all",
	})
	rec := httptest.NewRecorder()
	body := newWorkflowRunBody(t, "completed", "success")
	h.handleWorkflowRun(rec, httptest.NewRequest(http.MethodPost, "/github/webhook", bytes.NewReader(body)), body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if s.calls != 1 || s.token != "tok" || s.chat != "99" {
		t.Fatalf("send = %+v", s)
	}
	if !strings.Contains(s.text, "o/r") || !strings.Contains(s.text, "run #7") {
		t.Fatalf("text = %q", s.text)
	}
}

func TestHandleWorkflowRun_NonCompleted_Ignored(t *testing.T) {
	s := &spySender{}
	h := newWebhookHandlerWithNotify(t, s, &store.NotifySettings{Enabled: true, BotToken: "t", ChatID: "1", Mode: "all"})
	rec := httptest.NewRecorder()
	body := newWorkflowRunBody(t, "requested", "")
	h.handleWorkflowRun(rec, httptest.NewRequest(http.MethodPost, "/github/webhook", bytes.NewReader(body)), body)
	if rec.Code != http.StatusOK || s.calls != 0 {
		t.Fatalf("status=%d calls=%d", rec.Code, s.calls)
	}
}

func TestHandleWorkflowRun_Disabled_NoSend(t *testing.T) {
	s := &spySender{}
	h := newWebhookHandlerWithNotify(t, s, &store.NotifySettings{Enabled: false, BotToken: "t", ChatID: "1", Mode: "all"})
	rec := httptest.NewRecorder()
	body := newWorkflowRunBody(t, "completed", "failure")
	h.handleWorkflowRun(rec, httptest.NewRequest(http.MethodPost, "/github/webhook", bytes.NewReader(body)), body)
	if rec.Code != http.StatusOK || s.calls != 0 {
		t.Fatalf("status=%d calls=%d", rec.Code, s.calls)
	}
}

func TestHandleWorkflowRun_FailuresMode_SkipsSuccess(t *testing.T) {
	s := &spySender{}
	h := newWebhookHandlerWithNotify(t, s, &store.NotifySettings{Enabled: true, BotToken: "t", ChatID: "1", Mode: "failures"})
	rec := httptest.NewRecorder()
	body := newWorkflowRunBody(t, "completed", "success")
	h.handleWorkflowRun(rec, httptest.NewRequest(http.MethodPost, "/github/webhook", bytes.NewReader(body)), body)
	if s.calls != 0 {
		t.Fatalf("expected skip, calls=%d", s.calls)
	}
	// Failure in the same mode DOES send.
	body = newWorkflowRunBody(t, "completed", "failure")
	h.handleWorkflowRun(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/github/webhook", bytes.NewReader(body)), body)
	if s.calls != 1 {
		t.Fatalf("expected one send for failure, calls=%d", s.calls)
	}
}

func TestHandleWorkflowRun_SendError_Still200(t *testing.T) {
	s := &spySender{err: errors.New("boom")}
	h := newWebhookHandlerWithNotify(t, s, &store.NotifySettings{Enabled: true, BotToken: "t", ChatID: "1", Mode: "all"})
	rec := httptest.NewRecorder()
	body := newWorkflowRunBody(t, "completed", "failure")
	h.handleWorkflowRun(rec, httptest.NewRequest(http.MethodPost, "/github/webhook", bytes.NewReader(body)), body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (telegram failure must not fail the webhook)", rec.Code)
	}
}
```

Ensure the test file imports `bytes`, `context`, `encoding/json`, `errors`, `log/slog`, `net/http`, `net/http/httptest`, `strings`, `testing`, and `github.com/muhac/actions-runner-pool/internal/store` (add any missing to the existing import block).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/httpapi/handlers/ -run TestHandleWorkflowRun -v`
Expected: FAIL — `h.handleWorkflowRun undefined`, `WebhookHandler.Telegram` unknown field.

- [ ] **Step 3: Add the `Telegram` field and `messageSender` interface**

In `internal/httpapi/handlers/webhook.go`, add the interface near `jobEnqueuer` and the field to `WebhookHandler`:

```go
// messageSender is the subset of *notify.Client the webhook needs to
// deliver a notification; an interface so tests can pass a spy.
type messageSender interface {
	SendMessage(ctx context.Context, token, chatID, text string) error
}
```

```go
// WebhookHandler handles GitHub webhook events.
type WebhookHandler struct {
	Cfg       *config.Config
	Store     store.Store
	Scheduler jobEnqueuer
	Telegram  messageSender // nil disables notifications (e.g. in unrelated tests)
	Log       *slog.Logger
}
```

Add `"context"` to the import block if not already present (it is used by the interface signature).

- [ ] **Step 4: Add the event case**

In the `switch r.Header.Get("X-GitHub-Event")` block, add a case alongside `workflow_job`:

```go
	case "workflow_run":
		h.handleWorkflowRun(w, r, body)
```

- [ ] **Step 5: Implement `handleWorkflowRun` and `buildRunMessage`**

Append to `internal/httpapi/handlers/webhook.go`:

```go
// ---------------- workflow_run (Telegram notifications) ----------------

type workflowRunEvent struct {
	Action      string `json:"action"`
	WorkflowRun struct {
		Name       string `json:"name"`
		Conclusion string `json:"conclusion"`
		HTMLURL    string `json:"html_url"`
		HeadBranch string `json:"head_branch"`
		RunNumber  int64  `json:"run_number"`
	} `json:"workflow_run"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Sender struct {
		Login string `json:"login"`
	} `json:"sender"`
}

// handleWorkflowRun delivers a Telegram message for a completed workflow
// run when notifications are enabled. Notification delivery is
// best-effort: any failure (bad payload, store error, Telegram error) is
// logged and still returns 200, so GitHub does not retry a transient
// notification problem as a webhook failure.
func (h *WebhookHandler) handleWorkflowRun(w http.ResponseWriter, r *http.Request, body []byte) {
	var ev workflowRunEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		h.logError("unmarshal workflow_run", err)
		w.WriteHeader(http.StatusOK)
		return
	}
	if ev.Action != "completed" {
		w.WriteHeader(http.StatusOK)
		return
	}
	settings, err := h.Store.GetNotifySettings(r.Context())
	if err != nil {
		h.logError("load notify settings", err)
		w.WriteHeader(http.StatusOK)
		return
	}
	if !settings.Enabled || settings.BotToken == "" || settings.ChatID == "" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if settings.Mode == "failures" && ev.WorkflowRun.Conclusion == "success" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if h.Telegram != nil {
		if err := h.Telegram.SendMessage(r.Context(), settings.BotToken, settings.ChatID, buildRunMessage(&ev)); err != nil {
			h.logError("send telegram notification", err)
		}
	}
	w.WriteHeader(http.StatusOK)
}

// buildRunMessage renders the plain-text notification for a completed run.
func buildRunMessage(ev *workflowRunEvent) string {
	run := ev.WorkflowRun
	var icon, verb string
	switch run.Conclusion {
	case "success":
		icon, verb = "✅", "passed"
	case "failure":
		icon, verb = "❌", "failed"
	case "cancelled":
		icon, verb = "⚠️", "cancelled"
	default:
		icon, verb = "ℹ️", run.Conclusion
	}
	return fmt.Sprintf("%s %s %s — %s\nrun #%d · branch %s · @%s\n%s",
		icon, run.Name, verb, ev.Repository.FullName,
		run.RunNumber, run.HeadBranch, ev.Sender.Login, run.HTMLURL)
}
```

Add `"fmt"` to the import block if not already present.

- [ ] **Step 6: Run the webhook tests**

Run: `go test ./internal/httpapi/handlers/ -run TestHandleWorkflowRun -v`
Expected: PASS (all five).

- [ ] **Step 7: Add `workflow_run` to the manifest + its test**

In `internal/github/manifest.go`, change the events line:

```go
		DefaultEvents: []string{"workflow_job", "workflow_run"},
```

Add to `internal/github/manifest_test.go`:

```go
func TestBuildManifest_SubscribesWorkflowRun(t *testing.T) {
	m := BuildManifest("https://example.test")
	var hasRun bool
	for _, e := range m.DefaultEvents {
		if e == "workflow_run" {
			hasRun = true
		}
	}
	if !hasRun {
		t.Fatalf("manifest must subscribe to workflow_run; got %v", m.DefaultEvents)
	}
}
```

- [ ] **Step 8: Run the manifest + handlers tests**

Run: `go test ./internal/github/ ./internal/httpapi/handlers/`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/github/manifest.go internal/github/manifest_test.go internal/httpapi/handlers/webhook.go internal/httpapi/handlers/webhook_test.go
git commit -m "feat(webhook): notify Telegram on completed workflow_run"
```

---

## Task 4: Notifications HTTP handlers + routes

**Files:**
- Create: `internal/httpapi/handlers/notifications.go`
- Test: `internal/httpapi/handlers/notifications_test.go`
- Modify: `internal/httpapi/router.go`

**Interfaces:**
- Consumes: `store.Store` notify methods (Task 1); `notify` client (Task 2); `adminWriteDenied` / `authorizedBearer` / `writeAdminAuthError` from `jobs.go`.
- Produces: `NotificationsHandler{Cfg, Store, Telegram telegramAPI, Log}` with methods `GetSettings`, `SaveToken`, `Connect`, `SetMode`, `SetEnabled`, `Test`; `telegramAPI` interface (`messageSender` + `GetUpdates(ctx, token) ([]notify.Update, error)`).

- [ ] **Step 1: Write the failing tests**

Create `internal/httpapi/handlers/notifications_test.go`:

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
	"github.com/muhac/actions-runner-pool/internal/notify"
	"github.com/muhac/actions-runner-pool/internal/store"
)

type fakeTelegram struct {
	updates  []notify.Update
	sendErr  error
	sentText string
}

func (f *fakeTelegram) SendMessage(_ context.Context, _, _, text string) error {
	f.sentText = text
	return f.sendErr
}
func (f *fakeTelegram) GetUpdates(_ context.Context, _ string) ([]notify.Update, error) {
	return f.updates, nil
}

func newNotifHandler(t *testing.T, tg telegramAPI) (*NotificationsHandler, *store.SQLite) {
	t.Helper()
	st, err := store.OpenSQLite("file:" + t.TempDir() + "/n.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg := &config.Config{AdminToken: "secret", AllowAdminEdit: true}
	return &NotificationsHandler{Cfg: cfg, Store: st, Telegram: tg}, st
}

func authReq(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer secret")
	r.Header.Set("Content-Type", "application/json")
	return r
}

func TestNotif_SaveToken_MaskedAndStored(t *testing.T) {
	h, st := newNotifHandler(t, &fakeTelegram{})
	rec := httptest.NewRecorder()
	h.SaveToken(rec, authReq(http.MethodPost, "/notifications/token", `{"token":"123456:secretpart"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "secretpart") {
		t.Fatalf("response leaked raw token: %s", rec.Body.String())
	}
	got, _ := st.GetNotifySettings(context.Background())
	if got.BotToken != "123456:secretpart" {
		t.Fatalf("token not stored: %q", got.BotToken)
	}
}

func TestNotif_Connect_ResolvesChat(t *testing.T) {
	tg := &fakeTelegram{updates: []notify.Update{}}
	tg.updates = append(tg.updates, notify.Update{})
	tg.updates[0].Message.Chat = notify.Chat{ID: 555, Title: "CI Room"}
	h, st := newNotifHandler(t, tg)
	// Token must exist first.
	_ = st.SaveNotifySettings(context.Background(), &store.NotifySettings{BotToken: "t", Mode: "all"})
	rec := httptest.NewRecorder()
	h.Connect(rec, authReq(http.MethodPost, "/notifications/connect", ``))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	got, _ := st.GetNotifySettings(context.Background())
	if got.ChatID != "555" || got.ChatTitle != "CI Room" {
		t.Fatalf("chat not saved: %+v", got)
	}
}

func TestNotif_Connect_NoUpdates_409(t *testing.T) {
	h, st := newNotifHandler(t, &fakeTelegram{updates: nil})
	_ = st.SaveNotifySettings(context.Background(), &store.NotifySettings{BotToken: "t", Mode: "all"})
	rec := httptest.NewRecorder()
	h.Connect(rec, authReq(http.MethodPost, "/notifications/connect", ``))
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d", rec.Code)
	}
}

func TestNotif_Mutations_RequireAdminEdit(t *testing.T) {
	h, _ := newNotifHandler(t, &fakeTelegram{})
	h.Cfg.AllowAdminEdit = false
	rec := httptest.NewRecorder()
	h.SaveToken(rec, authReq(http.MethodPost, "/notifications/token", `{"token":"x"}`))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403 when AllowAdminEdit=false, got %d", rec.Code)
	}
}

func TestNotif_SetModeAndEnabled(t *testing.T) {
	h, st := newNotifHandler(t, &fakeTelegram{})
	h.SetMode(httptest.NewRecorder(), authReq(http.MethodPost, "/notifications/mode", `{"mode":"failures"}`))
	h.SetEnabled(httptest.NewRecorder(), authReq(http.MethodPost, "/notifications/enabled", `{"enabled":true}`))
	got, _ := st.GetNotifySettings(context.Background())
	if got.Mode != "failures" || !got.Enabled {
		t.Fatalf("settings = %+v", got)
	}
}

func TestNotif_GetSettings_MasksToken(t *testing.T) {
	h, st := newNotifHandler(t, &fakeTelegram{})
	_ = st.SaveNotifySettings(context.Background(), &store.NotifySettings{BotToken: "123456:secretpart", ChatTitle: "Room", Mode: "all"})
	rec := httptest.NewRecorder()
	h.GetSettings(rec, authReq(http.MethodGet, "/notifications", ``))
	var v map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if v["has_token"] != true {
		t.Fatalf("has_token missing: %v", v)
	}
	if strings.Contains(rec.Body.String(), "secretpart") {
		t.Fatalf("GetSettings leaked token: %s", rec.Body.String())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/httpapi/handlers/ -run TestNotif -v`
Expected: FAIL — `NotificationsHandler` / `telegramAPI` undefined.

- [ ] **Step 3: Implement the handler**

Create `internal/httpapi/handlers/notifications.go`:

```go
package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/muhac/actions-runner-pool/internal/config"
	"github.com/muhac/actions-runner-pool/internal/notify"
	"github.com/muhac/actions-runner-pool/internal/store"
)

// telegramAPI is the subset of *notify.Client the notifications handler
// needs; an interface so tests can pass a fake.
type telegramAPI interface {
	SendMessage(ctx context.Context, token, chatID, text string) error
	GetUpdates(ctx context.Context, token string) ([]notify.Update, error)
}

// NotificationsHandler serves the dashboard "Notifications" panel: the
// Telegram bot token, chat connection, mode, enabled toggle, and a test
// send. All endpoints require a valid admin bearer; mutations also
// require AllowAdminEdit (same gate as job retry/cancel and credential
// rotation).
type NotificationsHandler struct {
	Cfg      *config.Config
	Store    store.Store
	Telegram telegramAPI
	Log      *slog.Logger
}

const notifBodyLimit = 16 * 1024

// settingsView is the masked, token-free projection returned to the UI.
type settingsView struct {
	Enabled   bool   `json:"enabled"`
	Mode      string `json:"mode"`
	ChatID    string `json:"chat_id"`
	ChatTitle string `json:"chat_title"`
	HasToken  bool   `json:"has_token"`
	TokenHint string `json:"token_hint"`
}

func viewOf(n *store.NotifySettings) settingsView {
	v := settingsView{Enabled: n.Enabled, Mode: n.Mode, ChatID: n.ChatID, ChatTitle: n.ChatTitle}
	if n.BotToken != "" {
		v.HasToken = true
		tail := n.BotToken
		if len(tail) > 4 {
			tail = tail[len(tail)-4:]
		}
		v.TokenHint = "••••" + tail
	}
	return v
}

// GetSettings returns the current masked settings. Read-only: requires a
// valid bearer but not AllowAdminEdit.
func (h *NotificationsHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	if !authorizedBearer(h.Cfg, r.Header.Get("Authorization")) {
		writeAdminAuthError(w, http.StatusUnauthorized)
		return
	}
	n, err := h.Store.GetNotifySettings(r.Context())
	if err != nil {
		h.fail(w, "get notify settings", err)
		return
	}
	writeJSON(w, viewOf(n))
}

// SaveToken stores/replaces the bot token.
func (h *NotificationsHandler) SaveToken(w http.ResponseWriter, r *http.Request) {
	n, ok := h.beginMutation(w, r)
	if !ok {
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	tok := strings.TrimSpace(body.Token)
	if tok == "" {
		http.Error(w, "token must not be empty", http.StatusBadRequest)
		return
	}
	n.BotToken = tok
	h.saveAndRespond(w, r, n)
}

// Connect resolves a chat_id from the bot's recent updates (the user
// messages the bot, then clicks Connect).
func (h *NotificationsHandler) Connect(w http.ResponseWriter, r *http.Request) {
	n, ok := h.beginMutation(w, r)
	if !ok {
		return
	}
	if n.BotToken == "" {
		http.Error(w, "save a bot token first", http.StatusBadRequest)
		return
	}
	ups, err := h.Telegram.GetUpdates(r.Context(), n.BotToken)
	if err != nil {
		// 409: most commonly a webhook is set on the bot, or the token
		// is wrong. Surface Telegram's message without the token.
		http.Error(w, "could not read updates: "+err.Error(), http.StatusConflict)
		return
	}
	chat, found := latestChat(ups)
	if !found {
		http.Error(w, "no messages found — send /start to your bot, then retry", http.StatusConflict)
		return
	}
	n.ChatID = strconv.FormatInt(chat.ID, 10)
	n.ChatTitle = chatLabel(chat)
	h.saveAndRespond(w, r, n)
}

// SetMode sets "all" or "failures".
func (h *NotificationsHandler) SetMode(w http.ResponseWriter, r *http.Request) {
	n, ok := h.beginMutation(w, r)
	if !ok {
		return
	}
	var body struct {
		Mode string `json:"mode"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Mode != "all" && body.Mode != "failures" {
		http.Error(w, `mode must be "all" or "failures"`, http.StatusBadRequest)
		return
	}
	n.Mode = body.Mode
	h.saveAndRespond(w, r, n)
}

// SetEnabled flips the master on/off.
func (h *NotificationsHandler) SetEnabled(w http.ResponseWriter, r *http.Request) {
	n, ok := h.beginMutation(w, r)
	if !ok {
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	n.Enabled = body.Enabled
	h.saveAndRespond(w, r, n)
}

// Test sends a sample message to the configured chat.
func (h *NotificationsHandler) Test(w http.ResponseWriter, r *http.Request) {
	if status := adminWriteDenied(h.Cfg, r.Header.Get("Authorization")); status != 0 {
		writeAdminAuthError(w, status)
		return
	}
	n, err := h.Store.GetNotifySettings(r.Context())
	if err != nil {
		h.fail(w, "get notify settings", err)
		return
	}
	if n.BotToken == "" || n.ChatID == "" {
		http.Error(w, "configure a token and connect a chat first", http.StatusBadRequest)
		return
	}
	if err := h.Telegram.SendMessage(r.Context(), n.BotToken, n.ChatID,
		"✅ gharp test notification — your pipeline alerts are wired up."); err != nil {
		http.Error(w, "send failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// beginMutation enforces the admin-write gate and loads current settings.
func (h *NotificationsHandler) beginMutation(w http.ResponseWriter, r *http.Request) (*store.NotifySettings, bool) {
	if status := adminWriteDenied(h.Cfg, r.Header.Get("Authorization")); status != 0 {
		writeAdminAuthError(w, status)
		return nil, false
	}
	n, err := h.Store.GetNotifySettings(r.Context())
	if err != nil {
		h.fail(w, "get notify settings", err)
		return nil, false
	}
	return n, true
}

func (h *NotificationsHandler) saveAndRespond(w http.ResponseWriter, r *http.Request, n *store.NotifySettings) {
	if err := h.Store.SaveNotifySettings(r.Context(), n); err != nil {
		h.fail(w, "save notify settings", err)
		return
	}
	writeJSON(w, viewOf(n))
}

func (h *NotificationsHandler) fail(w http.ResponseWriter, msg string, err error) {
	if h.Log != nil {
		h.Log.Error(msg, "error", err)
	}
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

// decodeJSON reads a small JSON body; writes a 400 and returns false on error.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	body := http.MaxBytesReader(w, r.Body, notifBodyLimit)
	defer func() { _ = body.Close() }()
	if err := json.NewDecoder(body).Decode(dst); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return false
	}
	return true
}

// latestChat returns the chat from the most recent update that carries one.
func latestChat(ups []notify.Update) (notify.Chat, bool) {
	for i := len(ups) - 1; i >= 0; i-- {
		if ups[i].Message.Chat.ID != 0 {
			return ups[i].Message.Chat, true
		}
	}
	return notify.Chat{}, false
}

// chatLabel is a human-readable name for a resolved chat.
func chatLabel(c notify.Chat) string {
	switch {
	case c.Title != "":
		return c.Title
	case c.Username != "":
		return "@" + c.Username
	default:
		return "chat " + strconv.FormatInt(c.ID, 10)
	}
}
```

Note: `writeJSON` already exists in this package (used by `AppConfigHandler`); reuse it. Do not redefine it.

- [ ] **Step 4: Run the handler tests**

Run: `go test ./internal/httpapi/handlers/ -run TestNotif -v`
Expected: PASS (all six).

- [ ] **Step 5: Wire the routes**

In `internal/httpapi/router.go`, add the import:

```go
	"github.com/muhac/actions-runner-pool/internal/notify"
```

After the webhook handler wiring, build the Telegram client and attach it, then register routes. Update the `WebhookHandler` construction to include `Telegram`:

```go
	tg := notify.New()

	wh := &handlers.WebhookHandler{Cfg: cfg, Store: st, Scheduler: sch, Telegram: tg, Log: log}
	mux.HandleFunc("POST /github/webhook", wh.Post)

	notif := &handlers.NotificationsHandler{Cfg: cfg, Store: st, Telegram: tg, Log: log}
	mux.HandleFunc("GET /notifications", notif.GetSettings)
	mux.HandleFunc("POST /notifications/token", notif.SaveToken)
	mux.HandleFunc("POST /notifications/connect", notif.Connect)
	mux.HandleFunc("POST /notifications/mode", notif.SetMode)
	mux.HandleFunc("POST /notifications/enabled", notif.SetEnabled)
	mux.HandleFunc("POST /notifications/test", notif.Test)
```

(Replace the existing `wh := &handlers.WebhookHandler{...}` line with the version above that adds `Telegram: tg`.)

- [ ] **Step 6: Build and run the whole module's tests**

Run: `go build ./... && go test ./...`
Expected: PASS, build clean.

- [ ] **Step 7: Commit**

```bash
git add internal/httpapi/handlers/notifications.go internal/httpapi/handlers/notifications_test.go internal/httpapi/router.go
git commit -m "feat(httpapi): notifications settings endpoints + Telegram wiring"
```

---

## Task 5: Dashboard UI — "Notifications" panel

**Files:**
- Modify: `internal/httpapi/handlers/templates/dashboard.html`

**Interfaces:**
- Consumes: the `/notifications*` endpoints (Task 4). Uses the page's existing `State.token` and relative `fetch` convention.

This task is UI wiring; it is verified by `go build`/`go vet` (template parses at startup via `template.Must`) and a manual smoke check. No new Go test.

- [ ] **Step 1: Add the panel markup**

In `internal/httpapi/handlers/templates/dashboard.html`, add a panel near the existing admin/token UI (the same area that explains "Stored in this browser only (sessionStorage)"). Use the existing card/panel classes already in the file (match the surrounding markup — e.g. the same wrapper class used by other dashboard cards):

```html
<section class="card" id="notifyCard">
  <h2>Notifications</h2>
  <p class="muted">Send a Telegram message when a workflow run finishes.</p>

  <label>Bot token
    <input id="ntToken" type="password" placeholder="123456:ABC-..." autocomplete="off">
  </label>
  <button id="ntSaveToken" type="button">Save token</button>
  <span id="ntTokenState" class="muted"></span>

  <div class="ntRow">
    <button id="ntConnect" type="button">Connect chat</button>
    <span id="ntChatState" class="muted">Send <code>/start</code> to your bot, then click Connect.</span>
  </div>

  <label>Notify on
    <select id="ntMode">
      <option value="all">All runs</option>
      <option value="failures">Failures only</option>
    </select>
  </label>

  <label class="ntInline">
    <input id="ntEnabled" type="checkbox"> Enabled
  </label>

  <button id="ntTest" type="button">Send test</button>
  <span id="ntTestState" class="muted"></span>
</section>
```

- [ ] **Step 2: Add the panel script**

In the page's `<script>` block (where `State.token` and `mutate()` are defined), add:

```javascript
function ntHeaders(json){
  const h = State.token ? {Authorization:'Bearer '+State.token} : {};
  if(json) h['Content-Type']='application/json';
  return h;
}
function ntSet(id,msg){ const el=document.getElementById(id); if(el) el.textContent=msg; }

async function ntLoad(){
  try{
    const r = await fetch('notifications',{headers:ntHeaders(false)});
    if(!r.ok) return;
    const v = await r.json();
    document.getElementById('ntMode').value = v.mode || 'all';
    document.getElementById('ntEnabled').checked = !!v.enabled;
    ntSet('ntTokenState', v.has_token ? ('token set '+(v.token_hint||'')) : 'no token');
    ntSet('ntChatState', v.chat_id ? ('connected: '+(v.chat_title||v.chat_id)) : 'not connected');
  }catch(e){/* panel stays at defaults */}
}

async function ntPost(path, body){
  const r = await fetch(path, {method:'POST', headers:ntHeaders(!!body), body: body?JSON.stringify(body):undefined});
  if(!r.ok){ throw new Error((await r.text()) || ('HTTP '+r.status)); }
  return r.status===204 ? null : r.json();
}

document.getElementById('ntSaveToken').addEventListener('click', async ()=>{
  const tok = document.getElementById('ntToken').value.trim();
  try{ const v=await ntPost('notifications/token',{token:tok});
       document.getElementById('ntToken').value='';
       ntSet('ntTokenState','token set '+(v.token_hint||'')); }
  catch(e){ ntSet('ntTokenState','error: '+e.message); }
});
document.getElementById('ntConnect').addEventListener('click', async ()=>{
  try{ const v=await ntPost('notifications/connect',null);
       ntSet('ntChatState','connected: '+(v.chat_title||v.chat_id)); }
  catch(e){ ntSet('ntChatState','error: '+e.message); }
});
document.getElementById('ntMode').addEventListener('change', async (ev)=>{
  try{ await ntPost('notifications/mode',{mode:ev.target.value}); }catch(e){ ntSet('ntChatState','error: '+e.message); }
});
document.getElementById('ntEnabled').addEventListener('change', async (ev)=>{
  try{ await ntPost('notifications/enabled',{enabled:ev.target.checked}); }catch(e){ ntSet('ntChatState','error: '+e.message); }
});
document.getElementById('ntTest').addEventListener('click', async ()=>{
  try{ await ntPost('notifications/test',null); ntSet('ntTestState','sent ✓'); }
  catch(e){ ntSet('ntTestState','error: '+e.message); }
});

ntLoad();
```

- [ ] **Step 3: Build to verify the template still parses**

Run: `go build ./... && go vet ./internal/httpapi/...`
Expected: clean (the template is parsed by `template.Must` at package init; a broken template would fail the test binary build/run).

- [ ] **Step 4: Run the handlers package tests (template init path)**

Run: `go test ./internal/httpapi/...`
Expected: PASS (template parses; `template.Must` did not panic).

- [ ] **Step 5: Manual smoke (document, do not block on infra)**

With a running instance + admin token entered in the UI: enter a bot token → Save; message the bot `/start` → Connect (shows "connected: <chat>"); set mode + Enabled; click Send test → a Telegram message arrives. (Out-of-band; no automated assertion.)

- [ ] **Step 6: Commit**

```bash
git add internal/httpapi/handlers/templates/dashboard.html
git commit -m "feat(dashboard): Notifications panel for Telegram alerts"
```

---

## Post-implementation: documentation note

After the five tasks land, add one line to the user-facing setup guide (the in-product "Set up runners" guide / the portal `user.html` guide overlay, or `README.md` security/usage section as appropriate): existing tenants must tick **"Workflow run"** under *GitHub App → Settings → Permissions & events → Subscribe to events* (no re-install needed — `actions: read` is already granted). New installs get it automatically from the manifest. This is a docs-only follow-up, not part of the Go build.

---

## Self-Review

**Spec coverage:**
- Placement in gharp, portal untouched → Tasks 1–5 are all gharp-internal. ✓
- workflow_run granularity → Task 3. ✓
- Manifest subscribe + existing-install checkbox → Task 3 (code) + Post-implementation note (docs). ✓
- Webhook handler completed/disabled/failures-only/best-effort → Task 3 tests. ✓
- internal/notify SendMessage + GetUpdates + egress note → Task 2 (egress is environmental, documented in spec). ✓
- Store notify_settings + Get/Save → Task 1. ✓
- Dashboard panel: token (masked), Connect via getUpdates, mode, enabled, test → Tasks 4 (endpoints) + 5 (UI). ✓
- Message format → `buildRunMessage` in Task 3 (matches spec sample). ✓
- Security: token never logged/echoed, masked in UI/GetSettings, 409 on getUpdates webhook conflict → Tasks 2/4. ✓
- Tests: webhook, notify client, store round-trip, handlers → Tasks 1–4. ✓
- Out of scope (manual chat_id, per-repo filters, other channels) → not implemented. ✓

**Placeholder scan:** No TBD/TODO; every code step shows full code. ✓

**Type consistency:** `messageSender.SendMessage(ctx, token, chatID, text) error` is identical in webhook.go and is a subset of `telegramAPI`; `*notify.Client` implements both. `store.NotifySettings` fields (`Enabled, BotToken, ChatID, ChatTitle, Mode`) are used identically across Tasks 1, 3, 4. `GetNotifySettings`/`SaveNotifySettings` signatures match interface and impl. Router passes one shared `tg := notify.New()` to both handlers. ✓
