package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

// crew_templates_cov2_test.go picks up the branches the first pass
// left: malformed agents_json on deploy and list, write failures via
// RAISE triggers, the auto-assign warn/emit-failure paths, and the
// empty-list fallback. Helpers prefixed covCT2.

type covCT2FailEmitter struct{}

func (covCT2FailEmitter) Emit(_ context.Context, _ journal.Entry) (string, error) {
	return "", errors.New("journal sink unavailable")
}
func (covCT2FailEmitter) Flush(_ context.Context) error { return nil }

func covCT2Fixture(t *testing.T) (*CrewTemplateHandler, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	return NewCrewTemplateHandler(db, newTestLogger()), userID, wsID
}

func covCT2Deploy(h *CrewTemplateHandler, userID, wsID, slug, crewName string) *httptest.ResponseRecorder {
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/crew-templates/"+slug+"/deploy",
			jsonBody(map[string]string{"crew_name": crewName})),
		userID, wsID, "OWNER")
	req.SetPathValue("slug", slug)
	rr := httptest.NewRecorder()
	h.Deploy(rr, req)
	return rr
}

func covCT2SeedTemplate(t *testing.T, h *CrewTemplateHandler, wsID, id, slug, agentsJSON string) {
	t.Helper()
	execOrFatal(t, h.db, `INSERT INTO crew_templates
		(id, name, slug, category, agents_json, is_builtin, workspace_id)
		VALUES (?, ?, ?, 'CUSTOM', ?, 0, ?)`, id, "T "+slug, slug, agentsJSON, wsID)
}

func TestCovCT2_Deploy_MalformedAgentsJSON_500(t *testing.T) {
	h, userID, wsID := covCT2Fixture(t)
	covCT2SeedTemplate(t, h, wsID, "covct2-bad", "covct2-bad", "{this is not json")
	rr := covCT2Deploy(h, userID, wsID, "covct2-bad", "Bad Crew")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovCT2_Deploy_SlugUniquenessCheckDBError_500(t *testing.T) {
	h, userID, wsID := covCT2Fixture(t)
	covCT2SeedTemplate(t, h, wsID, "covct2-t1", "covct2-t1",
		`[{"name":"A","slug":"a","agent_role":"AGENT"}]`)
	// crew_templates lookup still works; the crews uniqueness probe fails.
	execOrFatal(t, h.db, `ALTER TABLE crews RENAME TO crews_broken`)
	rr := covCT2Deploy(h, userID, wsID, "covct2-t1", "Crew One")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovCT2_Deploy_CrewInsertFailure_500(t *testing.T) {
	h, userID, wsID := covCT2Fixture(t)
	covCT2SeedTemplate(t, h, wsID, "covct2-t2", "covct2-t2",
		`[{"name":"A","slug":"a","agent_role":"AGENT"}]`)
	execOrFatal(t, h.db, `CREATE TRIGGER covct2_block_crew BEFORE INSERT ON crews
		BEGIN SELECT RAISE(ABORT, 'covct2 forced'); END`)
	rr := covCT2Deploy(h, userID, wsID, "covct2-t2", "Crew Two")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovCT2_Deploy_AgentInsertFailure_500_TxRolledBack(t *testing.T) {
	h, userID, wsID := covCT2Fixture(t)
	covCT2SeedTemplate(t, h, wsID, "covct2-t3", "covct2-t3",
		`[{"name":"A","slug":"a","agent_role":"AGENT"}]`)
	execOrFatal(t, h.db, `CREATE TRIGGER covct2_block_agent BEFORE INSERT ON agents
		BEGIN SELECT RAISE(ABORT, 'covct2 forced'); END`)
	rr := covCT2Deploy(h, userID, wsID, "covct2-t3", "Crew Three")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
	// The crew INSERT happened inside the same tx — it must be gone.
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM crews WHERE slug = 'crew-three'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("crews rows = %d err=%v, want rollback to 0", n, err)
	}
}

// TestCovCT2_AutoAssign_ListQueryFailure_EmitAlsoFails — both the
// credentials query AND the journal emit fail; the helper must absorb
// both (warn-only contract).
func TestCovCT2_AutoAssign_ListQueryFailure_EmitAlsoFails(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	db.Close()
	// Must not panic or error out.
	autoAssignCredentials(context.Background(), db, newTestLogger(), covCT2FailEmitter{},
		wsID, "covct2-agent", "2026-01-01T00:00:00Z")
	_ = userID
}

// TestCovCT2_AutoAssign_InsertFailure_EmitsPerRowFailure — credentials
// exist but the agent_credentials INSERT is blocked; a per-row
// credential.auto_assign_failed entry lands in the journal.
func TestCovCT2_AutoAssign_InsertFailure_EmitsPerRowFailure(t *testing.T) {
	ensureEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedAgentRow(t, db, "covct2-ag", wsID, "", "A", "covct2-ag", "AGENT")
	execOrFatal(t, db, `
		INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by, created_at, updated_at)
		VALUES ('covct2-cred', ?, 'ANTHROPIC_API_KEY', 'enc', 'API_KEY', 'ANTHROPIC', 'WORKSPACE', 'ACTIVE', ?, datetime('now'), datetime('now'))`,
		wsID, userID)
	execOrFatal(t, db, `CREATE TRIGGER covct2_block_ac BEFORE INSERT ON agent_credentials
		BEGIN SELECT RAISE(ABORT, 'covct2 forced'); END`)

	w := journal.NewWriter(db, newTestLogger(), journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = w.Close() })
	autoAssignCredentials(context.Background(), db, newTestLogger(), w,
		wsID, "covct2-ag", "2026-01-01T00:00:00Z")
	_ = w.Flush(context.Background())

	var summary string
	if err := db.QueryRow(`SELECT summary FROM journal_entries
		WHERE workspace_id = ? AND agent_id = 'covct2-ag' AND entry_type = ?`,
		wsID, string(journal.EntryCredentialAutoAssignFailed)).Scan(&summary); err != nil {
		t.Fatalf("auto_assign_failed entry missing: %v", err)
	}
	if !strings.Contains(summary, "insert") {
		t.Errorf("summary = %q, want insert failure reason", summary)
	}
}

// TestCovCT2_AutoAssign_EmptyWorkspace_EmitFails — no credentials AND
// a broken journal: the empty-event emit error is only logged.
func TestCovCT2_AutoAssign_EmptyWorkspace_EmitFails(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	_ = userID
	autoAssignCredentials(context.Background(), db, newTestLogger(), covCT2FailEmitter{},
		wsID, "covct2-ag2", "2026-01-01T00:00:00Z")
}

// TestCovCT2_List_MalformedAgentsJSON_DegradesToEmptyAgents — a stored
// template with broken agents_json must not poison the whole list.
func TestCovCT2_List_MalformedAgentsJSON_DegradesToEmptyAgents(t *testing.T) {
	h, userID, wsID := covCT2Fixture(t)
	covCT2SeedTemplate(t, h, wsID, "covct2-badjson", "covct2-badjson", "{broken")
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/crew-templates", nil),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"slug":"covct2-badjson"`) {
		t.Errorf("body missing the malformed template row: %s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"agents":[]`) {
		t.Errorf("body = %s, want degraded empty agents array", rr.Body.String())
	}
}

// TestCovCT2_List_EmptyCatalogue_ReturnsEmptyArray — when seeding is
// blocked and the table is empty, List answers [] not null.
func TestCovCT2_List_EmptyCatalogue_ReturnsEmptyArray(t *testing.T) {
	h, userID, wsID := covCT2Fixture(t)
	execOrFatal(t, h.db, `DELETE FROM crew_templates`)
	execOrFatal(t, h.db, `CREATE TRIGGER covct2_block_seed BEFORE INSERT ON crew_templates
		BEGIN SELECT RAISE(ABORT, 'covct2 forced'); END`)
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/crew-templates", nil),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	if strings.TrimSpace(rr.Body.String()) != "[]" {
		t.Errorf("body = %q, want []", rr.Body.String())
	}
}

func TestCovCT2_Get_DBError_500(t *testing.T) {
	h, userID, wsID := covCT2Fixture(t)
	h.db.Close()
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/crew-templates/x", nil),
		userID, wsID, "OWNER")
	req.SetPathValue("slug", "x")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovCT2_DeployCrewTemplate_LoadTemplateDBError(t *testing.T) {
	h, _, wsID := covCT2Fixture(t)
	h.db.Close()
	if _, err := deployCrewTemplate(context.Background(), h.db, newTestLogger(), noopEmitter{},
		wsID, "anything", "Crew", ""); err == nil ||
		!strings.Contains(err.Error(), "load template") {
		t.Fatalf("err = %v, want load template failure", err)
	}
}
