package api

// pipelines_crud.go coverage top-up #2 — the store-error 500 forks
// (Delete lookup/soft-delete, ListVersions, GetVersion, Rollback, Save,
// InternalSave), the ImportPipeline slug-fallback + metadata + conflict
// branches, the Save cycle-detect / parse 422s, and InternalSave's
// bound-token + lookup-warn + test-gate paths.
//
// DB failure injection uses SQLite triggers / dropped tables, same
// pattern as the rest of the cov2 files. All tests are prefixed
// TestCov2PC.

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// cov2PCRig mirrors covPCHandler but also exposes the db handle so tests
// can drop tables / add triggers.
func cov2PCRig(t *testing.T) (*PipelineHandler, *sql.DB, string, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "cov2pc_crew", wsID, "Lead Crew", "cov2pc-crew")
	seedAgentRow(t, db, "cov2pc_agent", wsID, crewID, "Lead", "agent_lead", "LEAD")
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return NewPipelineHandler(db, logger, nil, nil), db, userID, wsID, crewID
}

func cov2PCTrigger(t *testing.T, db *sql.DB, name, opAndTable string) {
	t.Helper()
	if _, err := db.Exec(`CREATE TRIGGER ` + name + ` BEFORE ` + opAndTable + `
		BEGIN SELECT RAISE(ABORT, 'cov2 injected failure'); END`); err != nil {
		t.Fatalf("create trigger %s: %v", name, err)
	}
}

// --- Delete: lookup + soft-delete store errors ---

func TestCov2PCDelete_StoreErrors(t *testing.T) {
	t.Run("soft delete blocked → 500", func(t *testing.T) {
		h, db, userID, wsID, crewID := cov2PCRig(t)
		covPCInsertPipeline(t, db, wsID, "p-del", "p-del", crewID, 0, 1, 0)
		cov2PCTrigger(t, db, "cov2pc_del_upd", "UPDATE ON pipelines")
		req := httptest.NewRequest("DELETE", "/x", nil)
		req.SetPathValue("slug", "p-del")
		req = withWorkspaceUser(req, userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.Delete(rr, req)
		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500, body=%s", rr.Code, rr.Body.String())
		}
	})

	t.Run("lookup blocked → 500", func(t *testing.T) {
		h, db, userID, wsID, _ := cov2PCRig(t)
		if _, err := db.Exec(`DROP TABLE pipelines`); err != nil {
			t.Fatalf("drop: %v", err)
		}
		req := httptest.NewRequest("DELETE", "/x", nil)
		req.SetPathValue("slug", "whatever")
		req = withWorkspaceUser(req, userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.Delete(rr, req)
		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500, body=%s", rr.Code, rr.Body.String())
		}
	})
}

// --- ImportPipeline: slug fallback, metadata provenance, conflicts ---

func cov2PCImportBody(slug, name, sourceWS string) string {
	def := covPCDef(name)
	meta := "{}"
	if sourceWS != "" {
		meta = `{"source_workspace_id":"` + sourceWS + `"}`
	}
	return `{
		"format": "crewship-pipeline-bundle/v1",
		"pipeline": {"name":"` + name + `","slug":"` + slug + `","dsl_version":"1.0","definition":` + def + `},
		"metadata": ` + meta + `,
		"author_crew_id": "cov2pc_crew"
	}`
}

func cov2PCImportReq(userID, wsID, body string) *http.Request {
	req := httptest.NewRequest("POST", "/x", strings.NewReader(body))
	return withWorkspaceUser(req, userID, wsID, "OWNER")
}

func TestCov2PCImport_SlugFallbackAndMetadata(t *testing.T) {
	h, db, userID, wsID, _ := cov2PCRig(t)
	// Empty slug → name is used as the slug; metadata source ws is
	// recorded as the imported-from provenance.
	rr := httptest.NewRecorder()
	h.ImportPipeline(rr, cov2PCImportReq(userID, wsID, cov2PCImportBody("", "importedpipe", "ws-source-1")))
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body=%s", rr.Code, rr.Body.String())
	}
	var slug, via, importedURL string
	if err := db.QueryRow(`SELECT slug, authored_via, COALESCE(imported_from_url,'') FROM pipelines WHERE workspace_id = ?`, wsID).
		Scan(&slug, &via, &importedURL); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if slug != "importedpipe" {
		t.Errorf("slug = %q, want name fallback importedpipe", slug)
	}
	if via != "imported" {
		t.Errorf("authored_via = %q, want imported", via)
	}
	if importedURL != "workspace:ws-source-1" {
		t.Errorf("imported_from_url = %q, want workspace:ws-source-1", importedURL)
	}
}

func TestCov2PCImport_ResurrectsSoftDeleted(t *testing.T) {
	h, db, userID, wsID, crewID := cov2PCRig(t)
	// A soft-deleted pipeline keeps its UNIQUE(workspace_id, slug) row; the
	// upsert lookup now finds it and resurrects via the UPDATE path rather
	// than tripping the constraint — so re-importing a previously-deleted
	// slug succeeds (this is what makes `seed --nuke` re-imports work).
	covPCInsertPipeline(t, db, wsID, "p-exists", "takenpipe", crewID, 0, 1, 0)
	if _, err := db.Exec(`UPDATE pipelines SET deleted_at = datetime('now') WHERE id = 'p-exists'`); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	rr := httptest.NewRecorder()
	h.ImportPipeline(rr, cov2PCImportReq(userID, wsID, cov2PCImportBody("takenpipe", "takenpipe", "")))
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (resurrected), body=%s", rr.Code, rr.Body.String())
	}
}

// --- ListVersions / GetVersion / Rollback: versions-table failures ---

func TestCov2PCVersionEndpoints_StoreError500(t *testing.T) {
	h, db, userID, wsID, crewID := cov2PCRig(t)
	covPCInsertPipeline(t, db, wsID, "p-v", "p-v", crewID, 0, 1, 0)
	if _, err := db.Exec(`DROP TABLE pipeline_versions`); err != nil {
		t.Fatalf("drop: %v", err)
	}

	t.Run("ListVersions", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/x?limit=5", nil)
		req.SetPathValue("slug", "p-v")
		req = withWorkspaceUser(req, userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.ListVersions(rr, req)
		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500, body=%s", rr.Code, rr.Body.String())
		}
	})

	t.Run("GetVersion", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/x", nil)
		req.SetPathValue("slug", "p-v")
		req.SetPathValue("n", "1")
		req = withWorkspaceUser(req, userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.GetVersion(rr, req)
		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500, body=%s", rr.Code, rr.Body.String())
		}
	})

	t.Run("Rollback", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/x", strings.NewReader(`{"version":1}`))
		req.SetPathValue("slug", "p-v")
		req = withWorkspaceUser(req, userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.Rollback(rr, req)
		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500, body=%s", rr.Code, rr.Body.String())
		}
	})
}

// --- Save: parse 422, cycle 422, conflict 409, store failure 500 ---

func TestCov2PCSave_ParseError422(t *testing.T) {
	h, _, userID, wsID, _ := cov2PCRig(t)
	body := `{"slug":"badp","definition":{"steps":"not-an-array"}}`
	req := httptest.NewRequest("POST", "/x", strings.NewReader(body))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Save(rr, req)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (parse), body=%s", rr.Code, rr.Body.String())
	}
}

func TestCov2PCSave_CycleDetected422(t *testing.T) {
	h, db, userID, wsID, crewID := cov2PCRig(t)
	// Saved pipeline whose DSL *name* equals the candidate's name and
	// which the candidate calls — the walk re-enters "cyc-cand" → cycle.
	loopDef := `{"name":"cyc-cand","steps":[{"id":"a","type":"agent_run","agent_slug":"agent_lead","prompt":"x"}]}`
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`
		INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash,
			ephemeral, workspace_visible, invocation_count, author_crew_id, authored_via,
			last_test_run_at, last_test_run_passed, created_at, updated_at)
		VALUES ('p-loop', ?, 'cyc-target', 'cyc-target', ?, 'h1', 0, 1, 0, ?, 'user_api', ?, 1, ?, ?)`,
		wsID, loopDef, crewID, now, now, now); err != nil {
		t.Fatalf("insert loop target: %v", err)
	}

	candidate := `{"name":"cyc-cand","steps":[{"id":"c","type":"call_pipeline","pipeline_slug":"cyc-target"}]}`
	body := `{"slug":"cyc-cand","name":"cyc cand","definition":` + candidate + `,"last_test_run_passed":true,"last_test_run_at":"` + now + `"}`
	req := httptest.NewRequest("POST", "/x", strings.NewReader(body))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Save(rr, req)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (cycle), body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "cycle") {
		t.Errorf("body = %s, want cycle error", rr.Body.String())
	}
}

func TestCov2PCSave_ResurrectsSoftDeleted(t *testing.T) {
	h, db, userID, wsID, crewID := cov2PCRig(t)
	// Soft-deleted row still holds the UNIQUE(workspace_id, slug) slot; the
	// upsert lookup finds it and resurrects via the UPDATE path, so saving a
	// previously-deleted slug succeeds (idempotent re-seed contract).
	covPCInsertPipeline(t, db, wsID, "p-c1", "clashslug", crewID, 0, 1, 0)
	if _, err := db.Exec(`UPDATE pipelines SET deleted_at = datetime('now') WHERE id = 'p-c1'`); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	req := httptest.NewRequest("POST", "/x", strings.NewReader(covPCSaveBody("clashslug", crewID, nil)))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Save(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (resurrected), body=%s", rr.Code, rr.Body.String())
	}
}

func TestCov2PCSave_StoreInsertBlocked500(t *testing.T) {
	h, db, userID, wsID, crewID := cov2PCRig(t)
	cov2PCTrigger(t, db, "cov2pc_save_ins", "INSERT ON pipelines")
	req := httptest.NewRequest("POST", "/x", strings.NewReader(covPCSaveBody("blockedp", crewID, nil)))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Save(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", rr.Code, rr.Body.String())
	}
}

// --- InternalSave ---

func cov2PCInternalBody(wsID, slug string, withTestRun bool) string {
	def := covPCDef(slug)
	testRun := ""
	if withTestRun {
		testRun = `,"last_test_run_at":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","last_test_run_passed":true`
	}
	return `{"workspace_id":"` + wsID + `","slug":"` + slug + `","name":"` + slug + `","author_crew_id":"cov2pc_crew","definition":` + def + testRun + `}`
}

func TestCov2PCInternalSave_BoundTokenMismatch403(t *testing.T) {
	h, _, _, wsID, _ := cov2PCRig(t)
	req := httptest.NewRequest("POST", "/x", strings.NewReader(cov2PCInternalBody(wsID, "ip1", true)))
	req = req.WithContext(context.WithValue(req.Context(), ctxInternalTokenWS, "ws-other"))
	rr := httptest.NewRecorder()
	h.InternalSave(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (foreign workspace), body=%s", rr.Code, rr.Body.String())
	}
}

func TestCov2PCInternalSave_ParseError422(t *testing.T) {
	h, _, _, wsID, _ := cov2PCRig(t)
	body := `{"workspace_id":"` + wsID + `","slug":"ip2","definition":{"steps":"nope"}}`
	rr := httptest.NewRecorder()
	h.InternalSave(rr, httptest.NewRequest("POST", "/x", strings.NewReader(body)))
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422, body=%s", rr.Code, rr.Body.String())
	}
}

func TestCov2PCInternalSave_AgentLookupWarnStillSaves(t *testing.T) {
	h, db, _, wsID, crewID := cov2PCRig(t)
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("fk off: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE agents`); err != nil {
		t.Fatalf("drop agents: %v", err)
	}
	body := `{"workspace_id":"` + wsID + `","slug":"ip3","name":"ip3","author_crew_id":"` + crewID + `",
		"definition":` + covPCDef("ip3") + `,
		"last_test_run_at":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","last_test_run_passed":true}`
	rr := httptest.NewRecorder()
	h.InternalSave(rr, httptest.NewRequest("POST", "/x", strings.NewReader(body)))
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (lookup failure is non-fatal), body=%s", rr.Code, rr.Body.String())
	}
}

func TestCov2PCInternalSave_TestGateFailed422(t *testing.T) {
	h, _, _, wsID, _ := cov2PCRig(t)
	rr := httptest.NewRecorder()
	h.InternalSave(rr, httptest.NewRequest("POST", "/x", strings.NewReader(cov2PCInternalBody(wsID, "ip4", false))))
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (test gate), body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "test_run") {
		t.Errorf("body = %s, want test_run gate message", rr.Body.String())
	}
}

func TestCov2PCInternalSave_ResurrectsSoftDeleted(t *testing.T) {
	h, db, _, wsID, crewID := cov2PCRig(t)
	covPCInsertPipeline(t, db, wsID, "p-ic", "ip5", crewID, 0, 1, 0)
	if _, err := db.Exec(`UPDATE pipelines SET deleted_at = datetime('now') WHERE id = 'p-ic'`); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	rr := httptest.NewRecorder()
	h.InternalSave(rr, httptest.NewRequest("POST", "/x", strings.NewReader(cov2PCInternalBody(wsID, "ip5", true))))
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (resurrected), body=%s", rr.Code, rr.Body.String())
	}
}

func TestCov2PCInternalSave_StoreInsertBlocked500(t *testing.T) {
	h, db, _, wsID, _ := cov2PCRig(t)
	cov2PCTrigger(t, db, "cov2pc_isave_ins", "INSERT ON pipelines")
	rr := httptest.NewRecorder()
	h.InternalSave(rr, httptest.NewRequest("POST", "/x", strings.NewReader(cov2PCInternalBody(wsID, "ip6", true))))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", rr.Code, rr.Body.String())
	}
}

func TestCov2PCInternalSave_CycleDetected422(t *testing.T) {
	h, db, _, wsID, crewID := cov2PCRig(t)
	loopDef := `{"name":"icyc-cand","steps":[{"id":"a","type":"agent_run","agent_slug":"agent_lead","prompt":"x"}]}`
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`
		INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash,
			ephemeral, workspace_visible, invocation_count, author_crew_id, authored_via,
			last_test_run_at, last_test_run_passed, created_at, updated_at)
		VALUES ('p-iloop', ?, 'icyc-target', 'icyc-target', ?, 'h2', 0, 1, 0, ?, 'user_api', ?, 1, ?, ?)`,
		wsID, loopDef, crewID, now, now, now); err != nil {
		t.Fatalf("insert loop target: %v", err)
	}
	candidate := `{"name":"icyc-cand","steps":[{"id":"c","type":"call_pipeline","pipeline_slug":"icyc-target"}]}`
	body := `{"workspace_id":"` + wsID + `","slug":"icyc-cand","name":"icyc","definition":` + candidate + `,
		"last_test_run_at":"` + now + `","last_test_run_passed":true}`
	rr := httptest.NewRecorder()
	h.InternalSave(rr, httptest.NewRequest("POST", "/x", strings.NewReader(body)))
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (cycle), body=%s", rr.Code, rr.Body.String())
	}
}
