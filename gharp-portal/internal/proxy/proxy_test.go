package proxy_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gharp/portal/internal/proxy"
)

// ---- mock store ----

type mockStore struct {
	assignment    proxy.Assignment
	hasAssignment bool
	slot          proxy.Slot
	hasSlot       bool
	storeErr      error
}

func (s *mockStore) GetAssignmentByUser(_ int64) (proxy.Assignment, bool, error) {
	return s.assignment, s.hasAssignment, s.storeErr
}
func (s *mockStore) GetSlot(_ string) (proxy.Slot, bool, error) {
	return s.slot, s.hasSlot, s.storeErr
}

// ---- helpers ----

type capturedReq struct {
	path          string
	authorization string
}

// newCapturingBackend returns a test backend that captures headers/path and responds with body.
func newCapturingBackend(t *testing.T, body string) (*httptest.Server, *capturedReq) {
	t.Helper()
	cap := &capturedReq{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.path = r.URL.Path
		cap.authorization = r.Header.Get("Authorization")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, cap
}

func addrOf(srv *httptest.Server) string {
	return strings.TrimPrefix(srv.URL, "http://")
}

// serve calls h.ServeHTTP directly (no network hop); userID is injected into context.
func serve(h *proxy.Handler, method, path string, userID int64, clientHeaders http.Header) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	for k, vs := range clientHeaders {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req = req.WithContext(proxy.WithUserID(req.Context(), userID))
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, req)
	return rw
}

// ---- path stripping ----

func TestProxy_StripAppPrefix(t *testing.T) {
	tests := []struct {
		requestPath string
		wantPath    string
	}{
		{"/app", "/"},
		{"/app/", "/"},
		{"/app/runners", "/runners"},
		{"/app/jobs/123", "/jobs/123"},
		{"/app/stats", "/stats"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.requestPath, func(t *testing.T) {
			backend, cap := newCapturingBackend(t, "ok")
			st := &mockStore{
				hasAssignment: true,
				assignment:    proxy.Assignment{UserID: 1, SlotID: "slot-1"},
				hasSlot:       true,
				slot:          proxy.Slot{InternalAddr: addrOf(backend)},
			}
			h := proxy.New(st)
			rw := serve(h, http.MethodGet, tc.requestPath, 1, nil)
			if rw.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rw.Code, rw.Body.String())
			}
			if cap.path != tc.wantPath {
				t.Errorf("backend path: want %q, got %q", tc.wantPath, cap.path)
			}
		})
	}
}

// ---- auth header handling ----

func TestProxy_InjectsServerToken(t *testing.T) {
	backend, cap := newCapturingBackend(t, "ok")
	st := &mockStore{
		hasAssignment: true,
		assignment:    proxy.Assignment{UserID: 1, SlotID: "slot-1"},
		hasSlot:       true,
		slot:          proxy.Slot{InternalAddr: addrOf(backend), AdminToken: "secret-admin-token"},
	}
	h := proxy.New(st)
	serve(h, http.MethodGet, "/app/stats", 1, nil)

	if cap.authorization != "Bearer secret-admin-token" {
		t.Errorf("Authorization: want %q, got %q", "Bearer secret-admin-token", cap.authorization)
	}
}

func TestProxy_StripsClientAuthorization(t *testing.T) {
	backend, cap := newCapturingBackend(t, "ok")
	st := &mockStore{
		hasAssignment: true,
		assignment:    proxy.Assignment{UserID: 1, SlotID: "slot-1"},
		hasSlot:       true,
		slot:          proxy.Slot{InternalAddr: addrOf(backend), AdminToken: "server-token"},
	}
	h := proxy.New(st)

	// Client tries to inject its own auth token.
	headers := http.Header{"Authorization": {"Bearer evil-client-token"}}
	serve(h, http.MethodGet, "/app/stats", 1, headers)

	if cap.authorization == "Bearer evil-client-token" {
		t.Error("client Authorization must not reach backend")
	}
	if cap.authorization != "Bearer server-token" {
		t.Errorf("server token not injected; backend got %q", cap.authorization)
	}
}

func TestProxy_NoTokenWhenEmpty(t *testing.T) {
	backend, cap := newCapturingBackend(t, "ok")
	st := &mockStore{
		hasAssignment: true,
		assignment:    proxy.Assignment{UserID: 1, SlotID: "slot-1"},
		hasSlot:       true,
		slot:          proxy.Slot{InternalAddr: addrOf(backend), AdminToken: ""},
	}
	h := proxy.New(st)
	serve(h, http.MethodGet, "/app/stats", 1, nil)

	if cap.authorization != "" {
		t.Errorf("no Authorization should be sent when AdminToken is empty, got %q", cap.authorization)
	}
}

// ---- access control ----

func TestProxy_NoSession_Returns401(t *testing.T) {
	st := &mockStore{}
	h := proxy.New(st)

	req := httptest.NewRequest(http.MethodGet, "/app/stats", nil)
	// No userID in context.
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, req)

	if rw.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without session, got %d", rw.Code)
	}
}

func TestProxy_NoAssignment_Returns403(t *testing.T) {
	st := &mockStore{hasAssignment: false}
	h := proxy.New(st)
	rw := serve(h, http.MethodGet, "/app/stats", 99, nil)

	if rw.Code != http.StatusForbidden {
		t.Errorf("expected 403 for user with no slot, got %d", rw.Code)
	}
}

func TestProxy_StoreError_Returns500(t *testing.T) {
	st := &mockStore{storeErr: fmt.Errorf("db down"), hasAssignment: true}
	h := proxy.New(st)
	rw := serve(h, http.MethodGet, "/app/stats", 1, nil)

	if rw.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 on store error, got %d", rw.Code)
	}
}

func TestProxy_SlotNotFound_Returns500(t *testing.T) {
	st := &mockStore{
		hasAssignment: true,
		assignment:    proxy.Assignment{UserID: 1, SlotID: "slot-1"},
		hasSlot:       false, // slot disappeared
	}
	h := proxy.New(st)
	rw := serve(h, http.MethodGet, "/app/stats", 1, nil)

	if rw.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 when slot not found, got %d", rw.Code)
	}
}

// ---- response forwarding ----

func TestProxy_ForwardsResponseBody(t *testing.T) {
	backend, _ := newCapturingBackend(t, "hello from gharp")
	st := &mockStore{
		hasAssignment: true,
		assignment:    proxy.Assignment{UserID: 1, SlotID: "slot-1"},
		hasSlot:       true,
		slot:          proxy.Slot{InternalAddr: addrOf(backend)},
	}
	h := proxy.New(st)
	rw := serve(h, http.MethodGet, "/app/", 1, nil)

	if got := rw.Body.String(); got != "hello from gharp" {
		t.Errorf("body: want %q, got %q", "hello from gharp", got)
	}
}

// TestProxy_Streaming verifies chunked streaming responses are not buffered by
// the proxy. We wrap the proxy in a real httptest.Server so Flushing works.
func TestProxy_Streaming(t *testing.T) {
	const chunks = 5

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("backend ResponseWriter is not http.Flusher")
			return
		}
		for i := 0; i < chunks; i++ {
			fmt.Fprintf(w, "chunk%d", i)
			flusher.Flush()
		}
	}))
	t.Cleanup(backend.Close)

	st := &mockStore{
		hasAssignment: true,
		assignment:    proxy.Assignment{UserID: 1, SlotID: "slot-1"},
		hasSlot:       true,
		slot:          proxy.Slot{InternalAddr: addrOf(backend)},
	}
	h := proxy.New(st)

	// Wrap proxy in a real HTTP server; middleware injects userID, simulating WS-B/E.
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(proxy.WithUserID(r.Context(), 1))
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(proxySrv.Close)

	resp, err := http.Get(proxySrv.URL + "/app/events") //nolint:noctx
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	want := "chunk0chunk1chunk2chunk3chunk4"
	if string(body) != want {
		t.Errorf("streaming body: want %q, got %q", want, string(body))
	}
}
