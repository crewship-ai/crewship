package api

// Tests for CrewHandler.ContainerStatus — the GET
// /api/v1/crews/{crewId}/container-status endpoint that proxies a crew's
// container status from crewshipd over the IPC unix socket. A fake IPC server
// on a real unix socket exercises the proxy path; the branch tests cover
// workspace scoping and the socket-not-configured fallback.

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// covCCSSeed inserts a workspace + crew (slug "alpha") and returns their ids.
func covCCSSeed(t *testing.T, h *CrewHandler) (wsID, crewID string) {
	t.Helper()
	wsID, crewID = "ws-ccs", "crew-ccs"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'W', 'w-ccs')`, wsID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Alpha', 'alpha')`, crewID, wsID); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	return wsID, crewID
}

func covCCSNewHandler(t *testing.T) *CrewHandler {
	t.Helper()
	h := NewCrewHandler(setupTestDB(t), slog.New(slog.NewTextHandler(discardWriterCovCPR{}, nil)))
	return h
}

// startFakeIPC serves an HTTP handler on a unix socket and returns its path.
func startFakeIPC(t *testing.T, handler http.Handler) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "ipc.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

func TestContainerStatus_MissingCrewID(t *testing.T) {
	h := covCCSNewHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/crews//container-status", nil)
	req.SetPathValue("crewId", "")
	req = req.WithContext(withWorkspace(req.Context(), "ws-ccs", "member"))
	rec := httptest.NewRecorder()

	h.ContainerStatus(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestContainerStatus_CrossWorkspace404(t *testing.T) {
	h := covCCSNewHandler(t)
	_, crewID := covCCSSeed(t, h)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/crews/"+crewID+"/container-status", nil)
	req.SetPathValue("crewId", crewID)
	// Request carries a DIFFERENT workspace — must not leak the crew.
	req = req.WithContext(withWorkspace(req.Context(), "ws-other", "member"))
	rec := httptest.NewRecorder()

	h.ContainerStatus(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404 for cross-workspace, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestContainerStatus_NotConfigured(t *testing.T) {
	h := covCCSNewHandler(t)
	wsID, crewID := covCCSSeed(t, h)
	// socketPath left empty → not_configured, not a 500.

	req := httptest.NewRequest(http.MethodGet, "/api/v1/crews/"+crewID+"/container-status", nil)
	req.SetPathValue("crewId", crewID)
	req = req.WithContext(withWorkspace(req.Context(), wsID, "member"))
	rec := httptest.NewRecorder()

	h.ContainerStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "not_configured" {
		t.Errorf("want status not_configured, got %v", body["status"])
	}
	if body["crew_id"] != crewID {
		t.Errorf("want crew_id %s, got %v", crewID, body["crew_id"])
	}
}

func TestContainerStatus_ProxiesIPC(t *testing.T) {
	h := covCCSNewHandler(t)
	wsID, crewID := covCCSSeed(t, h)

	var gotPath string
	sock := startFakeIPC(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"crew_id": "ignored-by-ipc", "status": "running", "uptime": "2026-07-13T00:00:00Z",
		})
	}))
	h.SetSocketPath(sock)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/crews/"+crewID+"/container-status", nil)
	req.SetPathValue("crewId", crewID)
	req = req.WithContext(withWorkspace(req.Context(), wsID, "member"))
	rec := httptest.NewRecorder()

	h.ContainerStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	// The API must call the IPC status route keyed by the RAW crew ID
	// (the crewshipd side resolves slug→container name).
	if gotPath != "/crews/"+crewID+"/container/status" {
		t.Errorf("IPC path = %q, want /crews/%s/container/status", gotPath, crewID)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "running" {
		t.Errorf("want status running, got %v", body["status"])
	}
	// crew_id in the response is always overwritten with the caller's id,
	// never trusting the IPC-supplied value.
	if body["crew_id"] != crewID {
		t.Errorf("want crew_id %s, got %v", crewID, body["crew_id"])
	}
}

func TestContainerStatus_IPCUnavailable(t *testing.T) {
	h := covCCSNewHandler(t)
	wsID, crewID := covCCSSeed(t, h)
	// Point at a socket path that does not exist → dial fails → unknown, 200.
	h.SetSocketPath(filepath.Join(t.TempDir(), "nope.sock"))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/crews/"+crewID+"/container-status", nil)
	req.SetPathValue("crewId", crewID)
	req = req.WithContext(withWorkspace(req.Context(), wsID, "member"))
	rec := httptest.NewRecorder()

	h.ContainerStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 (graceful), got %d", rec.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["status"] != "unknown" {
		t.Errorf("want status unknown when IPC down, got %v", body["status"])
	}
}
