package api

// The version-list endpoint must say whether an empty list is a fact
// or a blind spot.
//
// §4.5 of the 2026-08-13 chat-surface audit: the memory panel renders
// whatever this endpoint returns, and an unprojected tier returns the
// same `{"entries": []}` as a tier nobody has written to yet. The panel
// then draws "(no history)" over a file that may well be full. The
// endpoint therefore carries the distinction; the panel renders it.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/memory"
)

type projectionEnvelope struct {
	Count      int `json:"count"`
	Projection struct {
		State  string `json:"state"`
		Reason string `json:"reason"`
	} `json:"projection"`
}

func listProjection(t *testing.T, h *MemoryVersionsHandler, userID, wsID, path string) projectionEnvelope {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/memory/versions?path="+path, nil)
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var out projectionEnvelope
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

// The two empties that must not look alike.
func TestMemoryVersions_List_EmptyProjectedTier_IsReadable(t *testing.T) {
	h, _, userID, wsID, _ := newMemVerHandlerTest(t)
	// A crew that has never written CREW.md: no rows, but the audit
	// watcher records this path, so the emptiness is a fact.
	got := listProjection(t, h, userID, wsID, "crew:c1/CREW.md")
	if got.Count != 0 {
		t.Fatalf("count = %d, want 0", got.Count)
	}
	if got.Projection.State != string(memory.ProjectionRecorded) {
		t.Errorf("projection.state = %q, want %q — a watched tier's empty list means "+
			"nothing has been written", got.Projection.State, memory.ProjectionRecorded)
	}
}

func TestMemoryVersions_List_UnprojectedTier_IsUnreadableNotEmpty(t *testing.T) {
	h, _, userID, wsID, _ := newMemVerHandlerTest(t)
	// lessons.md is written by the negative-learning evaluator and
	// recorded by nobody. The list is empty and always will be — which
	// is not the same statement as "the agent has learned nothing".
	got := listProjection(t, h, userID, wsID, "agent:martin/lessons.md")
	if got.Count != 0 {
		t.Fatalf("count = %d, want 0", got.Count)
	}
	if got.Projection.State != string(memory.ProjectionUnrecorded) {
		t.Fatalf("projection.state = %q, want %q", got.Projection.State, memory.ProjectionUnrecorded)
	}
	if got.Projection.Reason == "" {
		t.Error("projection.reason is empty — the caller has to be able to tell the reader WHY")
	}
}

// A server with no blob root cannot record any version at all
// (RecordVersion refuses without one), so every path is unreadable
// there — including the ones that are normally projected.
func TestMemoryVersions_List_VersioningDisabled_ReportsUnavailable(t *testing.T) {
	h, _, userID, wsID, _ := newMemVerHandlerTest(t)
	h.SetBlobRoot("")

	got := listProjection(t, h, userID, wsID, "crew:c1/CREW.md")
	if got.Projection.State != string(memory.ProjectionUnavailable) {
		t.Errorf("projection.state = %q, want %q", got.Projection.State, memory.ProjectionUnavailable)
	}
}

// Rows present ⇒ the surface is demonstrably reading something, so the
// projection must not contradict what is on screen.
func TestMemoryVersions_List_WithRows_ReportsRecorded(t *testing.T) {
	h, db, userID, wsID, blobRoot := newMemVerHandlerTest(t)
	seedMemoryVersion(t, db, blobRoot, wsID, "crew:c1/CREW.md", "shared body", "audit-watcher")

	got := listProjection(t, h, userID, wsID, "crew:c1/CREW.md")
	if got.Count != 1 {
		t.Fatalf("count = %d, want 1", got.Count)
	}
	if got.Projection.State != string(memory.ProjectionRecorded) {
		t.Errorf("projection.state = %q, want %q", got.Projection.State, memory.ProjectionRecorded)
	}
}
