package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/license"
)

// crews_create_cov_test.go covers the remaining Create branches: the
// license-gate call, invalid restricted-mode domains, the soft-deleted
// crew cleanup warn paths (forced via RAISE triggers), the
// devcontainer common-utils auto-inject, the runtime_image parse
// rejection, and the final INSERT failure. Helpers are prefixed covCC.

func covCCHandler(t *testing.T) (*CrewHandler, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	return NewCrewHandler(db, newTestLogger()), userID, wsID
}

func covCCPost(h *CrewHandler, userID, wsID string, body map[string]any) *httptest.ResponseRecorder {
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/crews", jsonBody(body)), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	return rr
}

// TestCovCC_LicenseGate_NoOpAllows — wiring a license must not block
// creation in v0.1 (CheckCrewLimit is a documented no-op). The 402/500
// arms are unreachable until enforcement lands.
func TestCovCC_LicenseGate_NoOpAllows(t *testing.T) {
	h, userID, wsID := covCCHandler(t)
	h.SetLicense(&license.License{})
	rr := covCCPost(h, userID, wsID, map[string]any{"name": "Licensed Crew", "slug": "covcc-lic"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovCC_RestrictedMode_InvalidDomain_400(t *testing.T) {
	h, userID, wsID := covCCHandler(t)
	rr := covCCPost(h, userID, wsID, map[string]any{
		"name": "Net Crew", "slug": "covcc-net",
		"network_mode":    "restricted",
		"allowed_domains": []string{"   "},
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "invalid domain") {
		t.Errorf("body = %s, want invalid domain error", rr.Body.String())
	}
}

// TestCovCC_SoftDeletedCleanupFailures_WarnAndInsertConflict forces all
// three cleanup statements (mission_tasks delete, missions delete, slug
// rename) to fail via RAISE triggers. Each failure is non-fatal (warn),
// but because the soft-deleted crew then still holds the slug, the final
// INSERT hits the UNIQUE constraint → 500.
func TestCovCC_SoftDeletedCleanupFailures_WarnAndInsertConflict(t *testing.T) {
	h, userID, wsID := covCCHandler(t)
	db := h.db
	// Soft-deleted crew occupying the slug, with one mission + task.
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug, deleted_at)
		VALUES ('covcc-old', ?, 'Old', 'covcc-dup', datetime('now'))`, wsID)
	seedAgentRow(t, db, "covcc-lead", wsID, "covcc-old", "Lead", "covcc-lead", "LEAD")
	execOrFatal(t, db, `INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at)
		VALUES ('covcc-m1', ?, 'covcc-old', 'covcc-lead', 'covcc-tr', 'M', 'IN_PROGRESS', datetime('now'))`, wsID)
	execOrFatal(t, db, `INSERT INTO mission_tasks (id, mission_id, title, status, task_order, created_at, updated_at)
		VALUES ('covcc-t1', 'covcc-m1', 'T', 'PENDING', 1, datetime('now'), datetime('now'))`)
	execOrFatal(t, db, `CREATE TRIGGER covcc_block_del_tasks BEFORE DELETE ON mission_tasks
		BEGIN SELECT RAISE(ABORT, 'covcc forced'); END`)
	execOrFatal(t, db, `CREATE TRIGGER covcc_block_del_missions BEFORE DELETE ON missions
		BEGIN SELECT RAISE(ABORT, 'covcc forced'); END`)
	execOrFatal(t, db, `CREATE TRIGGER covcc_block_upd_crews BEFORE UPDATE ON crews
		BEGIN SELECT RAISE(ABORT, 'covcc forced'); END`)

	rr := covCCPost(h, userID, wsID, map[string]any{"name": "Dup Crew", "slug": "covcc-dup"})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (insert blocked by stale unique slug); body=%s",
			rr.Code, rr.Body.String())
	}
	// The mission row must have survived (cleanup was blocked).
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM missions WHERE id = 'covcc-m1'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("mission rows = %d err=%v, want untouched row", n, err)
	}
}

// TestCovCC_DevcontainerAutoInjectsCommonUtils — a devcontainer config
// without a user-creating feature gets common-utils injected before
// persistence.
func TestCovCC_DevcontainerAutoInjectsCommonUtils(t *testing.T) {
	h, userID, wsID := covCCHandler(t)
	rr := covCCPost(h, userID, wsID, map[string]any{
		"name": "Dev Crew", "slug": "covcc-dev",
		"devcontainer_config": `{"image":"debian:stable"}`,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var stored string
	if err := h.db.QueryRow(`SELECT devcontainer_config FROM crews WHERE id = ?`, resp.ID).Scan(&stored); err != nil {
		t.Fatalf("read crew: %v", err)
	}
	if !strings.Contains(stored, "common-utils") {
		t.Errorf("stored devcontainer_config = %s, want auto-injected common-utils feature", stored)
	}
}

func TestCovCC_RuntimeImage_UnparseableRef_400(t *testing.T) {
	h, userID, wsID := covCCHandler(t)
	// Invalid reference syntax fails name.ParseReference locally —
	// no registry round-trip happens.
	rr := covCCPost(h, userID, wsID, map[string]any{
		"name": "Img Crew", "slug": "covcc-img",
		"runtime_image": "UPPERCASE NOT ALLOWED::",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "invalid runtime_image") {
		t.Errorf("body = %s, want invalid runtime_image error", rr.Body.String())
	}
}
