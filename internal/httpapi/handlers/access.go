package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/muhac/actions-runner-pool/internal/config"
	"github.com/muhac/actions-runner-pool/internal/store"
)

// AccessHandler serves the "Access control" panel: the owner allowlist that
// decides whose repositories may launch runners on this slot. The App owner
// is always allowed and is shown read-only.
type AccessHandler struct {
	Cfg   *config.Config
	Store store.Store
	Log   *slog.Logger
}

const accessBodyLimit = 16 * 1024

type accessView struct {
	AppOwner      string   `json:"app_owner"`
	AllowedOwners []string `json:"allowed_owners"`
}

// splitOwners parses the stored comma list into a slice (trimmed, no empties).
func splitOwners(s string) []string {
	out := []string{}
	for _, e := range strings.Split(s, ",") {
		if e = strings.TrimSpace(e); e != "" {
			out = append(out, e)
		}
	}
	return out
}

// normalizeOwners trims, drops empties, and de-duplicates (case-insensitive,
// keeping first spelling), returning the cleaned slice.
func normalizeOwners(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, e := range in {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		k := strings.ToLower(e)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, e)
	}
	return out
}

// GetAccess returns the App owner and the configured allowed owners.
func (h *AccessHandler) GetAccess(w http.ResponseWriter, r *http.Request) {
	if !authorizedBearer(h.Cfg, r.Header.Get("Authorization")) {
		writeAdminAuthError(w, http.StatusUnauthorized)
		return
	}
	appOwner := ""
	if cfg, err := h.Store.GetAppConfig(r.Context()); err != nil {
		h.fail(w, "get app config", err)
		return
	} else if cfg != nil {
		appOwner = cfg.OwnerLogin
	}
	a, err := h.Store.GetAccessSettings(r.Context())
	if err != nil {
		h.fail(w, "get access settings", err)
		return
	}
	writeJSON(w, accessView{AppOwner: appOwner, AllowedOwners: splitOwners(a.AllowedOwners)})
}

// SaveOwners replaces the allowed-owners list.
func (h *AccessHandler) SaveOwners(w http.ResponseWriter, r *http.Request) {
	if status := adminWriteDenied(h.Cfg, r.Header.Get("Authorization")); status != 0 {
		writeAdminAuthError(w, status)
		return
	}
	body := http.MaxBytesReader(w, r.Body, accessBodyLimit)
	defer func() { _ = body.Close() }()
	var req struct {
		Owners []string `json:"owners"`
	}
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	owners := normalizeOwners(req.Owners)
	if err := h.Store.SaveAccessSettings(r.Context(), &store.AccessSettings{AllowedOwners: strings.Join(owners, ",")}); err != nil {
		h.fail(w, "save access settings", err)
		return
	}
	appOwner := ""
	if cfg, err := h.Store.GetAppConfig(r.Context()); err == nil && cfg != nil {
		appOwner = cfg.OwnerLogin
	}
	writeJSON(w, accessView{AppOwner: appOwner, AllowedOwners: owners})
}

func (h *AccessHandler) fail(w http.ResponseWriter, msg string, err error) {
	if h.Log != nil {
		h.Log.Error(msg, "error", err)
	}
	http.Error(w, "internal server error", http.StatusInternalServerError)
}
