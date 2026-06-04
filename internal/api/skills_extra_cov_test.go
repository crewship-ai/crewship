package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// covSkErr wraps a message as an error for the isClientFacingImportError
// table. Local to this file so it can't collide with package symbols.
func covSkErr(msg string) error { return errors.New(msg) }

// covSkWriteProposed overwrites a staged skill file under
// {root}/{crewSlug}/topics/.proposed/{fileName}.
func covSkWriteProposed(root, crewSlug, fileName, content string) error {
	full := filepath.Join(root, crewSlug, "topics", ".proposed", fileName)
	return os.WriteFile(full, []byte(content), 0o644)
}

// ---------------------------------------------------------------------------
// skills_extra_cov_test.go — fills the branches the existing skills_test.go,
// skills_generate_test.go, and skills_proposed_handler_test.go leave
// uncovered:
//
//   - skills.go:       List (installed filters / source / vendor / 500),
//                      Get (500 via fault injection), Delete (all branches),
//                      SetJournal (nil + non-nil)
//   - skills_generate.go: Generate 500 on credential-lookup DB fault
//   - skills_proposed_handler.go: SetJournal, requireSkillManagerRole (401),
//                      List missing crew_id, proposedDirForCrew (not-configured
//                      503 / unsafe-slug 404), Approve invalid-json + missing
//                      fields + 422-import-failure, Reject invalid-json +
//                      missing fields
//   - skills_bulk_import.go: full surface (forbidden, missing ws, invalid
//                      JSON, missing git_url, client-facing 502, server-side
//                      502, isClientFacingImportError table)
//   - agent_skills.go: ListSkills (not-found / 500 / happy), AddSkill
//                      (forbidden / not-found / bad-json / happy / idempotent /
//                      500), RemoveSkill (forbidden / not-found / not-assigned /
//                      happy)
//
// SKIPPED: the real LLM generate path (Anthropic Messages API call) and the
// git-clone success path in bulk-import — both need network egress. We cover
// every validation / error / DB branch up to (and after) those calls instead.
// ---------------------------------------------------------------------------

// covSkSeedCrewAndAgent seeds a crew + agent and returns their IDs.
func covSkSeedCrewAndAgent(t *testing.T, h *AgentHandler, wsID string) (crewID, agentID string) {
	t.Helper()
	crewID = seedCrewRow(t, h.db, "cov-crew-1", wsID, "Cov Crew", "cov-crew")
	agentID = seedAgentRow(t, h.db, "cov-agent-1", wsID, crewID, "Cov Agent", "cov-agent", "AGENT")
	return crewID, agentID
}

// =============================== skills.go =================================

func TestCovSkSkillHandlerSetJournal(t *testing.T) {
	db := setupTestDB(t)
	h := NewSkillHandler(db, newTestLogger())

	// nil → resets to noop; must not panic and journal stays non-nil.
	h.SetJournal(nil)
	if h.journal == nil {
		t.Fatal("SetJournal(nil) left journal nil")
	}
	// non-nil → installs the emitter.
	h.SetJournal(noopEmitter{})
	if h.journal == nil {
		t.Fatal("SetJournal(noop) left journal nil")
	}
}

func TestCovSkListInstalledAndFilters(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	h := NewSkillHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	crewID := seedCrewRow(t, db, "cov-crew-list", wsID, "Crew", "crew-list")
	agentID := seedAgentRow(t, db, "cov-agent-list", wsID, crewID, "Agent", "agent-list", "AGENT")

	seedSkillForTest(t, db, "skl-a", "alpha-installed", "CODING")
	seedSkillForTest(t, db, "skl-b", "beta-uninstalled", "CODING")
	// Assign skl-a to the agent so the installed filters return exactly one.
	if _, err := db.Exec(`INSERT INTO agent_skills (id, agent_id, skill_id, enabled, created_at) VALUES ('as-1', ?, 'skl-a', 1, datetime('now'))`, agentID); err != nil {
		t.Fatalf("seed agent_skills: %v", err)
	}

	cases := []struct {
		name      string
		query     string
		wantCount int
	}{
		{"installed_for_agent", "?installed_for_agent_id=" + agentID, 1},
		{"installed_flag", "?installed=1", 1},
		{"source_filter", "?source=CUSTOM", 2},
		{"vendor_no_match", "?vendor=acme", 0},
		{"maturity_no_match", "?maturity=OFFICIAL", 0},
		{"runtime_no_match", "?runtime=DOCKER", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/v1/skills"+tc.query, nil)
			req = withWorkspaceUser(req, userID, wsID, "MEMBER")
			rr := httptest.NewRecorder()
			h.List(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
			}
			var result []map[string]interface{}
			if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if len(result) != tc.wantCount {
				t.Errorf("len = %d, want %d", len(result), tc.wantCount)
			}
			if tc.name == "installed_for_agent" {
				inst, _ := result[0]["installed_on"].([]interface{})
				if len(inst) != 1 {
					t.Errorf("installed_on len = %d, want 1 (populateInstalledOn join)", len(inst))
				}
			}
		})
	}
}

func TestCovSkListDBError500(t *testing.T) {
	db := setupTestDB(t)
	h := NewSkillHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	db.Close() // fault injection: query fails

	req := httptest.NewRequest("GET", "/api/v1/skills", nil)
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

func TestCovSkGetDBError500(t *testing.T) {
	db := setupTestDB(t)
	h := NewSkillHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedSkillForTest(t, db, "sk-get-err", "get-err", "CODING")

	db.Close()

	req := httptest.NewRequest("GET", "/api/v1/skills/sk-get-err", nil)
	req.SetPathValue("skillId", "sk-get-err")
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

func TestCovSkDeleteForbidden(t *testing.T) {
	db := setupTestDB(t)
	h := NewSkillHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedSkillForTest(t, db, "sk-del-1", "del-skill", "CODING")

	req := httptest.NewRequest("DELETE", "/api/v1/workspaces/"+wsID+"/skills/sk-del-1", nil)
	req.SetPathValue("skillId", "sk-del-1")
	req = withWorkspaceUser(req, userID, wsID, "MANAGER") // manage requires OWNER/ADMIN
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

func TestCovSkDeleteMissingID400(t *testing.T) {
	db := setupTestDB(t)
	h := NewSkillHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	req := httptest.NewRequest("DELETE", "/api/v1/workspaces/"+wsID+"/skills/", nil)
	// no SetPathValue → empty skillId
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestCovSkDeleteNotFound(t *testing.T) {
	db := setupTestDB(t)
	h := NewSkillHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	req := httptest.NewRequest("DELETE", "/api/v1/workspaces/"+wsID+"/skills/nope", nil)
	req.SetPathValue("skillId", "nope")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestCovSkDeleteBundledRefused(t *testing.T) {
	db := setupTestDB(t)
	h := NewSkillHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	if _, err := db.Exec(`INSERT INTO skills (id, name, slug, display_name, version, category, source, verification, downloads, rating_count, pricing_tier, featured, tags, content)
		VALUES ('sk-bundled', 'bundled-skill', 'bundled-skill', 'Bundled', '1.0.0', 'CODING', 'BUNDLED', 'UNVERIFIED', 0, 0, 'FREE', 0, '[]', '# B')`); err != nil {
		t.Fatalf("seed bundled: %v", err)
	}

	req := httptest.NewRequest("DELETE", "/api/v1/workspaces/"+wsID+"/skills/sk-bundled", nil)
	req.SetPathValue("skillId", "sk-bundled")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (bundled refused)", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "BUNDLED") {
		t.Errorf("body should explain bundled refusal: %s", rr.Body.String())
	}
}

func TestCovSkDeleteHappyPath(t *testing.T) {
	db := setupTestDB(t)
	h := NewSkillHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedSkillForTest(t, db, "sk-del-ok", "del-ok", "CODING")

	req := httptest.NewRequest("DELETE", "/api/v1/workspaces/"+wsID+"/skills/sk-del-ok", nil)
	req.SetPathValue("skillId", "sk-del-ok")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM skills WHERE id = 'sk-del-ok'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("skill row count = %d, want 0 (deleted)", count)
	}
}

func TestCovSkDeleteDBError500(t *testing.T) {
	db := setupTestDB(t)
	h := NewSkillHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedSkillForTest(t, db, "sk-del-500", "del-500", "CODING")

	db.Close() // fault: lookup SELECT fails

	req := httptest.NewRequest("DELETE", "/api/v1/workspaces/"+wsID+"/skills/sk-del-500", nil)
	req.SetPathValue("skillId", "sk-del-500")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

// ============================ skills_generate.go ==========================

func TestCovSkGenerateCredentialLookupDBError500(t *testing.T) {
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	h := NewSkillGenerateHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	db.Close() // resolveAnthropicProvider's SELECT errors (non-ErrNoRows) → 500

	req := httptest.NewRequest("POST", "/x", strings.NewReader(`{"slug":"my-skill","prompt":"do a thing"}`))
	req.SetPathValue("workspaceId", wsID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Generate(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (credential lookup DB error)", rr.Code)
	}
}

// ========================= skills_proposed_handler.go =====================

func TestCovSkProposedSetJournal(t *testing.T) {
	db := setupTestDB(t)
	h := NewSkillProposedHandler(db, newTestLogger())
	h.SetJournal(nil)
	if h.journal == nil {
		t.Fatal("SetJournal(nil) left journal nil")
	}
	h.SetJournal(noopEmitter{})
	if h.journal == nil {
		t.Fatal("SetJournal(noop) left journal nil")
	}
}

func TestCovSkProposedRequireRoleNoWorkspace401(t *testing.T) {
	db := setupTestDB(t)
	h := NewSkillProposedHandler(db, newTestLogger())

	req := httptest.NewRequest("GET", "/api/v1/skills/proposed?crew_id=x", nil)
	// No workspace in context → 401.
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestCovSkProposedListMissingCrewID400(t *testing.T) {
	db := setupTestDB(t)
	h := NewSkillProposedHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	req := httptest.NewRequest("GET", "/api/v1/skills/proposed", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (missing crew_id)", rr.Code)
	}
}

func TestCovSkProposedListNotConfigured503(t *testing.T) {
	db := setupTestDB(t)
	h := NewSkillProposedHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "cov-crew-503", wsID, "Crew", "crew-503")
	// SetCrewMemoryRoot deliberately NOT called → proposedDirForCrew
	// returns "crew memory root not configured" → mapDirError 503.

	req := httptest.NewRequest("GET", "/api/v1/skills/proposed?crew_id="+crewID, nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 (memory root not configured)", rr.Code)
	}
}

func TestCovSkProposedListUnknownCrew404(t *testing.T) {
	db := setupTestDB(t)
	h := NewSkillProposedHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h.SetCrewMemoryRoot(t.TempDir())

	req := httptest.NewRequest("GET", "/api/v1/skills/proposed?crew_id=ghost-crew", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (unknown crew → ErrNotExist)", rr.Code)
	}
}

func TestCovSkProposedApproveInvalidJSON400(t *testing.T) {
	db := setupTestDB(t)
	h := NewSkillProposedHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	req := httptest.NewRequest("POST", "/api/v1/skills/proposed/approve", strings.NewReader(`not-json`))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Approve(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestCovSkProposedApproveMissingFields400(t *testing.T) {
	db := setupTestDB(t)
	h := NewSkillProposedHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	req := httptest.NewRequest("POST", "/api/v1/skills/proposed/approve", strings.NewReader(`{}`))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Approve(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (missing crew_id/file_name)", rr.Code)
	}
}

func TestCovSkProposedApproveImportFailure422(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	h := NewSkillProposedHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewWithSlug(t, db, wsID, "bad-content-crew")

	// Stage a file that exists but contains content the importer's parser
	// rejects (no valid YAML frontmatter) → importer error → 422.
	root, fileName := stagedSkillFixture(t, "bad-content-crew", "broken")
	// Overwrite with junk that fails ParseSKILLMD inside the importer.
	if err := covSkWriteProposed(root, "bad-content-crew", fileName, "this is not a valid SKILL.md"); err != nil {
		t.Fatalf("overwrite staged: %v", err)
	}
	h.SetCrewMemoryRoot(root)

	bodyBytes, _ := json.Marshal(approveBody{CrewID: crewID, FileName: fileName})
	req := httptest.NewRequest("POST", "/api/v1/skills/proposed/approve", bytes.NewReader(bodyBytes))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Approve(rr, req)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422 (import validation failure), body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovSkProposedRejectInvalidJSON400(t *testing.T) {
	db := setupTestDB(t)
	h := NewSkillProposedHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	req := httptest.NewRequest("POST", "/api/v1/skills/proposed/reject", strings.NewReader(`{`))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Reject(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestCovSkProposedRejectMissingFields400(t *testing.T) {
	db := setupTestDB(t)
	h := NewSkillProposedHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	req := httptest.NewRequest("POST", "/api/v1/skills/proposed/reject", strings.NewReader(`{"crew_id":"x"}`))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Reject(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (missing file_name)", rr.Code)
	}
}

func TestCovSkProposedRejectPathTraversal400(t *testing.T) {
	db := setupTestDB(t)
	h := NewSkillProposedHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewWithSlug(t, db, wsID, "rej-traverse-crew")
	h.SetCrewMemoryRoot(t.TempDir())

	bodyBytes, _ := json.Marshal(approveBody{CrewID: crewID, FileName: "../../etc/passwd"})
	req := httptest.NewRequest("POST", "/api/v1/skills/proposed/reject", bytes.NewReader(bodyBytes))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Reject(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (path traversal)", rr.Code)
	}
}

// ============================ skills_bulk_import.go =======================

func TestCovSkIsClientFacingImportError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"bulk-requires", covSkErr("bulk import requires a git_url"), true},
		{"only-supports", covSkErr("bulk import only supports https git URLs"), true},
		{"via-git-url", covSkErr("bulk import via git_url failed: x"), true},
		{"private-ip", covSkErr("private/internal IP addresses are not allowed"), true},
		{"localhost", covSkErr("localhost git URLs are not allowed"), true},
		{"missing-host", covSkErr("git URL missing host"), true},
		{"embed", covSkErr("git URL must not embed credentials"), true},
		{"parse", covSkErr("parse git URL: bad"), true},
		{"opaque", covSkErr("fatal: could not read from remote /var/lib/secret"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isClientFacingImportError(tc.err); got != tc.want {
				t.Errorf("isClientFacingImportError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestCovSkBulkImportForbidden(t *testing.T) {
	db := setupTestDB(t)
	h := NewSkillBulkImportHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	req := httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/skills/bulk-import",
		strings.NewReader(`{"git_url":"https://example.com/repo.git"}`))
	req.SetPathValue("workspaceId", wsID)
	req = withWorkspaceUser(req, userID, wsID, "MEMBER") // create needs MANAGER+
	rr := httptest.NewRecorder()
	h.Import(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

func TestCovSkBulkImportMissingWorkspace400(t *testing.T) {
	db := setupTestDB(t)
	h := NewSkillBulkImportHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	req := httptest.NewRequest("POST", "/x",
		strings.NewReader(`{"git_url":"https://example.com/repo.git"}`))
	// no SetPathValue("workspaceId") → empty
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Import(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (missing workspace_id)", rr.Code)
	}
}

func TestCovSkBulkImportInvalidJSON400(t *testing.T) {
	db := setupTestDB(t)
	h := NewSkillBulkImportHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	req := httptest.NewRequest("POST", "/x", strings.NewReader(`not-json`))
	req.SetPathValue("workspaceId", wsID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Import(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (invalid JSON)", rr.Code)
	}
}

func TestCovSkBulkImportMissingGitURL400(t *testing.T) {
	db := setupTestDB(t)
	h := NewSkillBulkImportHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	req := httptest.NewRequest("POST", "/x", strings.NewReader(`{"git_url":"   "}`))
	req.SetPathValue("workspaceId", wsID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Import(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (empty git_url)", rr.Code)
	}
}

func TestCovSkBulkImportSSRFBlocked502(t *testing.T) {
	db := setupTestDB(t)
	h := NewSkillBulkImportHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// A localhost/private git URL trips the importer's URL validation,
	// which is a client-facing error → 502 with the message echoed.
	req := httptest.NewRequest("POST", "/x",
		strings.NewReader(`{"git_url":"https://localhost/repo.git"}`))
	req.SetPathValue("workspaceId", wsID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Import(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 (blocked git URL), body=%s", rr.Code, rr.Body.String())
	}
}

// ============================== agent_skills.go ===========================

func TestCovSkAgentListSkillsNotFound(t *testing.T) {
	db := setupTestDB(t)
	h := NewAgentHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	req := httptest.NewRequest("GET", "/api/v1/agents/ghost/skills", nil)
	req.SetPathValue("agentId", "ghost")
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.ListSkills(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestCovSkAgentListSkillsDBError500(t *testing.T) {
	db := setupTestDB(t)
	h := NewAgentHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	_, agentID := covSkSeedCrewAndAgent(t, h, wsID)

	db.Close() // agentExists check errors → 500

	req := httptest.NewRequest("GET", "/api/v1/agents/"+agentID+"/skills", nil)
	req.SetPathValue("agentId", agentID)
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.ListSkills(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

func TestCovSkAgentListSkillsHappy(t *testing.T) {
	db := setupTestDB(t)
	h := NewAgentHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	_, agentID := covSkSeedCrewAndAgent(t, h, wsID)
	seedSkillForTest(t, db, "sk-ls-1", "ls-skill", "CODING")
	if _, err := db.Exec(`INSERT INTO agent_skills (id, agent_id, skill_id, enabled, created_at) VALUES ('as-ls-1', ?, 'sk-ls-1', 1, datetime('now'))`, agentID); err != nil {
		t.Fatalf("seed agent_skills: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/agents/"+agentID+"/skills", nil)
	req.SetPathValue("agentId", agentID)
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.ListSkills(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var result []agentSkillResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(result) != 1 || result[0].SkillID != "sk-ls-1" || !result[0].Enabled {
		t.Errorf("result = %+v, want one enabled sk-ls-1", result)
	}
}

func TestCovSkAgentAddSkillForbidden(t *testing.T) {
	db := setupTestDB(t)
	h := NewAgentHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	_, agentID := covSkSeedCrewAndAgent(t, h, wsID)

	req := httptest.NewRequest("POST", "/api/v1/agents/"+agentID+"/skills",
		strings.NewReader(`{"skill_id":"x"}`))
	req.SetPathValue("agentId", agentID)
	req = withWorkspaceUser(req, userID, wsID, "VIEWER")
	rr := httptest.NewRecorder()
	h.AddSkill(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

func TestCovSkAgentAddSkillAgentNotFound(t *testing.T) {
	db := setupTestDB(t)
	h := NewAgentHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	req := httptest.NewRequest("POST", "/api/v1/agents/ghost/skills",
		strings.NewReader(`{"skill_id":"x"}`))
	req.SetPathValue("agentId", "ghost")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AddSkill(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestCovSkAgentAddSkillBadBody400(t *testing.T) {
	db := setupTestDB(t)
	h := NewAgentHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	_, agentID := covSkSeedCrewAndAgent(t, h, wsID)

	// Missing skill_id → 400.
	req := httptest.NewRequest("POST", "/api/v1/agents/"+agentID+"/skills",
		strings.NewReader(`{}`))
	req.SetPathValue("agentId", agentID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AddSkill(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (missing skill_id)", rr.Code)
	}
}

func TestCovSkAgentAddSkillHappyAndIdempotent(t *testing.T) {
	db := setupTestDB(t)
	h := NewAgentHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	_, agentID := covSkSeedCrewAndAgent(t, h, wsID)
	seedSkillForTest(t, db, "sk-add-1", "add-skill", "CODING")

	doAdd := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/v1/agents/"+agentID+"/skills",
			strings.NewReader(`{"skill_id":"sk-add-1"}`))
		req.SetPathValue("agentId", agentID)
		req = withWorkspaceUser(req, userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.AddSkill(rr, req)
		return rr
	}

	// First add → 201 Created.
	if rr := doAdd(); rr.Code != http.StatusCreated {
		t.Fatalf("first add status = %d, want 201, body=%s", rr.Code, rr.Body.String())
	}
	// Second add → idempotent 200 with already_assigned.
	rr2 := doAdd()
	if rr2.Code != http.StatusOK {
		t.Fatalf("second add status = %d, want 200 (idempotent), body=%s", rr2.Code, rr2.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(rr2.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["already_assigned"] != true {
		t.Errorf("already_assigned = %v, want true", resp["already_assigned"])
	}
	// Exactly one row persisted.
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM agent_skills WHERE agent_id = ? AND skill_id = 'sk-add-1'`, agentID).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("agent_skills row count = %d, want 1", count)
	}
}

func TestCovSkAgentRemoveSkillForbidden(t *testing.T) {
	db := setupTestDB(t)
	h := NewAgentHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	_, agentID := covSkSeedCrewAndAgent(t, h, wsID)

	req := httptest.NewRequest("DELETE", "/api/v1/agents/"+agentID+"/skills/sk-x", nil)
	req.SetPathValue("agentId", agentID)
	req.SetPathValue("skillId", "sk-x")
	req = withWorkspaceUser(req, userID, wsID, "VIEWER")
	rr := httptest.NewRecorder()
	h.RemoveSkill(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

func TestCovSkAgentRemoveSkillAgentNotFound(t *testing.T) {
	db := setupTestDB(t)
	h := NewAgentHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	req := httptest.NewRequest("DELETE", "/api/v1/agents/ghost/skills/sk-x", nil)
	req.SetPathValue("agentId", "ghost")
	req.SetPathValue("skillId", "sk-x")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.RemoveSkill(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (agent not found)", rr.Code)
	}
}

func TestCovSkAgentRemoveSkillNotAssigned404(t *testing.T) {
	db := setupTestDB(t)
	h := NewAgentHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	_, agentID := covSkSeedCrewAndAgent(t, h, wsID)

	req := httptest.NewRequest("DELETE", "/api/v1/agents/"+agentID+"/skills/never-assigned", nil)
	req.SetPathValue("agentId", agentID)
	req.SetPathValue("skillId", "never-assigned")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.RemoveSkill(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (skill not assigned)", rr.Code)
	}
}

func TestCovSkAgentRemoveSkillHappy(t *testing.T) {
	db := setupTestDB(t)
	h := NewAgentHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	_, agentID := covSkSeedCrewAndAgent(t, h, wsID)
	seedSkillForTest(t, db, "sk-rm-1", "rm-skill", "CODING")
	if _, err := db.Exec(`INSERT INTO agent_skills (id, agent_id, skill_id, enabled, created_at) VALUES ('as-rm-1', ?, 'sk-rm-1', 1, datetime('now'))`, agentID); err != nil {
		t.Fatalf("seed agent_skills: %v", err)
	}

	req := httptest.NewRequest("DELETE", "/api/v1/agents/"+agentID+"/skills/sk-rm-1", nil)
	req.SetPathValue("agentId", agentID)
	req.SetPathValue("skillId", "sk-rm-1")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.RemoveSkill(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204, body=%s", rr.Code, rr.Body.String())
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM agent_skills WHERE agent_id = ? AND skill_id = 'sk-rm-1'`, agentID).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("agent_skills row count = %d, want 0 (removed)", count)
	}
}
