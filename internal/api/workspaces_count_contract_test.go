package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// Contract test for the workspace usage counts (#866.1).
//
// The settings General tab reads a nested `_count` object
// (settings-layout.tsx: org._count.{crews,agents,members}); the backend
// historically only serialized flat `_count_crews/_count_agents/
// _count_members` with omitempty, so the UI always showed 0. This pins
// BOTH shapes: the nested object (canonical) and the flat keys (kept one
// release for back-compat) so a future refactor can't silently reopen
// the mismatch.

func TestWorkspaceGet_UsageCounts_NestedAndFlat(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewWorkspaceHandler(db, logger)

	// 2 crews, 3 agents. Members = 1 (the seeded OWNER).
	seedCrew(t, db, "c1", wsID, "Crew 1", "c1")
	seedCrew(t, db, "c2", wsID, "Crew 2", "c2")
	seedAgent(t, db, "a1", wsID, "c1", "Agent 1", "a1")
	seedAgent(t, db, "a2", wsID, "c1", "Agent 2", "a2")
	seedAgent(t, db, "a3", wsID, "c2", "Agent 3", "a3")

	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/workspaces/"+wsID, nil),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	// Nested shape — this is what the frontend actually reads.
	var nested struct {
		Count *struct {
			Crews   int `json:"crews"`
			Agents  int `json:"agents"`
			Members int `json:"members"`
		} `json:"_count"`
		FlatCrews   int `json:"_count_crews"`
		FlatAgents  int `json:"_count_agents"`
		FlatMembers int `json:"_count_members"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &nested); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rr.Body.String())
	}
	if nested.Count == nil {
		t.Fatalf("nested _count object missing; body=%s", rr.Body.String())
	}
	if nested.Count.Crews != 2 || nested.Count.Agents != 3 || nested.Count.Members != 1 {
		t.Fatalf("nested _count = %+v, want {crews:2 agents:3 members:1}", *nested.Count)
	}
	// Flat keys retained for back-compat (one release).
	if nested.FlatCrews != 2 || nested.FlatAgents != 3 || nested.FlatMembers != 1 {
		t.Fatalf("flat counts = crews:%d agents:%d members:%d, want 2/3/1",
			nested.FlatCrews, nested.FlatAgents, nested.FlatMembers)
	}
}

// Zero-count workspaces must still emit a nested _count object (with
// zeros) so the FE renders "0" rather than choking on an absent field.
func TestWorkspaceGet_UsageCounts_ZeroStillNested(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewWorkspaceHandler(db, logger)

	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/workspaces/"+wsID, nil),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	raw, ok := body["_count"]
	if !ok {
		t.Fatalf("_count key absent on zero-count workspace; body=%s", rr.Body.String())
	}
	var c struct{ Crews, Agents, Members int }
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("unmarshal _count: %v", err)
	}
	if c.Crews != 0 || c.Agents != 0 || c.Members != 1 {
		t.Fatalf("_count = %+v, want {0,0,1}", c)
	}
}
