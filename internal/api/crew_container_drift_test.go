package api

// The configured-vs-effective gap on `crewship crew container-status` (#1681).
//
// A crew's container_memory_mb / container_cpus can be edited at any time, and
// both are applied at ContainerCreate and nowhere else. Until the container is
// recreated the crew runs under the OLD limits while `crew get` reports the
// new ones — and no surface said so, which is the half of #1681 that makes the
// other half hard to notice.
//
// This endpoint is the one place that can say it: crewshipd reports what the
// running container actually carries (read off the inspect, never recomputed),
// and this handler holds the crews row. Neither side alone can.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// covCCSSizedCrew seeds a crew with explicit configured limits.
func covCCSSizedCrew(t *testing.T, h *CrewHandler, memoryMB int, cpus float64) (wsID, crewID string) {
	t.Helper()
	wsID, crewID = "ws-drift", "crew-drift"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'W', 'w-drift')`, wsID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO crews (id, workspace_id, name, slug, container_memory_mb, container_cpus)
		VALUES (?, ?, 'Alpha', 'alpha', ?, ?)`, crewID, wsID, memoryMB, cpus); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	return wsID, crewID
}

// covCCSStatus drives the handler against a fake crewshipd that answers with
// ipcBody, and returns the decoded response.
func covCCSStatus(t *testing.T, h *CrewHandler, wsID, crewID string, ipcBody map[string]any) map[string]any {
	t.Helper()
	sock := startFakeIPC(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ipcBody)
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
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body
}

// driftFields returns the `field` of every reported drift entry.
func driftFields(t *testing.T, body map[string]any) []string {
	t.Helper()
	raw, ok := body["config_drift"]
	if !ok {
		return nil
	}
	entries, ok := raw.([]any)
	if !ok {
		t.Fatalf("config_drift is %T, want a list", raw)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		m, ok := e.(map[string]any)
		if !ok {
			t.Fatalf("config_drift entry is %T, want an object", e)
		}
		out = append(out, m["field"].(string))
	}
	return out
}

// The reported case: the row says 8192 MiB, the container was created with
// 4096. Both numbers travel, so the operator is told what is true rather than
// only what was asked for.
func TestContainerStatus_ReportsConfiguredVsEffectiveGap(t *testing.T) {
	h := covCCSNewHandler(t)
	wsID, crewID := covCCSSizedCrew(t, h, 8192, 2)

	body := covCCSStatus(t, h, wsID, crewID, map[string]any{
		"crew_id": crewID, "status": "running",
		"effective_memory_mb": 4096, "effective_cpus": 2.0,
	})

	if got := driftFields(t, body); len(got) != 1 || got[0] != "container_memory_mb" {
		t.Fatalf("config_drift = %v, want exactly [container_memory_mb]; a crew running under a limit "+
			"nobody configured has to be visible somewhere (#1681)", got)
	}
	if body["configured_memory_mb"] != float64(8192) {
		t.Errorf("configured_memory_mb = %v, want 8192", body["configured_memory_mb"])
	}
	if body["effective_memory_mb"] != float64(4096) {
		t.Errorf("effective_memory_mb = %v, want 4096 — the number the container actually runs under",
			body["effective_memory_mb"])
	}
}

// Both limits can drift at once, and reporting one is as misleading as
// reporting neither.
func TestContainerStatus_ReportsBothLimits(t *testing.T) {
	h := covCCSNewHandler(t)
	wsID, crewID := covCCSSizedCrew(t, h, 8192, 4)

	body := covCCSStatus(t, h, wsID, crewID, map[string]any{
		"crew_id": crewID, "status": "running",
		"effective_memory_mb": 4096, "effective_cpus": 2.0,
	})

	got := driftFields(t, body)
	if len(got) != 2 {
		t.Fatalf("config_drift = %v, want both container_memory_mb and container_cpus", got)
	}
}

// The quiet case has to stay quiet: a container created from the current
// configuration reports nothing, or the field becomes noise nobody reads.
func TestContainerStatus_NoDriftWhenLimitsAgree(t *testing.T) {
	h := covCCSNewHandler(t)
	wsID, crewID := covCCSSizedCrew(t, h, 8192, 2)

	body := covCCSStatus(t, h, wsID, crewID, map[string]any{
		"crew_id": crewID, "status": "running",
		"effective_memory_mb": 8192, "effective_cpus": 2.0,
	})

	if got := driftFields(t, body); len(got) != 0 {
		t.Errorf("config_drift = %v for a container that matches its crew's configuration", got)
	}
}

// "The container did not say" and "the container says zero" are different
// answers, and only the second one could be drift. crewshipd omits the fields
// when the provider has no opinion — the Apple provider does not track them —
// and an omission compared against a configured 8192 would manufacture a drift
// report out of silence.
func TestContainerStatus_SilenceIsNotDrift(t *testing.T) {
	h := covCCSNewHandler(t)
	wsID, crewID := covCCSSizedCrew(t, h, 8192, 2)

	body := covCCSStatus(t, h, wsID, crewID, map[string]any{
		"crew_id": crewID, "status": "running",
	})

	if got := driftFields(t, body); len(got) != 0 {
		t.Errorf("config_drift = %v from a status carrying no effective limits at all", got)
	}
	if _, present := body["effective_memory_mb"]; present {
		t.Errorf("effective_memory_mb was invented: %v", body["effective_memory_mb"])
	}
}

// A crew with no container has nothing to compare against, and an "unknown"
// status must not come back carrying a confident verdict about limits.
func TestContainerStatus_NoDriftWhenTheContainerIsUnknown(t *testing.T) {
	h := covCCSNewHandler(t)
	wsID, crewID := covCCSSizedCrew(t, h, 8192, 2)

	body := covCCSStatus(t, h, wsID, crewID, map[string]any{
		"crew_id": crewID, "status": "unknown",
	})

	if got := driftFields(t, body); len(got) != 0 {
		t.Errorf("config_drift = %v for a crew with no container to have limits", got)
	}
}
