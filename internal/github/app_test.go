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
