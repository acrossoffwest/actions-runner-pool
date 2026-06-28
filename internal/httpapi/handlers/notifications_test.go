package handlers

import (
	"context"
	"encoding/json"
	"errors"
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
	getErr   error
	sentText string
}

func (f *fakeTelegram) SendMessage(_ context.Context, _, _, text string) error {
	f.sentText = text
	return f.sendErr
}
func (f *fakeTelegram) GetUpdates(_ context.Context, _ string) ([]notify.Update, error) {
	return f.updates, f.getErr
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

func TestNotif_Connect_GetUpdatesError_409(t *testing.T) {
	h, st := newNotifHandler(t, &fakeTelegram{getErr: errors.New("webhook is set")})
	_ = st.SaveNotifySettings(context.Background(), &store.NotifySettings{BotToken: "t", Mode: "all"})
	rec := httptest.NewRecorder()
	h.Connect(rec, authReq(http.MethodPost, "/notifications/connect", ``))
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409 on GetUpdates error, got %d", rec.Code)
	}
}

func TestNotif_Test_Success(t *testing.T) {
	tg := &fakeTelegram{}
	h, st := newNotifHandler(t, tg)
	_ = st.SaveNotifySettings(context.Background(), &store.NotifySettings{BotToken: "t", ChatID: "9", Mode: "all"})
	rec := httptest.NewRecorder()
	h.Test(rec, authReq(http.MethodPost, "/notifications/test", ``))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d body=%s", rec.Code, rec.Body.String())
	}
	if tg.sentText == "" {
		t.Fatal("expected a test message to be sent")
	}
}

func TestNotif_Test_SendError_502(t *testing.T) {
	h, st := newNotifHandler(t, &fakeTelegram{sendErr: errors.New("boom")})
	_ = st.SaveNotifySettings(context.Background(), &store.NotifySettings{BotToken: "t", ChatID: "9", Mode: "all"})
	rec := httptest.NewRecorder()
	h.Test(rec, authReq(http.MethodPost, "/notifications/test", ``))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("want 502 on send error, got %d", rec.Code)
	}
}

func TestNotif_SetMode_Invalid_400(t *testing.T) {
	h, _ := newNotifHandler(t, &fakeTelegram{})
	rec := httptest.NewRecorder()
	h.SetMode(rec, authReq(http.MethodPost, "/notifications/mode", `{"mode":"everything"}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for invalid mode, got %d", rec.Code)
	}
}
