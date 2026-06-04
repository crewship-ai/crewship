package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// ---------------------------------------------------------------------------
// pipelines_crud.go — deep coverage for the branches the sibling suites
// (pipelines_crud_export_versions_test.go, pipelines_crud_rollback_import_test.go)
// leave uncovered:
//
//   - role/authorization gates (MEMBER → 403 on Rollback + ImportPipeline)
//   - 500 fault injection via db.Close() across every read/write handler
//   - ImportPipeline happy path: seeds a crew + agent_lead, posts a valid
//     bundle, asserts the pipelines row actually landed in the DB.
//   - ImportPipeline slug-conflict → 409.
//
// We deliberately SKIP the orchestrator/exec branches (RunAgent, executor
// wiring) — those handlers live in pipelines.go / exec paths, not here,
// and need a runner stand-up.
// ---------------------------------------------------------------------------

// covPCDValidBundle is the JSON bundle envelope wrapping the task's
// canonical valid DSL. agentSlug pins the single agent_run step; the
// caller must seed a crew that owns an agent with that slug for import
// validation to pass.
func covPCDValidBundle(name, slug, agentSlug, crewID string) string {
	def := `{"name":"my-pipe","steps":[{"id":"a","type":"agent_run","agent_slug":"` +
		agentSlug + `","prompt":"hi"}]}`
	b, _ := json.Marshal(map[string]any{
		"format": "crewship-pipeline-bundle/v1",
		"pipeline": map[string]any{
			"name":       name,
			"slug":       slug,
			"definition": json.RawMessage(def),
		},
		"author_crew_id": crewID,
	})
	return string(b)
}

// covPCDSeedCrewWithLead seeds a crew plus an agent with slug
// "agent_lead" so ImportPipeline's pipeline.Validate cross-reference
// passes. Returns the crew ID.
func covPCDSeedCrewWithLead(t *testing.T, h *PipelineHandler, wsID string) string {
	t.Helper()
	crewID := seedCrewRow(t, h.db, "crew-pcd", wsID, "PCD Crew", "pcd-crew")
	seedAgentRow(t, h.db, "agent-pcd-lead", wsID, crewID, "Lead", "agent_lead", "LEAD")
	return crewID
}

// ---- Rollback: authorization ----

func TestCovPCDRollback_MemberForbidden_403(t *testing.T) {
	// canRole(role, "manage") only clears OWNER/ADMIN; MEMBER must 403
	// before any store work happens.
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	req := httptest.NewRequest("POST", "/x", strings.NewReader(`{"version":1}`))
	req.SetPathValue("slug", "any")
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.Rollback(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (MEMBER cannot rollback)", rr.Code)
	}
}

func TestCovPCDRollback_ManagerForbidden_403(t *testing.T) {
	// MANAGER clears "create" but NOT "manage" — rollback is manage-tier.
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	req := httptest.NewRequest("POST", "/x", strings.NewReader(`{"version":1}`))
	req.SetPathValue("slug", "any")
	req = withWorkspaceUser(req, userID, wsID, "MANAGER")
	rr := httptest.NewRecorder()
	h.Rollback(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (MANAGER cannot rollback)", rr.Code)
	}
}

func TestCovPCDRollback_DBClosed_500(t *testing.T) {
	// db.Close() makes store.GetBySlug error with something other than
	// ErrNotFound → the "load pipeline" 500 branch.
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	_ = h.db.Close()
	req := httptest.NewRequest("POST", "/x", strings.NewReader(`{"version":1}`))
	req.SetPathValue("slug", "any")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Rollback(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (db closed)", rr.Code)
	}
}

// ---- ImportPipeline: authorization ----

func TestCovPCDImportPipeline_MemberForbidden_403(t *testing.T) {
	// Import is create-tier (canRole(role,"create")). MEMBER → 403 even
	// with a perfectly valid bundle.
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	crewID := covPCDSeedCrewWithLead(t, h, wsID)
	body := covPCDValidBundle("Imported", "imported", "agent_lead", crewID)
	req := httptest.NewRequest("POST", "/x", strings.NewReader(body))
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.ImportPipeline(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (MEMBER cannot import)", rr.Code)
	}
}

// ---- ImportPipeline: happy path + DB state ----

func TestCovPCDImportPipeline_HappyPath_PersistsRow(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	crewID := covPCDSeedCrewWithLead(t, h, wsID)
	body := covPCDValidBundle("My Pipe", "my-pipe", "agent_lead", crewID)

	req := httptest.NewRequest("POST", "/x", strings.NewReader(body))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ImportPipeline(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s, want 201", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rr.Body.String())
	}
	if resp["slug"] != "my-pipe" {
		t.Errorf("response slug = %v, want my-pipe", resp["slug"])
	}
	if resp["name"] != "My Pipe" {
		t.Errorf("response name = %v, want \"My Pipe\"", resp["name"])
	}

	// DB-state assertion: the pipelines row must exist in the receiving
	// workspace, with authored_via = imported and the bundle's crew as author.
	var (
		gotSlug, gotWS, gotVia, gotCrew string
	)
	row := h.db.QueryRow(
		`SELECT slug, workspace_id, authored_via, author_crew_id
		   FROM pipelines WHERE workspace_id = ? AND slug = 'my-pipe'`, wsID)
	if err := row.Scan(&gotSlug, &gotWS, &gotVia, &gotCrew); err != nil {
		t.Fatalf("readback imported pipeline: %v", err)
	}
	if gotWS != wsID {
		t.Errorf("workspace_id = %q, want %q", gotWS, wsID)
	}
	if gotVia != string(pipeline.AuthoredViaImported) {
		t.Errorf("authored_via = %q, want %q", gotVia, pipeline.AuthoredViaImported)
	}
	if gotCrew != crewID {
		t.Errorf("author_crew_id = %q, want %q", gotCrew, crewID)
	}
}

// NOTE: the ImportPipeline → 409 (ErrSlugConflict) branch is intentionally
// NOT covered here: pipeline.Store.Save is upsert-by-slug, so re-importing
// the same slug takes the UPDATE path (returns 201, appends a version) and
// never reaches ErrSlugConflict. That error only surfaces from a raw UNIQUE
// constraint race during INSERT, which isn't reproducible through this
// handler with the in-memory single-connection test DB.

// ---- 500 fault injection on the read-side handlers ----

func TestCovPCDExportPipeline_DBClosed_500(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	_ = h.db.Close()
	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("slug", "any")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ExportPipeline(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (db closed)", rr.Code)
	}
}

func TestCovPCDListVersions_DBClosed_500(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	_ = h.db.Close()
	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("slug", "any")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ListVersions(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (db closed)", rr.Code)
	}
}

func TestCovPCDGetVersion_DBClosed_500(t *testing.T) {
	// Valid integer version param so we get past the 400 gate and reach
	// the store.GetBySlug → 500 branch.
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	_ = h.db.Close()
	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("slug", "any")
	req.SetPathValue("n", "1")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.GetVersion(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (db closed)", rr.Code)
	}
}

func TestCovPCDImportPipeline_DBClosed_500(t *testing.T) {
	// Seed crew/agent BEFORE closing so the bundle passes parse +
	// validation (lookupAgentSlugs failures are non-fatal / best-effort),
	// then the store.Save call fails on the closed DB → 500.
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	crewID := covPCDSeedCrewWithLead(t, h, wsID)
	body := covPCDValidBundle("Closed", "closed-slug", "agent_lead", crewID)
	_ = h.db.Close()
	req := httptest.NewRequest("POST", "/x", strings.NewReader(body))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ImportPipeline(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (db closed on save)", rr.Code)
	}
}
