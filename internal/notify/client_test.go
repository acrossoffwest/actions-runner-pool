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
