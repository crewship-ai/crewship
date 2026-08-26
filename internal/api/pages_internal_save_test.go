package api

// InternalSave (POST /api/v1/internal/pages/save) is the trusted sidecar
// endpoint the save_page MCP tool forwards to (mirrors PipelineHandler's
// /internal/pipelines/save and TestPipelineInternalSave_* below it). Unlike
// pipelines, page creation is gated on policy.ActionPageCreate rather than a
// risk classifier — a strict/guided/trusted crew is held (no page created,
// a blocking ADMIN inbox item instead), full autonomy creates it.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/policy"
)

func pageInternalSaveFixture(t *testing.T) (*PageHandler, string, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "c-page", wsID, "Eng", "eng-pages")
	agentID := seedAgentRow(t, db, "a-page", wsID, crewID, "Lead", "lead-pages", "LEAD")
	h := NewPageHandler(db, nil, newTestLogger())
	h.SetPolicyResolver(policy.NewResolver(db))
	return h, wsID, crewID, agentID
}

func TestPageInternalSave_InvalidJSON(t *testing.T) {
	h, _, _, _ := pageInternalSaveFixture(t)
	req := httptest.NewRequest("POST", "/api/v1/internal/pages/save", bytes.NewBufferString(`{bad`))
	rr := httptest.NewRecorder()
	h.InternalSave(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestPageInternalSave_MissingFields(t *testing.T) {
	h, wsID, _, _ := pageInternalSaveFixture(t)
	// No crew_id, no name, no panels.
	req := httptest.NewRequest("POST", "/api/v1/internal/pages/save",
		bytes.NewBufferString(`{"workspace_id":"`+wsID+`"}`))
	rr := httptest.NewRecorder()
	h.InternalSave(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestPageInternalSave_CrossTenantTokenRejected(t *testing.T) {
	h, wsID, crewID, agentID := pageInternalSaveFixture(t)
	body := `{"workspace_id":"` + wsID + `","crew_id":"` + crewID + `","agent_id":"` + agentID + `",` +
		`"name":"Status","panels":[{"id":"p1","schema":"status.v1","owner":"crew/eng-pages","producer":"agent/lead-pages","sla_seconds":30,"span":4}]}`
	req := httptest.NewRequest("POST", "/api/v1/internal/pages/save", bytes.NewBufferString(body))
	req = req.WithContext(crewBoundCtx1222("some-other-workspace", "some-other-crew"))
	rr := httptest.NewRecorder()
	h.InternalSave(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403; body=%s", rr.Code, rr.Body.String())
	}
	var count int
	if err := h.db.QueryRow("SELECT COUNT(*) FROM pages WHERE workspace_id = ?", wsID).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("pages created = %d, want 0", count)
	}
}

// TestPageInternalSave_HeldAtDefaultAutonomy proves the autonomy gate: a
// crew at the DB default (guided) autonomy_level gets ActionPageCreate ==
// DecisionInboxApprove == held. No page is created, and a blocking inbox
// item is filed instead.
func TestPageInternalSave_HeldAtDefaultAutonomy(t *testing.T) {
	h, wsID, crewID, agentID := pageInternalSaveFixture(t)
	body := `{"workspace_id":"` + wsID + `","crew_id":"` + crewID + `","agent_id":"` + agentID + `",` +
		`"name":"Ops Status","panels":[{"id":"p1","schema":"status.v1","owner":"crew/eng-pages","producer":"agent/lead-pages","sla_seconds":30,"span":4}]}`
	req := httptest.NewRequest("POST", "/api/v1/internal/pages/save", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	h.InternalSave(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403 (held); body=%s", rr.Code, rr.Body.String())
	}
	var count int
	if err := h.db.QueryRow("SELECT COUNT(*) FROM pages WHERE workspace_id = ?", wsID).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("pages created = %d, want 0 (held)", count)
	}
	var inboxCount int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE workspace_id = ? AND blocking = 1`, wsID).Scan(&inboxCount); err != nil {
		t.Fatalf("count inbox: %v", err)
	}
	if inboxCount != 1 {
		t.Errorf("blocking inbox items = %d, want 1", inboxCount)
	}
}

func TestPageInternalSave_HappyPathAtFullAutonomy(t *testing.T) {
	h, wsID, crewID, agentID := pageInternalSaveFixture(t)
	if _, err := h.db.Exec(`UPDATE crews SET autonomy_level = 'full' WHERE id = ?`, crewID); err != nil {
		t.Fatalf("set autonomy: %v", err)
	}

	body := `{"workspace_id":"` + wsID + `","crew_id":"` + crewID + `","agent_id":"` + agentID + `",` +
		`"name":"Ops Status","description":"Agent-authored status board",` +
		`"panels":[{"id":"p1","schema":"status.v1","owner":"crew/eng-pages","producer":"agent/lead-pages","sla_seconds":30,"span":4}]}`
	req := httptest.NewRequest("POST", "/api/v1/internal/pages/save", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	h.InternalSave(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201; body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["slug"] != "ops-status" {
		t.Errorf("slug=%v want ops-status", resp["slug"])
	}
	if resp["owner"] != "crew/eng-pages" {
		t.Errorf("owner=%v want crew/eng-pages", resp["owner"])
	}

	var ownerCrewID string
	var ownerUserID sql.NullString
	if err := h.db.QueryRow(`SELECT owner_crew_id, owner_user_id FROM pages WHERE workspace_id = ? AND slug = 'ops-status'`, wsID).
		Scan(&ownerCrewID, &ownerUserID); err != nil {
		t.Fatalf("query page: %v", err)
	}
	if ownerCrewID != crewID {
		t.Errorf("owner_crew_id=%q want %q", ownerCrewID, crewID)
	}
	if ownerUserID.Valid && ownerUserID.String != "" {
		t.Errorf("owner_user_id=%q want empty (owner is the crew, never a user)", ownerUserID.String)
	}

	var authorAgentID sql.NullString
	if err := h.db.QueryRow(`
		SELECT pv.author_agent_id FROM page_versions pv
		JOIN pages p ON p.id = pv.page_id
		WHERE p.workspace_id = ? AND p.slug = 'ops-status'`, wsID).Scan(&authorAgentID); err != nil {
		t.Fatalf("query page_versions: %v", err)
	}
	if !authorAgentID.Valid || authorAgentID.String != agentID {
		t.Errorf("page_versions.author_agent_id=%v want %q", authorAgentID, agentID)
	}
}

func TestPageInternalSave_InvalidPanelSchemaRejected(t *testing.T) {
	h, wsID, crewID, agentID := pageInternalSaveFixture(t)
	if _, err := h.db.Exec(`UPDATE crews SET autonomy_level = 'full' WHERE id = ?`, crewID); err != nil {
		t.Fatalf("set autonomy: %v", err)
	}
	body := `{"workspace_id":"` + wsID + `","crew_id":"` + crewID + `","agent_id":"` + agentID + `",` +
		`"name":"Bad Page","panels":[{"id":"p1","schema":"not-a-real-schema","sla_seconds":30,"span":4}]}`
	req := httptest.NewRequest("POST", "/api/v1/internal/pages/save", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	h.InternalSave(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400; body=%s", rr.Code, rr.Body.String())
	}
}
