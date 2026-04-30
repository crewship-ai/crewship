package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestJournalLookup_EmptyWorkspace ensures the response contains
// non-nil empty arrays so the frontend can `.find(...)` without nil
// guards.
func TestJournalLookup_EmptyWorkspace(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewJournalLookupHandler(db, newTestLogger())

	req := httptest.NewRequest("GET", "/api/v1/journal/lookup", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp journalLookupResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Crews == nil || resp.Agents == nil || resp.Missions == nil {
		t.Errorf("empty response should be non-nil arrays: crews=%v agents=%v missions=%v",
			resp.Crews, resp.Agents, resp.Missions)
	}
	if len(resp.Crews) != 0 || len(resp.Agents) != 0 || len(resp.Missions) != 0 {
		t.Errorf("empty workspace should return empty lists, got: %+v", resp)
	}

	// JSON shape — confirm none of the fields serialise as `null`.
	body := rr.Body.String()
	for _, k := range []string{"\"crews\":null", "\"agents\":null", "\"missions\":null"} {
		if containsString(body, k) {
			t.Errorf("response body must not contain %s; body=%s", k, body)
		}
	}
}

// TestJournalLookup_PopulatedWorkspace seeds a few crews, agents and
// missions and verifies they all return correctly.
func TestJournalLookup_PopulatedWorkspace(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// Two crews with explicit icon + color so we can assert round-trip.
	if _, err := db.Exec(
		`INSERT INTO crews (id, workspace_id, name, slug, icon, color, container_memory_mb, container_cpus)
		 VALUES ('c-eng', ?, 'Engineering', 'engineering', 'code', 'emerald', 4096, 2.0),
		        ('c-qa',  ?, 'Quality',     'quality',     'shield-check', 'amber', 4096, 2.0)`,
		wsID, wsID); err != nil {
		t.Fatalf("seed crews: %v", err)
	}

	// Three agents — two in c-eng, one in c-qa.
	seedAgentRow(t, db, "a-tomas", wsID, "c-eng", "Tomáš", "tomas", "AGENT")
	seedAgentRow(t, db, "a-viktor", wsID, "c-eng", "Viktor", "viktor", "AGENT")
	seedAgentRow(t, db, "a-eva", wsID, "c-qa", "Eva", "eva", "AGENT")
	// Bump avatar_seed on one to verify the field round-trips.
	if _, err := db.Exec(`UPDATE agents SET avatar_seed = 'seed-tomas', avatar_style = 'pixel-art' WHERE id = 'a-tomas'`); err != nil {
		t.Fatalf("update agent: %v", err)
	}

	// Two missions — missions table requires workspace + crew + lead_agent.
	if _, err := db.Exec(
		`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status)
		 VALUES ('m-a', ?, 'c-eng', 'a-tomas', 'tr-a', 'Migrate auth', 'IN_PROGRESS'),
		        ('m-b', ?, 'c-qa',  'a-eva',   'tr-b', 'QA sweep',     'COMPLETED')`,
		wsID, wsID); err != nil {
		t.Fatalf("seed missions: %v", err)
	}

	h := NewJournalLookupHandler(db, newTestLogger())
	req := httptest.NewRequest("GET", "/api/v1/journal/lookup", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp journalLookupResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Crews — alphabetical (Engineering before Quality).
	if len(resp.Crews) != 2 {
		t.Fatalf("crews len = %d want 2", len(resp.Crews))
	}
	if resp.Crews[0].ID != "c-eng" || resp.Crews[0].Name != "Engineering" || resp.Crews[0].Slug != "engineering" {
		t.Errorf("crew 0: %+v", resp.Crews[0])
	}
	if resp.Crews[0].Icon == nil || *resp.Crews[0].Icon != "code" {
		t.Errorf("crew 0 icon: %v", resp.Crews[0].Icon)
	}
	if resp.Crews[0].Color == nil || *resp.Crews[0].Color != "emerald" {
		t.Errorf("crew 0 color: %v", resp.Crews[0].Color)
	}
	if resp.Crews[1].ID != "c-qa" || resp.Crews[1].Slug != "quality" {
		t.Errorf("crew 1: %+v", resp.Crews[1])
	}

	// Agents — three returned, alphabetical by name (Eva, Tomáš, Viktor).
	if len(resp.Agents) != 3 {
		t.Fatalf("agents len = %d want 3", len(resp.Agents))
	}
	// Spot-check: Tomáš must have the avatar_seed/style we set.
	var tomas *lookupAgentEntry
	for i := range resp.Agents {
		if resp.Agents[i].ID == "a-tomas" {
			tomas = &resp.Agents[i]
			break
		}
	}
	if tomas == nil {
		t.Fatalf("tomas not found in %v", resp.Agents)
	}
	if tomas.AvatarSeed == nil || *tomas.AvatarSeed != "seed-tomas" {
		t.Errorf("tomas avatar_seed: %v", tomas.AvatarSeed)
	}
	if tomas.AvatarStyle == nil || *tomas.AvatarStyle != "pixel-art" {
		t.Errorf("tomas avatar_style: %v", tomas.AvatarStyle)
	}
	if tomas.CrewID == nil || *tomas.CrewID != "c-eng" {
		t.Errorf("tomas crew_id: %v", tomas.CrewID)
	}

	// Missions — both returned. Order is created_at DESC; the inserts
	// race within one second so we accept either order, just verify
	// both ids are present and the columns round-trip.
	if len(resp.Missions) != 2 {
		t.Fatalf("missions len = %d want 2", len(resp.Missions))
	}
	seenA, seenB := false, false
	for _, m := range resp.Missions {
		switch m.ID {
		case "m-a":
			if m.Title != "Migrate auth" || m.Status != "IN_PROGRESS" {
				t.Errorf("m-a fields: %+v", m)
			}
			seenA = true
		case "m-b":
			if m.Title != "QA sweep" || m.Status != "COMPLETED" {
				t.Errorf("m-b fields: %+v", m)
			}
			seenB = true
		}
	}
	if !seenA || !seenB {
		t.Errorf("missing missions: a=%v b=%v in %+v", seenA, seenB, resp.Missions)
	}
}

// TestJournalLookup_SoftDeletedHidden ensures rows with deleted_at set
// don't surface in the response.
func TestJournalLookup_SoftDeletedHidden(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "c-live", wsID, "Live", "live")
	seedCrewRow(t, db, "c-gone", wsID, "Gone", "gone")
	seedAgentRow(t, db, "a-live", wsID, "c-live", "Alive", "alive", "AGENT")
	seedAgentRow(t, db, "a-gone", wsID, "c-live", "Gone", "gone-agent", "AGENT")

	if _, err := db.Exec(`UPDATE crews SET deleted_at = datetime('now') WHERE id = 'c-gone'`); err != nil {
		t.Fatalf("soft-delete crew: %v", err)
	}
	if _, err := db.Exec(`UPDATE agents SET deleted_at = datetime('now') WHERE id = 'a-gone'`); err != nil {
		t.Fatalf("soft-delete agent: %v", err)
	}

	h := NewJournalLookupHandler(db, newTestLogger())
	req := httptest.NewRequest("GET", "/api/v1/journal/lookup", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp journalLookupResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Crews) != 1 || resp.Crews[0].ID != "c-live" {
		t.Errorf("crews should hide soft-deleted: %+v", resp.Crews)
	}
	if len(resp.Agents) != 1 || resp.Agents[0].ID != "a-live" {
		t.Errorf("agents should hide soft-deleted: %+v", resp.Agents)
	}
}

// TestJournalLookup_WorkspaceIsolation guarantees that a request scoped
// to one workspace never returns rows from another.
func TestJournalLookup_WorkspaceIsolation(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsA := seedTestWorkspace(t, db, userID)

	// Create a second workspace by hand (helper hardcodes the ID).
	wsB := "ws-other"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other')`, wsB); err != nil {
		t.Fatalf("seed ws b: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('m-other', ?, ?, 'OWNER')`, wsB, userID); err != nil {
		t.Fatalf("seed member: %v", err)
	}

	seedCrewRow(t, db, "c-mine", wsA, "Mine", "mine")
	seedCrewRow(t, db, "c-theirs", wsB, "Theirs", "theirs")
	seedAgentRow(t, db, "a-mine", wsA, "c-mine", "Mine", "mine", "AGENT")
	seedAgentRow(t, db, "a-theirs", wsB, "c-theirs", "Theirs", "theirs", "AGENT")
	if _, err := db.Exec(
		`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status)
		 VALUES ('m-mine', ?, 'c-mine', 'a-mine', 'tr-mine', 'Mine', 'IN_PROGRESS'),
		        ('m-theirs', ?, 'c-theirs', 'a-theirs', 'tr-theirs', 'Theirs', 'IN_PROGRESS')`,
		wsA, wsB); err != nil {
		t.Fatalf("seed missions: %v", err)
	}

	h := NewJournalLookupHandler(db, newTestLogger())

	// Hit workspace A — must see only A's rows.
	req := httptest.NewRequest("GET", "/api/v1/journal/lookup", nil)
	req = withWorkspaceUser(req, userID, wsA, "OWNER")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var resp journalLookupResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	for _, c := range resp.Crews {
		if c.ID == "c-theirs" {
			t.Errorf("workspace leak via crews: %+v", resp.Crews)
		}
	}
	for _, a := range resp.Agents {
		if a.ID == "a-theirs" {
			t.Errorf("workspace leak via agents: %+v", resp.Agents)
		}
	}
	for _, m := range resp.Missions {
		if m.ID == "m-theirs" {
			t.Errorf("workspace leak via missions: %+v", resp.Missions)
		}
	}
}

// TestJournalLookup_RequiresWorkspace returns 401 without ws context.
func TestJournalLookup_RequiresWorkspace(t *testing.T) {
	db := setupTestDB(t)
	h := NewJournalLookupHandler(db, newTestLogger())
	req := httptest.NewRequest("GET", "/api/v1/journal/lookup", nil)
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401; body=%s", rr.Code, rr.Body.String())
	}
}

// containsString — small helper to keep imports light.
func containsString(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// Avoid unused-import on sql when only used in a helper.
var _ = sql.ErrNoRows
