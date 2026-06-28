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
