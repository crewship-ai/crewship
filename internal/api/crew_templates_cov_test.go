package api

// Additional statement-coverage for crew_templates.go, targeting the
// branches the existing TestCrewTemplate_ListGetDeploy leaves uncovered:
// workspace-scoped templates, DB-error 500 paths (via db.Close fault
// injection), SetJournal nil/real, the credential auto-assign success +
// empty paths, and deploy with an explicit crew_slug.
//
// SKIPPED: there are no marketplace / network branches in this file
// (deployCrewTemplate is purely local DB work), so nothing network-bound
// is exercised here.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

// covCTAgentsJSON builds an agents_json payload with the given agent slugs.
func covCTAgentsJSON(t *testing.T, slugs ...string) string {
	t.Helper()
	agents := make([]map[string]any, 0, len(slugs))
	for _, s := range slugs {
		agents = append(agents, map[string]any{
			"name":          s,
			"slug":          s,
			"role_title":    "Engineer",
			"agent_role":    "AGENT",
			"cli_adapter":   "CLAUDE_CODE",
			"llm_provider":  "ANTHROPIC",
			"llm_model":     "claude",
			"tool_profile":  "CODING",
			"system_prompt": "do work",
		})
	}
	b, err := json.Marshal(agents)
	if err != nil {
		t.Fatalf("marshal agents: %v", err)
	}
	return string(b)
}

// covCTEmitter is a journal.Emitter that records the entry types it sees,
// so tests can assert auto-assign emitted the expected events.
type covCTEmitter struct {
	types []journal.EntryType
}

func (e *covCTEmitter) Emit(_ context.Context, entry journal.Entry) (string, error) {
	e.types = append(e.types, entry.Type)
	return "je-" + string(entry.Type), nil
}

func (e *covCTEmitter) Flush(_ context.Context) error { return nil }

func (e *covCTEmitter) has(t journal.EntryType) bool {
	for _, got := range e.types {
		if got == t {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// List — workspace-scoped template inclusion + DB-error 500
// ---------------------------------------------------------------------------

func TestCovCTListIncludesWorkspaceTemplate(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewCrewTemplateHandler(db, newTestLogger())

	agents := covCTAgentsJSON(t, "alpha", "beta")
	if _, err := db.Exec(`INSERT INTO crew_templates
		(id, name, slug, description, icon, color, category, agents_json, is_builtin, workspace_id)
		VALUES ('ct-ws', 'WS Tmpl', 'ws-tmpl', 'd', 'i', '#fff', 'CUSTOM', ?, 0, ?)`,
		agents, wsID); err != nil {
		t.Fatalf("seed ws template: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/crew-templates", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d, body: %s", rr.Code, rr.Body.String())
	}

	var templates []crewTemplateResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &templates); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var found bool
	for _, tmpl := range templates {
		if tmpl.Slug == "ws-tmpl" {
			found = true
			if tmpl.IsBuiltin {
				t.Errorf("ws-tmpl should not be builtin")
			}
			if len(tmpl.Agents) != 2 {
				t.Errorf("ws-tmpl agents = %d, want 2", len(tmpl.Agents))
			}
		}
	}
	if !found {
		t.Errorf("workspace template not returned in list")
	}
}

func TestCovCTListDBError(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewCrewTemplateHandler(db, newTestLogger())

	db.Close() // fault injection → query fails → 500

	req := httptest.NewRequest("GET", "/api/v1/crew-templates", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("list on closed db = %d, want 500", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Get — workspace-scoped happy path + DB-error 500
// ---------------------------------------------------------------------------

func TestCovCTGetWorkspaceTemplate(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewCrewTemplateHandler(db, newTestLogger())

	agents := covCTAgentsJSON(t, "solo")
	if _, err := db.Exec(`INSERT INTO crew_templates
		(id, name, slug, category, agents_json, is_builtin, workspace_id)
		VALUES ('ct-get', 'Get Tmpl', 'get-tmpl', 'CUSTOM', ?, 0, ?)`,
		agents, wsID); err != nil {
		t.Fatalf("seed: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/crew-templates/get-tmpl", nil)
	req.SetPathValue("slug", "get-tmpl")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("get status = %d, body: %s", rr.Code, rr.Body.String())
	}
	var got crewTemplateResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Slug != "get-tmpl" || len(got.Agents) != 1 {
		t.Errorf("unexpected template: %+v", got)
	}
}

func TestCovCTGetDBError(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewCrewTemplateHandler(db, newTestLogger())

	db.Close() // fault injection → 500 (not 404)

	req := httptest.NewRequest("GET", "/api/v1/crew-templates/anything", nil)
	req.SetPathValue("slug", "anything")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("get on closed db = %d, want 500", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// SetJournal — nil resets to noop, real emitter is retained
// ---------------------------------------------------------------------------

func TestCovCTSetJournal(t *testing.T) {
	db := setupTestDB(t)
	h := NewCrewTemplateHandler(db, newTestLogger())

	em := &covCTEmitter{}
	h.SetJournal(em)
	if _, ok := h.journal.(*covCTEmitter); !ok {
		t.Errorf("SetJournal(real) did not retain emitter: %T", h.journal)
	}

	h.SetJournal(nil)
	if _, ok := h.journal.(noopEmitter); !ok {
		t.Errorf("SetJournal(nil) did not reset to noopEmitter: %T", h.journal)
	}
}

// ---------------------------------------------------------------------------
// Deploy — explicit crew_slug + DB state assertions + auto-assign success
// ---------------------------------------------------------------------------

func TestCovCTDeployWithSlugAndCredentials(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	em := &covCTEmitter{}
	h := NewCrewTemplateHandler(db, newTestLogger())
	h.SetJournal(em)

	// Workspace-scoped template with two agents.
	agents := covCTAgentsJSON(t, "lead", "helper")
	if _, err := db.Exec(`INSERT INTO crew_templates
		(id, name, slug, category, agents_json, is_builtin, workspace_id)
		VALUES ('ct-dep', 'Dep Tmpl', 'dep-tmpl', 'CUSTOM', ?, 0, ?)`,
		agents, wsID); err != nil {
		t.Fatalf("seed template: %v", err)
	}

	// One Anthropic credential so autoAssignCredentials links it.
	if _, err := db.Exec(`INSERT INTO credentials
		(id, workspace_id, name, encrypted_value, type, provider, created_by)
		VALUES ('cred-1', ?, 'ANTHROPIC_API_KEY', 'enc', 'API_KEY', 'ANTHROPIC', ?)`,
		wsID, userID); err != nil {
		t.Fatalf("seed credential: %v", err)
	}

	body := `{"crew_name":"My Crew","crew_slug":"Custom Slug"}`
	req := httptest.NewRequest("POST", "/api/v1/crew-templates/dep-tmpl/deploy",
		bytes.NewBufferString(body))
	req.SetPathValue("slug", "dep-tmpl")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Deploy(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("deploy = %d, body: %s", rr.Code, rr.Body.String())
	}

	var dep deployCrewResult
	if err := json.Unmarshal(rr.Body.Bytes(), &dep); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if dep.CrewSlug != "custom-slug" {
		t.Errorf("crew_slug = %q, want slugified 'custom-slug'", dep.CrewSlug)
	}
	if dep.AgentCount != 2 {
		t.Errorf("agent count = %d, want 2", dep.AgentCount)
	}

	// DB state: crew row exists with the slugified slug.
	var crewCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM crews WHERE id = ? AND slug = 'custom-slug' AND workspace_id = ?`,
		dep.CrewID, wsID).Scan(&crewCount); err != nil {
		t.Fatalf("query crew: %v", err)
	}
	if crewCount != 1 {
		t.Errorf("crew rows = %d, want 1", crewCount)
	}

	// DB state: both agents created with template-derived slugs.
	var agentCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM agents WHERE crew_id = ?`, dep.CrewID).Scan(&agentCount); err != nil {
		t.Fatalf("query agents: %v", err)
	}
	if agentCount != 2 {
		t.Errorf("agent rows = %d, want 2", agentCount)
	}

	// DB state: each agent got the Anthropic credential auto-assigned.
	for _, aid := range dep.AgentIDs {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM agent_credentials WHERE agent_id = ? AND credential_id = 'cred-1'`, aid).Scan(&n); err != nil {
			t.Fatalf("query agent_credentials: %v", err)
		}
		if n != 1 {
			t.Errorf("agent %s credential rows = %d, want 1", aid, n)
		}
	}

	// auto-assign found credentials → no empty event emitted.
	if em.has(journal.EntryCredentialAutoAssignEmpty) {
		t.Errorf("unexpected auto_assign_empty event when credentials present")
	}
}

func TestCovCTDeployEmptyCredentialsEmitsEmptyEvent(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	em := &covCTEmitter{}
	h := NewCrewTemplateHandler(db, newTestLogger())
	h.SetJournal(em)

	agents := covCTAgentsJSON(t, "solo")
	if _, err := db.Exec(`INSERT INTO crew_templates
		(id, name, slug, category, agents_json, is_builtin, workspace_id)
		VALUES ('ct-empty', 'Empty Tmpl', 'empty-tmpl', 'CUSTOM', ?, 0, ?)`,
		agents, wsID); err != nil {
		t.Fatalf("seed template: %v", err)
	}

	req := httptest.NewRequest("POST", "/api/v1/crew-templates/empty-tmpl/deploy",
		bytes.NewBufferString(`{"crew_name":"No Creds Crew"}`))
	req.SetPathValue("slug", "empty-tmpl")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Deploy(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("deploy = %d, body: %s", rr.Code, rr.Body.String())
	}

	// No Anthropic credentials in workspace → empty event emitted.
	if !em.has(journal.EntryCredentialAutoAssignEmpty) {
		t.Errorf("expected auto_assign_empty event when no credentials present; saw %v", em.types)
	}
}

// TestCovCTDeployBlankSlugConflict drives the path where crew_slug is provided
// but slugifies to empty (only punctuation) → 409 conflict.
func TestCovCTDeployBlankSlugConflict(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewCrewTemplateHandler(db, newTestLogger())

	agents := covCTAgentsJSON(t, "solo")
	if _, err := db.Exec(`INSERT INTO crew_templates
		(id, name, slug, category, agents_json, is_builtin, workspace_id)
		VALUES ('ct-blank', 'Blank Tmpl', 'blank-tmpl', 'CUSTOM', ?, 0, ?)`,
		agents, wsID); err != nil {
		t.Fatalf("seed template: %v", err)
	}

	req := httptest.NewRequest("POST", "/api/v1/crew-templates/blank-tmpl/deploy",
		bytes.NewBufferString(`{"crew_name":"Real Name","crew_slug":"!!!"}`))
	req.SetPathValue("slug", "blank-tmpl")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Deploy(rr, req)
	if rr.Code != http.StatusConflict {
		t.Errorf("blank slug deploy = %d, want 409, body: %s", rr.Code, rr.Body.String())
	}
}

// TestCovCTDeployDBError closes the db before deploy so deployCrewTemplate's
// template-load query fails with a non-NotFound error → 500.
func TestCovCTDeployDBError(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewCrewTemplateHandler(db, newTestLogger())

	db.Close()

	req := httptest.NewRequest("POST", "/api/v1/crew-templates/x/deploy",
		bytes.NewBufferString(`{"crew_name":"X"}`))
	req.SetPathValue("slug", "x")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Deploy(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("deploy on closed db = %d, want 500", rr.Code)
	}
}
