package api

// Branch-coverage tests for feature_flags_handler.go, templates.go, and
// consolidate_handler.go — focused on the error/fault-injection branches
// the existing *_test.go files don't reach.
//
// Fault-injection pattern: seed valid rows, build a valid request, then
// db.Close() immediately before invoking the handler. The first query
// the handler runs then returns "sql: database is closed", driving the
// 500 (or handler-specific 5xx) branch. setupTestDB already registers a
// t.Cleanup(db.Close); a second close is harmless.
//
// SKIPPED (per task scope): the actual LLM/consolidator background run
// (consolidate kicks a goroutine against a Summarizer + writes
// learned-*.md). We cover Run's synchronous trigger/validation/status
// branches only and never block on or assert the goroutine's effects.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/consolidate"
)

// ── feature flags ──────────────────────────────────────────────────────────

func TestCovFTCFeatureFlagList_DBClosed500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewFeatureFlagHandler(db, nil, newTestLogger())

	db.Close()

	req := httptest.NewRequest("GET", "/api/v1/feature-flags", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovFTCFeatureFlagCreate_DBClosed500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewFeatureFlagHandler(db, nil, newTestLogger())

	db.Close()

	req := httptest.NewRequest("POST", "/api/v1/feature-flags",
		bytes.NewBufferString(`{"key":"k1","percentage":50}`))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Create(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovFTCFeatureFlagUpdate_DBClosed500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	if _, err := db.Exec(`INSERT INTO feature_flags (id, key, enabled, percentage, created_at, updated_at)
		VALUES ('ff1','flagx',1,10,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed flag: %v", err)
	}
	h := NewFeatureFlagHandler(db, nil, newTestLogger())

	db.Close()

	req := httptest.NewRequest("PATCH", "/api/v1/feature-flags/flagx",
		bytes.NewBufferString(`{"enabled":false}`))
	req.SetPathValue("key", "flagx")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Update(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovFTCFeatureFlagUpdate_MissingKey400(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewFeatureFlagHandler(db, nil, newTestLogger())

	req := httptest.NewRequest("PATCH", "/api/v1/feature-flags/",
		bytes.NewBufferString(`{"enabled":true}`))
	// no path value set → key == ""
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Update(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovFTCFeatureFlagDelete_DBClosed500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewFeatureFlagHandler(db, nil, newTestLogger())

	db.Close()

	req := httptest.NewRequest("DELETE", "/api/v1/feature-flags/flagx", nil)
	req.SetPathValue("key", "flagx")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovFTCFeatureFlagUpsertOverride_DBClosed500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	if _, err := db.Exec(`INSERT INTO feature_flags (id, key, enabled, percentage, created_at, updated_at)
		VALUES ('ff1','flagx',1,10,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed flag: %v", err)
	}
	h := NewFeatureFlagHandler(db, nil, newTestLogger())

	db.Close()

	req := httptest.NewRequest("PUT", "/api/v1/feature-flags/flagx/override",
		bytes.NewBufferString(`{"enabled":true}`))
	req.SetPathValue("key", "flagx")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.UpsertOverride(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovFTCFeatureFlagUpsertOverride_NoEnabled400(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewFeatureFlagHandler(db, nil, newTestLogger())

	req := httptest.NewRequest("PUT", "/api/v1/feature-flags/flagx/override",
		bytes.NewBufferString(`{}`))
	req.SetPathValue("key", "flagx")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.UpsertOverride(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (explicit enabled required); body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovFTCFeatureFlagDeleteOverride_DBClosed500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	if _, err := db.Exec(`INSERT INTO feature_flags (id, key, enabled, percentage, created_at, updated_at)
		VALUES ('ff1','flagx',1,10,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed flag: %v", err)
	}
	h := NewFeatureFlagHandler(db, nil, newTestLogger())

	db.Close()

	req := httptest.NewRequest("DELETE", "/api/v1/feature-flags/flagx/override", nil)
	req.SetPathValue("key", "flagx")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.DeleteOverride(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

// ── templates ────────────────────────────────────────────────────────────

func TestCovFTCTemplateList_DBClosed500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewTemplateHandler(db, newTestLogger())

	db.Close()

	req := httptest.NewRequest("GET", "/api/v1/templates", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovFTCTemplateGet_DBClosed500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewTemplateHandler(db, newTestLogger())

	db.Close()

	req := httptest.NewRequest("GET", "/api/v1/templates/wt_x", nil)
	req.SetPathValue("templateId", "wt_x")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Get(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovFTCTemplateCreate_DBClosed500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewTemplateHandler(db, newTestLogger())

	db.Close()

	req := httptest.NewRequest("POST", "/api/v1/templates",
		bytes.NewBufferString(`{"name":"T","template_json":{"a":1}}`))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Create(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovFTCTemplateUpdate_CheckModifiableDBClosed500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewTemplateHandler(db, newTestLogger())

	db.Close()

	req := httptest.NewRequest("PATCH", "/api/v1/templates/wt_x",
		bytes.NewBufferString(`{"name":"new"}`))
	req.SetPathValue("templateId", "wt_x")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Update(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovFTCTemplateUpdate_BadJSON400(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewTemplateHandler(db, newTestLogger())

	req := httptest.NewRequest("PATCH", "/api/v1/templates/wt_x",
		bytes.NewBufferString(`{not json`))
	req.SetPathValue("templateId", "wt_x")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Update(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovFTCTemplateDelete_DBClosed500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewTemplateHandler(db, newTestLogger())

	db.Close()

	req := httptest.NewRequest("DELETE", "/api/v1/templates/wt_x", nil)
	req.SetPathValue("templateId", "wt_x")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

// ── consolidate ──────────────────────────────────────────────────────────

func TestCovFTCConsolidateRun_NoWorkspace401(t *testing.T) {
	db := setupTestDB(t)
	h := NewConsolidateHandler(db, newTestLogger())

	req := httptest.NewRequest("POST", "/api/v1/consolidate/run",
		bytes.NewBufferString(`{}`))
	// no workspace/user context → WorkspaceIDFromContext == ""
	rr := httptest.NewRecorder()
	h.Run(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovFTCConsolidateRun_InvalidJSON400(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewConsolidateHandler(db, newTestLogger())

	req := httptest.NewRequest("POST", "/api/v1/consolidate/run",
		bytes.NewBufferString(`{not json`))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Run(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovFTCConsolidateRun_CrewValidateDBClosed500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewConsolidateHandler(db, newTestLogger())
	h.SetConsolidator(&consolidate.Consolidator{
		DB:         db,
		Journal:    noopEmitter{},
		Summarizer: &stubSummarizer{},
		Logger:     newTestLogger(),
	})

	db.Close()

	// crew_id supplied → crewLiveInWorkspace runs a query against the
	// closed DB → 500 "failed to validate crew".
	req := httptest.NewRequest("POST", "/api/v1/consolidate/run",
		bytes.NewBufferString(`{"crew_id":"crew-x"}`))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Run(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovFTCConsolidateRun_CrewNotFound404(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewConsolidateHandler(db, newTestLogger())
	h.SetConsolidator(&consolidate.Consolidator{
		DB:         db,
		Journal:    noopEmitter{},
		Summarizer: &stubSummarizer{},
		Logger:     newTestLogger(),
	})

	req := httptest.NewRequest("POST", "/api/v1/consolidate/run",
		bytes.NewBufferString(`{"crew_id":"does-not-exist"}`))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Run(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}
