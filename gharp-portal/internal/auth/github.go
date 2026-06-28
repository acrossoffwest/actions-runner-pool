package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// GitHubUser holds the fields we need from the GitHub /user API.
type GitHubUser struct {
	ID    int64
	Login string
}

// GitHubClient abstracts GitHub OAuth token exchange and user-info calls.
// Inject a mock in tests; use NewHTTPGitHubClient in production.
type GitHubClient interface {
	ExchangeCode(ctx context.Context, code string) (accessToken string, err error)
	GetUser(ctx context.Context, accessToken string) (GitHubUser, error)
}

// NewHTTPGitHubClient returns a real GitHubClient that calls github.com.
func NewHTTPGitHubClient(clientID, clientSecret string) GitHubClient {
	return &httpGitHubClient{
		clientID:     clientID,
		clientSecret: clientSecret,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
		tokenURL:     "https://github.com/login/oauth/access_token",
		apiBaseURL:   "https://api.github.com",
	}
}

type httpGitHubClient struct {
	clientID     string
	clientSecret string
	httpClient   *http.Client
	tokenURL     string
	apiBaseURL   string
}

func (c *httpGitHubClient) ExchangeCode(ctx context.Context, code string) (string, error) {
	body := url.Values{
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
		"code":          {code},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL,
		strings.NewReader(body.Encode()))
	if err != nil {
		return "", fmt.Errorf("github: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("github: token exchange: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("github: decode token response: %w", err)
	}
	if result.Error != "" {
		return "", errors.New("github: " + result.Error + ": " + result.ErrorDesc)
	}
	if result.AccessToken == "" {
		return "", errors.New("github: empty access token")
	}
	return result.AccessToken, nil
}

func (c *httpGitHubClient) GetUser(ctx context.Context, accessToken string) (GitHubUser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiBaseURL+"/user", nil)
	if err != nil {
		return GitHubUser{}, fmt.Errorf("github: build user request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return GitHubUser{}, fmt.Errorf("github: get user: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return GitHubUser{}, fmt.Errorf("github: user API returned %d", resp.StatusCode)
	}

	var u struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return GitHubUser{}, fmt.Errorf("github: decode user response: %w", err)
	}
	if u.ID == 0 || u.Login == "" {
		return GitHubUser{}, errors.New("github: missing id or login in user response")
	}
	return GitHubUser{ID: u.ID, Login: u.Login}, nil
}
