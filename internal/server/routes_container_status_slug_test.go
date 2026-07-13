package server

// Regression test: handleContainerStatus must resolve a crew's slug to its
// container name (via provider.CrewContainerName) before inspecting the
// provider, exactly like handleContainerStop. Passing the raw crew ID to
// ContainerStatus made Docker's inspect miss the container and report
// "unknown" for a crew that was in fact running.

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// covStatusRecordingContainer records the id handed to ContainerStatus so the
// test can assert slug→name resolution happened.
type covStatusRecordingContainer struct {
	mockContainer
	inspectedID string
}

func (c *covStatusRecordingContainer) CrewContainerName(_ string, slug string) string {
	return "crewship-team-" + slug
}

func (c *covStatusRecordingContainer) ContainerStatus(_ context.Context, id string) (*provider.ContainerStatus, error) {
	c.inspectedID = id
	return &provider.ContainerStatus{ID: id, State: "running", Uptime: "1h"}, nil
}

func TestHandleContainerStatus_ResolvesSlugToContainerName(t *testing.T) {
	s := newTestServerWithDeps(t)
	ctr := &covStatusRecordingContainer{}
	s.container = ctr
	mustExec(t, s.db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_st','ST','ws-st')`)
	mustExec(t, s.db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew_st','ws_st','ST','gamma-st')`)

	req := httptest.NewRequest("GET", "/crews/crew_st/container/status", nil)
	w := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	// Provider must be inspected by the resolved container name, not the raw id.
	if ctr.inspectedID != "crewship-team-gamma-st" {
		t.Errorf("inspected id = %q, want crewship-team-gamma-st", ctr.inspectedID)
	}
	resp := parseJSON(t, w.Body.Bytes())
	if resp["status"] != "running" {
		t.Errorf("status = %v, want running", resp["status"])
	}
	// The response crew_id stays the caller-facing crew ID.
	if resp["crew_id"] != "crew_st" {
		t.Errorf("crew_id = %v, want crew_st", resp["crew_id"])
	}
}
