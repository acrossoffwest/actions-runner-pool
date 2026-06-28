package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/muhac/actions-runner-pool/internal/config"
	"github.com/muhac/actions-runner-pool/internal/store"
)

func newAccessHandler(t *testing.T) (*AccessHandler, *store.SQLite) {
	t.Helper()
	st, err := store.OpenSQLite("file:" + t.TempDir() + "/a.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg := &config.Config{AdminToken: "secret", AllowAdminEdit: true}
	return &AccessHandler{Cfg: cfg, Store: st}, st
}

func accessReq(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer secret")
	r.Header.Set("Content-Type", "application/json")
	return r
}

func TestAccess_GetReturnsAppOwnerAndList(t *testing.T) {
	h, st := newAccessHandler(t)
	_ = st.SaveAppConfig(context.Background(), &store.AppConfig{
		AppID: 1, Slug: "s", WebhookSecret: "wsecretwsecret16", PEM: []byte("p"),
		ClientID: "c", BaseURL: "https://x", OwnerLogin: "acrossoffwest",
	})
	_ = st.SaveAccessSettings(context.Background(), &store.AccessSettings{AllowedOwners: "tmgr-dev"})
	rec := httptest.NewRecorder()
	h.GetAccess(rec, accessReq(http.MethodGet, "/access", ``))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var v struct {
		AppOwner      string   `json:"app_owner"`
		AllowedOwners []string `json:"allowed_owners"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if v.AppOwner != "acrossoffwest" || len(v.AllowedOwners) != 1 || v.AllowedOwners[0] != "tmgr-dev" {
		t.Fatalf("view = %+v", v)
	}
}

func TestAccess_SaveOwnersNormalizes(t *testing.T) {
	h, st := newAccessHandler(t)
	rec := httptest.NewRecorder()
	h.SaveOwners(rec, accessReq(http.MethodPost, "/access/owners", `{"owners":[" tmgr-dev ","acme","","tmgr-dev"]}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	got, _ := st.GetAccessSettings(context.Background())
	if got.AllowedOwners != "tmgr-dev,acme" {
		t.Fatalf("stored = %q (want trimmed, de-duped, no empties)", got.AllowedOwners)
	}
}

func TestAccess_SaveRequiresAdminEdit(t *testing.T) {
	h, _ := newAccessHandler(t)
	h.Cfg.AllowAdminEdit = false
	rec := httptest.NewRecorder()
	h.SaveOwners(rec, accessReq(http.MethodPost, "/access/owners", `{"owners":["x"]}`))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rec.Code)
	}
}
