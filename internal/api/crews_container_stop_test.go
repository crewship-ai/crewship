package api

// The counterpart to ContainerStart. Before it, a crew container could
// be started deliberately but only stopped by accident — an idle TTL
// expiring, or a network-policy edit dropping it as a side effect. An
// operator who started three crews to land a restore had no way to give
// the memory back.
//
// The stop itself lives on crewshipd (handleContainerStop already stops
// the runtime AND the crew's sidecars), so this endpoint proxies rather
// than reimplementing. Reimplementing the sidecar half in a second place
// is exactly how a teardown once reached into another tenant's Postgres
// (#1732).

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func stopRequest(crewID, wsID, role string) *http.Request {
	req := httptest.NewRequest("POST", "/api/v1/crews/"+crewID+"/container-stop", nil)
	req.SetPathValue("crewId", crewID)
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxRole, role)
	return req.WithContext(ctx)
}

// stopIPCRecorder answers crewshipd's container/stop route and records
// that it was reached.
type stopIPCRecorder struct {
	hits   int
	path   string
	status int
}

func (s *stopIPCRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.hits++
	s.path = r.URL.Path
	code := s.status
	if code == 0 {
		code = http.StatusOK
	}
	w.WriteHeader(code)
	_, _ = w.Write([]byte(`{"status":"stopped"}`))
}

func TestContainerStop_StopsViaCrewshipd(t *testing.T) {
	h, wsID, crewID := startTestHandler(t, nil)
	ipc := &stopIPCRecorder{}
	h.SetSocketPath(startFakeIPC(t, ipc))
	act := &startFakeActivity{}
	h.SetActivityNoter(act)

	rec := httptest.NewRecorder()
	h.ContainerStop(rec, stopRequest(crewID, wsID, "OWNER"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "stopped" {
		t.Errorf("status = %v, want stopped", body["status"])
	}
	if ipc.hits != 1 {
		t.Fatalf("crewshipd hit %d times, want 1", ipc.hits)
	}
	// It has to be the route that also stops the sidecars. Any other and
	// the crew's Postgres keeps its memory after the crew is "stopped".
	if !strings.Contains(ipc.path, "/container/stop") {
		t.Errorf("hit %q, want crewshipd's container/stop route", ipc.path)
	}
}

// Symmetric with the note on start: the reaper's entry points at a
// container that no longer exists, so leaving it makes the reaper
// eventually log an idle expiry for something a human stopped.
func TestContainerStop_ForgetsTheReaperEntry(t *testing.T) {
	h, wsID, crewID := startTestHandler(t, nil)
	h.SetSocketPath(startFakeIPC(t, &stopIPCRecorder{}))
	act := &startFakeActivity{}
	h.SetActivityNoter(act)

	rec := httptest.NewRecorder()
	h.ContainerStop(rec, stopRequest(crewID, wsID, "OWNER"))

	if act.forgotten != 1 {
		t.Errorf("ForgetCrewActivity called %d times, want 1", act.forgotten)
	}
	if act.crewID != crewID {
		t.Errorf("forgot %q, want %q", act.crewID, crewID)
	}
}

// A failed stop must not drop the reaper's entry: the container may well
// still be running, and forgetting it is how it would then outlive its
// TTL with nothing tracking it.
func TestContainerStop_KeepsReaperEntryWhenTheStopFailed(t *testing.T) {
	h, wsID, crewID := startTestHandler(t, nil)
	h.SetSocketPath(startFakeIPC(t, &stopIPCRecorder{status: http.StatusInternalServerError}))
	act := &startFakeActivity{}
	h.SetActivityNoter(act)

	rec := httptest.NewRecorder()
	h.ContainerStop(rec, stopRequest(crewID, wsID, "OWNER"))

	if rec.Code == http.StatusOK {
		t.Fatalf("a rejected stop reported success: %s", rec.Body.String())
	}
	if act.forgotten != 0 {
		t.Errorf("forgot a crew whose container may still be running")
	}
}

func TestContainerStop_ScopesToTheCallersWorkspace(t *testing.T) {
	h, _, crewID := startTestHandler(t, nil)
	ipc := &stopIPCRecorder{}
	h.SetSocketPath(startFakeIPC(t, ipc))

	rec := httptest.NewRecorder()
	h.ContainerStop(rec, stopRequest(crewID, "another-workspace", "OWNER"))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if ipc.hits != 0 {
		t.Errorf("stopped a crew belonging to another workspace")
	}
}

// Stopping a crew someone else is working in interrupts them, so it is
// not a read just because it frees memory rather than spending it.
func TestContainerStop_RequiresCreateRole(t *testing.T) {
	h, wsID, crewID := startTestHandler(t, nil)
	ipc := &stopIPCRecorder{}
	h.SetSocketPath(startFakeIPC(t, ipc))

	rec := httptest.NewRecorder()
	h.ContainerStop(rec, stopRequest(crewID, wsID, "VIEWER"))

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for VIEWER", rec.Code)
	}
	if ipc.hits != 0 {
		t.Errorf("VIEWER stopped a container")
	}
}

func TestContainerStop_NoRuntimeConfigured(t *testing.T) {
	h, wsID, crewID := startTestHandler(t, nil)

	rec := httptest.NewRecorder()
	h.ContainerStop(rec, stopRequest(crewID, wsID, "OWNER"))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "container runtime") {
		t.Errorf("503 does not name the cause: %s", rec.Body.String())
	}
}
