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
