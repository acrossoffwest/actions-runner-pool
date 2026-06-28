// Package notify sends messages through the Telegram Bot API.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
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

// redactToken returns err with every occurrence of token removed. net/http
// embeds the request URL — which contains "/bot<token>/..." — in the
// *url.Error returned by Do, so a transport failure would otherwise leak the
// bot token into logs or HTTP responses. Security-critical: the token is a
// secret and must never surface.
func redactToken(err error, token string) error {
	if err == nil {
		return nil
	}
	if token == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), token, "<redacted>"))
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
		return redactToken(err, token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return redactToken(err, token)
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
		return nil, redactToken(err, token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, redactToken(err, token)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		var ae apiError
		_ = json.Unmarshal(body, &ae)
		if ae.Description != "" {
			return nil, fmt.Errorf("telegram getUpdates: %s", ae.Description)
		}
		return nil, fmt.Errorf("telegram getUpdates: HTTP %d", resp.StatusCode)
	}
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
