package api

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// pipelines_crud.go coverage — Save (workspace-scoped, JWT identity), List
// filters/ordering, Get, and Delete. The sibling files cover Export/Versions/
// GetVersion/Rollback/ImportPipeline and InternalSave; this file targets the
// remaining Save branches + List variants against the fully-migrated DB.
//
// Skipped (no DB/network coverage value here):
//   - Save's internal-500 paths (store wired to a real DB; not forced).
//   - List/Get/Delete store-error 500 branches (would require a broken DB).
// ---------------------------------------------------------------------------

// covPCHandler builds a PipelineHandler against a migrated in-memory DB and
// seeds a crew ("crew_lead") with one LEAD agent whose slug is "agent_lead"
// so happy-path Save bodies validate. Returns handler, userID, wsID, crewID.
func covPCHandler(t *testing.T) (*PipelineHandler, string, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "covpc_crew", wsID, "Lead Crew", "lead-crew")
	seedAgentRow(t, db, "covpc_agent", wsID, crewID, "Lead", "agent_lead", "LEAD")
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return NewPipelineHandler(db, logger, nil, nil), userID, wsID, crewID
}

// covPCValidDef is a DSL definition that parses + validates against the
// seeded "agent_lead" slug.
const covPCValidDef = `{"name":"%SLUG%","steps":[{"id":"a","type":"agent_run","agent_slug":"agent_lead","prompt":"hi"}]}`

func covPCDef(slug string) string {
	return strings.ReplaceAll(covPCValidDef, "%SLUG%", slug)
}

// covPCSaveBody assembles a userSaveRequest JSON body. A fresh passing
// test-run timestamp clears the store gate on the default (legacy) path.
func covPCSaveBody(slug, crewID string, extra map[string]any) string {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	body := map[string]any{
		"slug":                 slug,
		"name":                 slug + " name",
		"description":          "desc",
		"definition":           json.RawMessage(covPCDef(slug)),
		"last_test_run_passed": true,
		"last_test_run_at":     now,
	}
	if crewID != "" {
		body["author_crew_id"] = crewID
	}
	for k, v := range extra {
		body[k] = v
	}
	b, _ := json.Marshal(body)
	return string(b)
}

// covPCInsertPipeline directly inserts a fully-formed pipeline row so List
// filter/order branches have data without round-tripping through Save.
func covPCInsertPipeline(t *testing.T, db *sql.DB, wsID, id, slug, authorCrew string, ephemeral, visible, invocations int) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	def := covPCDef(slug)
	_, err := db.Exec(`
		INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash,
			ephemeral, workspace_visible, invocation_count, author_crew_id, authored_via,
			last_test_run_at, last_test_run_passed, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'user_api', ?, 1, ?, ?)`,
		id, wsID, slug, slug, def, "hash_"+id, ephemeral, visible, invocations, authorCrew, now, now, now)
	if err != nil {
		t.Fatalf("insert pipeline %s: %v", id, err)
	}
}

// ---- Save: auth / role gates ----

func TestCovPCSave_NoUser_401(t *testing.T) {
	h, _, wsID, _ := covPCHandler(t)
	req := httptest.NewRequest("POST", "/x", strings.NewReader(covPCSaveBody("p", "", nil)))
	// Inject workspace + role but NO user.
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Save(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestCovPCSave_MemberForbidden_403(t *testing.T) {
	h, userID, wsID, crewID := covPCHandler(t)
	for _, role := range []string{"VIEWER", "MEMBER"} {
		req := httptest.NewRequest("POST", "/x", strings.NewReader(covPCSaveBody("p", crewID, nil)))
		req = withWorkspaceUser(req, userID, wsID, role)
		rr := httptest.NewRecorder()
		h.Save(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Errorf("role %q: status = %d, want 403", role, rr.Code)
		}
	}
}

// ---- Save: request-body validation ----

func TestCovPCSave_BadJSON_400(t *testing.T) {
	h, userID, wsID, _ := covPCHandler(t)
	req := httptest.NewRequest("POST", "/x", strings.NewReader("not-json"))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Save(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestCovPCSave_MissingSlugOrDefinition_400(t *testing.T) {
	h, userID, wsID, _ := covPCHandler(t)
	cases := map[string]string{
		"no-slug":       `{"name":"x","definition":{"name":"x","steps":[]}}`,
		"no-definition": `{"slug":"x","name":"x"}`,
		"both-missing":  `{"name":"x"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/x", strings.NewReader(body))
			req = withWorkspaceUser(req, userID, wsID, "OWNER")
			rr := httptest.NewRecorder()
			h.Save(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Errorf("%s: status = %d, want 400", name, rr.Code)
			}
		})
	}
}

func TestCovPCSave_SkipTestGate_ManagerForbidden_403(t *testing.T) {
	h, userID, wsID, crewID := covPCHandler(t)
	body := covPCSaveBody("skip-mgr", crewID, map[string]any{"skip_test_gate": true})
	req := httptest.NewRequest("POST", "/x", strings.NewReader(body))
	req = withWorkspaceUser(req, userID, wsID, "MANAGER")
	rr := httptest.NewRecorder()
	h.Save(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "OWNER or ADMIN role") {
		t.Errorf("body = %s, want role-required message", rr.Body.String())
	}
}

// ---- Save: DSL parse / validate / cycle ----

func TestCovPCSave_BadDSLParse_422(t *testing.T) {
	h, userID, wsID, _ := covPCHandler(t)
	// "name" with spaces fails the parser's slug-shape check.
	body := `{"slug":"bad","name":"bad","definition":{"name":"BAD NAME","steps":[]}}`
	req := httptest.NewRequest("POST", "/x", strings.NewReader(body))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Save(rr, req)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", rr.Code)
	}
}

func TestCovPCSave_AgentSlugValidationFailure_422(t *testing.T) {
	// author_crew_id pins the seeded crew (only agent_lead), but the DSL
	// references "ghost_agent" — Validate must reject it as 422.
	h, userID, wsID, crewID := covPCHandler(t)
	def := `{"name":"vfail","steps":[{"id":"a","type":"agent_run","agent_slug":"ghost_agent","prompt":"hi"}]}`
	now := time.Now().UTC().Format(time.RFC3339Nano)
	body := `{"slug":"vfail","name":"vfail","definition":` + def +
		`,"author_crew_id":"` + crewID + `","last_test_run_passed":true,"last_test_run_at":"` + now + `"}`
	req := httptest.NewRequest("POST", "/x", strings.NewReader(body))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Save(rr, req)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422 (unknown agent slug)", rr.Code)
	}
}

// ---- Save: test-gate behaviour ----

func TestCovPCSave_StaleTestRun_GateFailed_422(t *testing.T) {
	// last_test_run_at older than the 5-minute freshness window: the store
	// gate rejects with ErrTestRunGateFailed → 422.
	h, userID, wsID, crewID := covPCHandler(t)
	stale := time.Now().UTC().Add(-30 * time.Minute).Format(time.RFC3339Nano)
	body := `{"slug":"stale","name":"stale","definition":` + covPCDef("stale") +
		`,"author_crew_id":"` + crewID + `","last_test_run_passed":true,"last_test_run_at":"` + stale + `"}`
	req := httptest.NewRequest("POST", "/x", strings.NewReader(body))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Save(rr, req)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "fresh") {
		t.Errorf("body = %s, want test_run gate message", rr.Body.String())
	}
}

func TestCovPCSave_NoTestRunFields_GateFailed_422(t *testing.T) {
	// No last_test_run_* and no skip/token: LastTestRunPassed stays false →
	// store gate fails immediately.
	h, userID, wsID, crewID := covPCHandler(t)
	body := `{"slug":"notest","name":"notest","definition":` + covPCDef("notest") +
		`,"author_crew_id":"` + crewID + `"}`
	req := httptest.NewRequest("POST", "/x", strings.NewReader(body))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Save(rr, req)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422 (no fresh test run)", rr.Code)
	}
}

// ---- Save: happy paths ----

func TestCovPCSave_HappyPath_OwnerWithCrew(t *testing.T) {
	h, userID, wsID, crewID := covPCHandler(t)
	req := httptest.NewRequest("POST", "/x", strings.NewReader(covPCSaveBody("owner-saved", crewID, nil)))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Save(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var out pipelineResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Slug != "owner-saved" {
		t.Errorf("slug = %q", out.Slug)
	}
	if out.AuthorUserID != userID {
		t.Errorf("author_user_id = %q, want %q", out.AuthorUserID, userID)
	}
	if out.AuthoredVia != "user_api" {
		t.Errorf("authored_via = %q, want user_api", out.AuthoredVia)
	}
	// Verify the DB row landed.
	var got string
	if err := h.db.QueryRow(`SELECT slug FROM pipelines WHERE workspace_id=? AND slug='owner-saved'`, wsID).Scan(&got); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if got != "owner-saved" {
		t.Errorf("db slug = %q", got)
	}
}

func TestCovPCSave_HappyPath_NoAuthorCrew_SkipsAgentValidation(t *testing.T) {
	// author_crew_id absent → agent-slug validation skipped. A DSL with an
	// arbitrary agent slug still saves (runtime resolution catches mismatch).
	h, userID, wsID, _ := covPCHandler(t)
	def := `{"name":"freeform","steps":[{"id":"a","type":"agent_run","agent_slug":"whoever","prompt":"hi"}]}`
	now := time.Now().UTC().Format(time.RFC3339Nano)
	body := `{"slug":"freeform","name":"freeform","definition":` + def +
		`,"last_test_run_passed":true,"last_test_run_at":"` + now + `"}`
	req := httptest.NewRequest("POST", "/x", strings.NewReader(body))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Save(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovPCSave_SkipTestGate_AdminAllowed(t *testing.T) {
	h, userID, wsID, crewID := covPCHandler(t)
	// skip_test_gate clears the gate without any last_test_run_* fields.
	body := `{"slug":"admin-skip","name":"x","definition":` + covPCDef("admin-skip") +
		`,"author_crew_id":"` + crewID + `","skip_test_gate":true}`
	req := httptest.NewRequest("POST", "/x", strings.NewReader(body))
	req = withWorkspaceUser(req, userID, wsID, "ADMIN")
	rr := httptest.NewRecorder()
	h.Save(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("ADMIN skip_test_gate: status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovPCSave_ExistingSlug_UpsertsInPlace(t *testing.T) {
	// Public Save is an upsert: re-saving an existing (non-deleted) slug
	// takes the UPDATE path and returns 201, not a 409. (ErrSlugConflict
	// only fires on a genuine UNIQUE race the pre-check missed, which the
	// public handler can't deterministically trigger here.)
	h, userID, wsID, crewID := covPCHandler(t)
	covPCInsertPipeline(t, h.db, wsID, "dup_pln", "dup", crewID, 0, 1, 0)
	req := httptest.NewRequest("POST", "/x", strings.NewReader(covPCSaveBody("dup", crewID, nil)))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Save(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s, want 201 (upsert)", rr.Code, rr.Body.String())
	}
	// Authorship flips to the user_api path on the update.
	var via string
	if err := h.db.QueryRow(`SELECT authored_via FROM pipelines WHERE id='dup_pln'`).Scan(&via); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if via != "user_api" {
		t.Errorf("authored_via = %q, want user_api after upsert", via)
	}
}

// ---- Save: save_token branch ----

func TestCovPCSave_SaveToken_Valid_ClearsGate(t *testing.T) {
	h, userID, wsID, crewID := covPCHandler(t)
	secret := []byte("test-secret-32-bytes-long-padxxx")
	h.SetSaveTokenSecret(secret)

	def := covPCDef("tokensave")
	defHash := definitionHashHex([]byte(def))
	token := signSaveToken(secret, wsID, defHash, userID, time.Now())

	// No last_test_run_* fields — the valid token alone clears the gate.
	body := `{"slug":"tokensave","name":"tokensave","definition":` + def +
		`,"author_crew_id":"` + crewID + `","save_token":"` + token + `"}`
	req := httptest.NewRequest("POST", "/x", strings.NewReader(body))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Save(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovPCSave_SaveToken_Invalid_422(t *testing.T) {
	h, userID, wsID, crewID := covPCHandler(t)
	h.SetSaveTokenSecret([]byte("test-secret-32-bytes-long-padxxx"))

	body := `{"slug":"badtoken","name":"badtoken","definition":` + covPCDef("badtoken") +
		`,"author_crew_id":"` + crewID + `","save_token":"123.deadbeef"}`
	req := httptest.NewRequest("POST", "/x", strings.NewReader(body))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Save(rr, req)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d body=%s, want 422", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "save_token invalid") {
		t.Errorf("body = %s, want save_token-invalid message", rr.Body.String())
	}
}

// ---- List: ordering + filters ----

func TestCovPCList_HappyPath_DefaultPopularity(t *testing.T) {
	h, userID, wsID, crewID := covPCHandler(t)
	covPCInsertPipeline(t, h.db, wsID, "p_low", "low", crewID, 0, 1, 1)
	covPCInsertPipeline(t, h.db, wsID, "p_high", "high", crewID, 0, 1, 99)

	req := httptest.NewRequest("GET", "/x", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var out []pipelineResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("count = %d, want 2", len(out))
	}
	// Popularity DESC: high (99) before low (1).
	if out[0].Slug != "high" {
		t.Errorf("first = %q, want high (popularity DESC)", out[0].Slug)
	}
	// List omits the definition body.
	if out[0].Definition != nil {
		t.Errorf("list should not include definition")
	}
}

func TestCovPCList_OrderVariants(t *testing.T) {
	h, userID, wsID, crewID := covPCHandler(t)
	covPCInsertPipeline(t, h.db, wsID, "p_a", "aaa", crewID, 0, 1, 5)
	covPCInsertPipeline(t, h.db, wsID, "p_b", "bbb", crewID, 0, 1, 50)

	for _, order := range []string{"recent", "name", "popularity", "unknown"} {
		req := httptest.NewRequest("GET", "/x?order="+order, nil)
		req = withWorkspaceUser(req, userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.List(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("order=%s: status = %d", order, rr.Code)
		}
		var out []pipelineResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("order=%s decode: %v", order, err)
		}
		if len(out) != 2 {
			t.Errorf("order=%s: count = %d, want 2", order, len(out))
		}
	}
}

func TestCovPCList_ExcludesEphemeralAndHiddenByDefault(t *testing.T) {
	h, userID, wsID, crewID := covPCHandler(t)
	covPCInsertPipeline(t, h.db, wsID, "p_vis", "visible", crewID, 0, 1, 1)
	covPCInsertPipeline(t, h.db, wsID, "p_eph", "ephemeral", crewID, 1, 1, 1)
	covPCInsertPipeline(t, h.db, wsID, "p_hid", "hidden", crewID, 0, 0, 1)

	// Default: only the visible non-ephemeral one.
	req := httptest.NewRequest("GET", "/x", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	var out []pipelineResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != 1 || out[0].Slug != "visible" {
		t.Errorf("default list = %+v, want only [visible]", slugsOf(out))
	}

	// include_ephemeral=1 + include_hidden=1 surfaces all three.
	req2 := httptest.NewRequest("GET", "/x?include_ephemeral=1&include_hidden=1", nil)
	req2 = withWorkspaceUser(req2, userID, wsID, "OWNER")
	rr2 := httptest.NewRecorder()
	h.List(rr2, req2)
	var out2 []pipelineResponse
	if err := json.Unmarshal(rr2.Body.Bytes(), &out2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out2) != 3 {
		t.Errorf("include-all list count = %d, want 3 (%+v)", len(out2), slugsOf(out2))
	}
}

func TestCovPCList_FilterByAuthorCrew(t *testing.T) {
	h, userID, wsID, crewID := covPCHandler(t)
	other := seedCrewRow(t, h.db, "covpc_crew2", wsID, "Other", "other-crew")
	covPCInsertPipeline(t, h.db, wsID, "p_mine", "mine", crewID, 0, 1, 1)
	covPCInsertPipeline(t, h.db, wsID, "p_theirs", "theirs", other, 0, 1, 1)

	req := httptest.NewRequest("GET", "/x?author_crew_id="+crewID, nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	var out []pipelineResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != 1 || out[0].Slug != "mine" {
		t.Errorf("author-crew filter = %+v, want only [mine]", slugsOf(out))
	}
}

func TestCovPCList_EmptyReturnsEmptyArray(t *testing.T) {
	h, userID, wsID, _ := covPCHandler(t)
	req := httptest.NewRequest("GET", "/x", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if strings.TrimSpace(rr.Body.String()) != "[]" {
		t.Errorf("empty list body = %q, want []", rr.Body.String())
	}
}

func slugsOf(rows []pipelineResponse) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Slug)
	}
	return out
}

// ---- Get ----

func TestCovPCGet_HappyPath_IncludesDefinition(t *testing.T) {
	h, userID, wsID, crewID := covPCHandler(t)
	covPCInsertPipeline(t, h.db, wsID, "g_pln", "getme", crewID, 0, 1, 0)
	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("slug", "getme")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var out pipelineResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Slug != "getme" {
		t.Errorf("slug = %q", out.Slug)
	}
	if len(out.Definition) == 0 {
		t.Error("Get must include definition")
	}
}

func TestCovPCGet_NotFound_404(t *testing.T) {
	h, userID, wsID, _ := covPCHandler(t)
	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("slug", "ghost")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// ---- Delete ----

func TestCovPCDelete_MemberForbidden_403(t *testing.T) {
	h, userID, wsID, crewID := covPCHandler(t)
	covPCInsertPipeline(t, h.db, wsID, "d_pln", "doomed", crewID, 0, 1, 0)
	req := httptest.NewRequest("DELETE", "/x", nil)
	req.SetPathValue("slug", "doomed")
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

func TestCovPCDelete_NotFound_404(t *testing.T) {
	h, userID, wsID, _ := covPCHandler(t)
	req := httptest.NewRequest("DELETE", "/x", nil)
	req.SetPathValue("slug", "ghost")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestCovPCDelete_HappyPath_SoftDeletes(t *testing.T) {
	h, userID, wsID, crewID := covPCHandler(t)
	covPCInsertPipeline(t, h.db, wsID, "d_pln2", "byebye", crewID, 0, 1, 0)
	req := httptest.NewRequest("DELETE", "/x", nil)
	req.SetPathValue("slug", "byebye")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rr.Code)
	}
	// Soft delete sets deleted_at; the row still exists but List won't show it.
	var deletedAt sql.NullString
	if err := h.db.QueryRow(`SELECT deleted_at FROM pipelines WHERE id='d_pln2'`).Scan(&deletedAt); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if !deletedAt.Valid {
		t.Error("deleted_at should be set after soft delete")
	}
}
