package proxy

import (
	"context"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

type contextKey int

const userIDKey contextKey = 0

// WithUserID returns a context carrying the authenticated user's ID.
// Auth middleware (WS-B/E) must call this before forwarding to Handler.ServeHTTP.
func WithUserID(ctx context.Context, id int64) context.Context {
	return context.WithValue(ctx, userIDKey, id)
}

// UserIDFromCtx extracts the user ID injected by WithUserID.
func UserIDFromCtx(ctx context.Context) (int64, bool) {
	id, ok := ctx.Value(userIDKey).(int64)
	return id, ok
}

// Handler is the reverse-proxy http.Handler for authenticated users.
// Each request is routed to the user's own gharp instance; ownership is
// enforced, /app prefix is stripped, and the server-side ADMIN_TOKEN is
// injected while any client-supplied Authorization header is stripped.
type Handler struct {
	store Store
}

// New creates a Handler backed by store.
func New(store Store) *Handler {
	return &Handler{store: store}
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	userID, ok := UserIDFromCtx(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	a, ok, err := h.store.GetAssignmentByUser(userID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "no slot assigned", http.StatusForbidden)
		return
	}

	slot, ok, err := h.store.GetSlot(a.SlotID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "slot not found", http.StatusInternalServerError)
		return
	}

	target, err := url.Parse("http://" + slot.InternalAddr)
	if err != nil {
		http.Error(w, "invalid slot address", http.StatusInternalServerError)
		return
	}

	adminToken := slot.AdminToken

	rp := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host

			// Strip /app prefix; fall back to "/" if path becomes empty.
			path := strings.TrimPrefix(req.URL.Path, "/app")
			if path == "" {
				path = "/"
			}
			req.URL.Path = path
			if req.URL.RawPath != "" {
				raw := strings.TrimPrefix(req.URL.RawPath, "/app")
				if raw == "" {
					raw = "/"
				}
				req.URL.RawPath = raw
			}

			// Strip client-supplied Authorization; inject server-side token.
			req.Header.Del("Authorization")
			if adminToken != "" {
				req.Header.Set("Authorization", "Bearer "+adminToken)
			}
		},
		// Flush immediately — required for SSE and gharp's 10 s long-poll.
		FlushInterval: -1,
	}
	rp.ServeHTTP(w, r)
}
