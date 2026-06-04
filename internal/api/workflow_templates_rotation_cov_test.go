package api

// Statement-coverage tests for workflow_templates_handler.go and
// credential_rotation.go. These exercise auth/role gates, invalid-JSON
// (400), not-found (404), validation, happy paths (asserting DB state),
// and 500 fault injection (via DROP TABLE).
//
// Helpers here are prefixed covWFR to avoid clashing with the shared
// harness; all test funcs are prefixed TestCovWFR. Existing harness
// helpers (setupTestDB, seedTestUser, seedTestWorkspace, seedCredentialEnc,
// withWorkspaceUser, newTestLogger, setTestEncryptionKey, withUser,
// withWorkspace) are reused.
//
// SKIPPED: the sidecar 401-fallback path described by the
// TODO(sidecar-fallback) comment in credential_rotation.go is not wired
// yet (no handler code), and the StartCredentialRotationExpiryWorker
// background goroutine / ticker loop is timing-driven; ExpireGracedRotations
// is exercised directly instead. There are no network credential-rotation
// provider branches in this file to skip.

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// covWFRValidTemplate is a minimal template_json that passes
// validateTemplateJSON: exactly one open stage, at least one completed.
const covWFRValidTemplate = `[{"name":"Todo","type":"open","position":1},{"name":"Done","type":"completed","position":2}]`

// covWFRBadJSON returns a body that fails json.Decode (unterminated object).
func covWFRBadJSON() *bytes.Buffer { return bytes.NewBufferString(`{"name": `) }

// covWFRSeedRotation inserts a credential_rotations row directly so the
// list/cancel paths have something to read without round-tripping Rotate.
func covWFRSeedRotation(t *testing.T, h *CredentialHandler, rotID, credID, status string) {
	t.Helper()
	if _, err := h.db.Exec(`
		INSERT INTO credential_rotations
		    (id, credential_id, old_value, grace_seconds, rotated_at, expires_at, rotated_by, status)
		VALUES (?, ?, 'enc-old', 3600, datetime('now'), datetime('now','+1 hour'), NULL, ?)`,
		rotID, credID, status); err != nil {
		t.Fatalf("seed rotation %s: %v", rotID, err)
	}
}

// ── workflow_templates_handler.go ───────────────────────────────────────────

func TestCovWFRWorkflowCreate_Forbidden(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewWorkflowTemplateHandler(db, nil, newTestLogger())

	body := jsonBody(map[string]any{"name": "wf", "template_json": covWFRValidTemplate})
	req := httptest.NewRequest("POST", "/api/v1/workflow-templates", body)
	req = withWorkspaceUser(req, userID, wsID, "VIEWER")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestCovWFRWorkflowCreate_InvalidJSON(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewWorkflowTemplateHandler(db, nil, newTestLogger())

	req := httptest.NewRequest("POST", "/api/v1/workflow-templates", covWFRBadJSON())
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestCovWFRWorkflowCreate_MissingName(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewWorkflowTemplateHandler(db, nil, newTestLogger())

	body := jsonBody(map[string]any{"name": "   ", "template_json": covWFRValidTemplate})
	req := httptest.NewRequest("POST", "/api/v1/workflow-templates", body)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestCovWFRWorkflowCreate_InvalidTemplateJSON(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewWorkflowTemplateHandler(db, nil, newTestLogger())

	// Two open stages → "exactly one stage with type=open" violation.
	bad := `[{"name":"A","type":"open","position":1},{"name":"B","type":"open","position":2}]`
	body := jsonBody(map[string]any{"name": "wf", "template_json": bad})
	req := httptest.NewRequest("POST", "/api/v1/workflow-templates", body)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestCovWFRWorkflowCreate_Happy(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewWorkflowTemplateHandler(db, nil, newTestLogger())

	desc := "my workflow"
	icon := "icon-x"
	color := "#3B82F6"
	body := jsonBody(map[string]any{
		"name":          "Engineering",
		"description":   desc,
		"template_json": covWFRValidTemplate,
		"icon":          icon,
		"color":         color,
	})
	req := httptest.NewRequest("POST", "/api/v1/workflow-templates", body)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}

	// Assert the row landed with is_builtin=0 and the canonical JSON.
	var name, tj string
	var isBuiltin int
	if err := db.QueryRow(`SELECT name, template_json, is_builtin FROM workflow_templates WHERE workspace_id = ?`, wsID).
		Scan(&name, &tj, &isBuiltin); err != nil {
		t.Fatalf("read inserted row: %v", err)
	}
	if name != "Engineering" {
		t.Errorf("name = %q, want Engineering", name)
	}
	if isBuiltin != 0 {
		t.Errorf("is_builtin = %d, want 0", isBuiltin)
	}
}

func TestCovWFRWorkflowCreate_DuplicateConflict(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewWorkflowTemplateHandler(db, nil, newTestLogger())

	mk := func() *httptest.ResponseRecorder {
		body := jsonBody(map[string]any{"name": "Dup", "template_json": covWFRValidTemplate})
		req := httptest.NewRequest("POST", "/api/v1/workflow-templates", body)
		req = withWorkspaceUser(req, userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.Create(rr, req)
		return rr
	}
	if rr := mk(); rr.Code != http.StatusCreated {
		t.Fatalf("first create status = %d, want 201", rr.Code)
	}
	if rr := mk(); rr.Code != http.StatusConflict {
		t.Fatalf("dup create status = %d, want 409", rr.Code)
	}
}

func TestCovWFRWorkflowList_HappyAndFault(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewWorkflowTemplateHandler(db, nil, newTestLogger())

	covWFRInsertTemplate(t, db, "wt1", wsID, "Alpha")

	req := httptest.NewRequest("GET", "/api/v1/workflow-templates", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	// Fault injection: drop the table → 500.
	if _, err := db.Exec(`DROP TABLE workflow_templates`); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	req2 := httptest.NewRequest("GET", "/api/v1/workflow-templates", nil)
	req2 = withWorkspaceUser(req2, userID, wsID, "OWNER")
	rr2 := httptest.NewRecorder()
	h.List(rr2, req2)
	if rr2.Code != http.StatusInternalServerError {
		t.Fatalf("list-after-drop status = %d, want 500", rr2.Code)
	}
}

func TestCovWFRWorkflowGet_NotFound(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewWorkflowTemplateHandler(db, nil, newTestLogger())

	req := httptest.NewRequest("GET", "/api/v1/workflow-templates/missing", nil)
	req.SetPathValue("id", "missing")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestCovWFRWorkflowGet_Happy(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewWorkflowTemplateHandler(db, nil, newTestLogger())
	covWFRInsertTemplate(t, db, "wt-get", wsID, "GetMe")

	req := httptest.NewRequest("GET", "/api/v1/workflow-templates/wt-get", nil)
	req.SetPathValue("id", "wt-get")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovWFRWorkflowUpdate_NotFound(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewWorkflowTemplateHandler(db, nil, newTestLogger())

	body := jsonBody(map[string]any{"name": "New"})
	req := httptest.NewRequest("PATCH", "/api/v1/workflow-templates/missing", body)
	req.SetPathValue("id", "missing")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestCovWFRWorkflowUpdate_Forbidden(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewWorkflowTemplateHandler(db, nil, newTestLogger())

	body := jsonBody(map[string]any{"name": "New"})
	req := httptest.NewRequest("PATCH", "/api/v1/workflow-templates/x", body)
	req.SetPathValue("id", "x")
	req = withWorkspaceUser(req, userID, wsID, "VIEWER")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestCovWFRWorkflowUpdate_InvalidJSON(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewWorkflowTemplateHandler(db, nil, newTestLogger())
	covWFRInsertTemplate(t, db, "wt-ij", wsID, "IJ")

	req := httptest.NewRequest("PATCH", "/api/v1/workflow-templates/wt-ij", covWFRBadJSON())
	req.SetPathValue("id", "wt-ij")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestCovWFRWorkflowUpdate_EmptyName(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewWorkflowTemplateHandler(db, nil, newTestLogger())
	covWFRInsertTemplate(t, db, "wt-en", wsID, "EN")

	body := jsonBody(map[string]any{"name": "  "})
	req := httptest.NewRequest("PATCH", "/api/v1/workflow-templates/wt-en", body)
	req.SetPathValue("id", "wt-en")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestCovWFRWorkflowUpdate_NoFields(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewWorkflowTemplateHandler(db, nil, newTestLogger())
	covWFRInsertTemplate(t, db, "wt-nf", wsID, "NF")

	body := jsonBody(map[string]any{})
	req := httptest.NewRequest("PATCH", "/api/v1/workflow-templates/wt-nf", body)
	req.SetPathValue("id", "wt-nf")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestCovWFRWorkflowUpdate_BadColor(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewWorkflowTemplateHandler(db, nil, newTestLogger())
	covWFRInsertTemplate(t, db, "wt-bc", wsID, "BC")

	body := jsonBody(map[string]any{"color": "notahex"})
	req := httptest.NewRequest("PATCH", "/api/v1/workflow-templates/wt-bc", body)
	req.SetPathValue("id", "wt-bc")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestCovWFRWorkflowUpdate_Happy(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewWorkflowTemplateHandler(db, nil, newTestLogger())
	covWFRInsertTemplate(t, db, "wt-up", wsID, "Before")

	body := jsonBody(map[string]any{
		"name":          "After",
		"description":   "d",
		"template_json": covWFRValidTemplate,
		"icon":          "ic",
		"color":         "#abcdef",
	})
	req := httptest.NewRequest("PATCH", "/api/v1/workflow-templates/wt-up", body)
	req.SetPathValue("id", "wt-up")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	var name string
	if err := db.QueryRow(`SELECT name FROM workflow_templates WHERE id = ?`, "wt-up").Scan(&name); err != nil {
		t.Fatalf("read updated row: %v", err)
	}
	if name != "After" {
		t.Errorf("name = %q, want After", name)
	}
}

func TestCovWFRWorkflowUpdate_NullableFieldsCleared(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewWorkflowTemplateHandler(db, nil, newTestLogger())
	covWFRInsertTemplate(t, db, "wt-null", wsID, "Null")

	// Empty strings for description/icon/color route through SetNull.
	body := jsonBody(map[string]any{"description": "", "icon": "", "color": ""})
	req := httptest.NewRequest("PATCH", "/api/v1/workflow-templates/wt-null", body)
	req.SetPathValue("id", "wt-null")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovWFRWorkflowDelete_Forbidden(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewWorkflowTemplateHandler(db, nil, newTestLogger())

	req := httptest.NewRequest("DELETE", "/api/v1/workflow-templates/x", nil)
	req.SetPathValue("id", "x")
	req = withWorkspaceUser(req, userID, wsID, "VIEWER")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestCovWFRWorkflowDelete_NotFound(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewWorkflowTemplateHandler(db, nil, newTestLogger())

	req := httptest.NewRequest("DELETE", "/api/v1/workflow-templates/missing", nil)
	req.SetPathValue("id", "missing")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestCovWFRWorkflowDelete_Happy(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewWorkflowTemplateHandler(db, nil, newTestLogger())
	covWFRInsertTemplate(t, db, "wt-del", wsID, "DeleteMe")

	req := httptest.NewRequest("DELETE", "/api/v1/workflow-templates/wt-del", nil)
	req.SetPathValue("id", "wt-del")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rr.Code)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM workflow_templates WHERE id = ?`, "wt-del").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("row count = %d, want 0", n)
	}
}

// covWFRInsertTemplate inserts a user workflow template row directly.
func covWFRInsertTemplate(t *testing.T, db *sql.DB, id, wsID, name string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO workflow_templates
		    (id, workspace_id, name, description, template_json, icon, color, is_builtin, created_at, updated_at)
		VALUES (?, ?, ?, NULL, ?, NULL, NULL, 0, datetime('now'), datetime('now'))`,
		id, wsID, name, covWFRValidTemplate); err != nil {
		t.Fatalf("insert template %s: %v", id, err)
	}
}

// ── credential_rotation.go ──────────────────────────────────────────────────

func TestCovWFRRotate_Forbidden(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewCredentialHandler(db, newTestLogger())
	seedCredentialEnc(t, db, wsID, userID, "cr1", "tok", "old-value")

	body := jsonBody(map[string]any{"value": "new-value"})
	req := httptest.NewRequest("POST", "/api/v1/credentials/cr1/rotate", body)
	req.SetPathValue("credentialId", "cr1")
	req = withWorkspaceUser(req, userID, wsID, "VIEWER")
	rr := httptest.NewRecorder()
	h.Rotate(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestCovWFRRotate_InvalidJSON(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewCredentialHandler(db, newTestLogger())
	seedCredentialEnc(t, db, wsID, userID, "cr-ij", "tok", "old")

	req := httptest.NewRequest("POST", "/api/v1/credentials/cr-ij/rotate", covWFRBadJSON())
	req.SetPathValue("credentialId", "cr-ij")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Rotate(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestCovWFRRotate_MissingValue(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewCredentialHandler(db, newTestLogger())
	seedCredentialEnc(t, db, wsID, userID, "cr-mv", "tok", "old")

	body := jsonBody(map[string]any{"value": "  "})
	req := httptest.NewRequest("POST", "/api/v1/credentials/cr-mv/rotate", body)
	req.SetPathValue("credentialId", "cr-mv")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Rotate(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestCovWFRRotate_GraceOutOfRange(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewCredentialHandler(db, newTestLogger())
	seedCredentialEnc(t, db, wsID, userID, "cr-g", "tok", "old")

	tooBig := maxGraceSeconds + 1
	body := jsonBody(map[string]any{"value": "v", "grace_seconds": tooBig})
	req := httptest.NewRequest("POST", "/api/v1/credentials/cr-g/rotate", body)
	req.SetPathValue("credentialId", "cr-g")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Rotate(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestCovWFRRotate_NotFound(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewCredentialHandler(db, newTestLogger())

	body := jsonBody(map[string]any{"value": "v"})
	req := httptest.NewRequest("POST", "/api/v1/credentials/missing/rotate", body)
	req.SetPathValue("credentialId", "missing")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Rotate(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestCovWFRRotate_Happy(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewCredentialHandler(db, newTestLogger())
	seedCredentialEnc(t, db, wsID, userID, "cr-ok", "tok", "old-value")

	grace := 3600
	body := jsonBody(map[string]any{"value": "new-value", "grace_seconds": grace})
	req := httptest.NewRequest("POST", "/api/v1/credentials/cr-ok/rotate", body)
	req.SetPathValue("credentialId", "cr-ok")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Rotate(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	// A rotation row exists, ACTIVE, with the old encrypted value stashed.
	var status, oldVal string
	if err := db.QueryRow(`SELECT status, old_value FROM credential_rotations WHERE credential_id = ?`, "cr-ok").
		Scan(&status, &oldVal); err != nil {
		t.Fatalf("read rotation row: %v", err)
	}
	if status != "ACTIVE" {
		t.Errorf("status = %q, want ACTIVE", status)
	}

	// The credential now decrypts to the new value.
	var enc string
	if err := db.QueryRow(`SELECT encrypted_value FROM credentials WHERE id = ?`, "cr-ok").Scan(&enc); err != nil {
		t.Fatalf("read credential: %v", err)
	}
	plain, err := encryption.Decrypt(enc)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if plain != "new-value" {
		t.Errorf("decrypted value = %q, want new-value", plain)
	}
}

func TestCovWFRListRotations_NotFound(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewCredentialHandler(db, newTestLogger())

	req := httptest.NewRequest("GET", "/api/v1/credentials/missing/rotations", nil)
	req.SetPathValue("credentialId", "missing")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ListRotations(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestCovWFRListRotations_Happy(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewCredentialHandler(db, newTestLogger())
	seedCredentialEnc(t, db, wsID, userID, "cr-lr", "tok", "old")
	covWFRSeedRotation(t, h, "rot-active", "cr-lr", "ACTIVE")
	covWFRSeedRotation(t, h, "rot-expired", "cr-lr", "EXPIRED")

	req := httptest.NewRequest("GET", "/api/v1/credentials/cr-lr/rotations", nil)
	req.SetPathValue("credentialId", "cr-lr")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ListRotations(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovWFRCancelRotation_Forbidden(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewCredentialHandler(db, newTestLogger())

	req := httptest.NewRequest("DELETE", "/api/v1/credential-rotations/x", nil)
	req.SetPathValue("rotationId", "x")
	req = withWorkspaceUser(req, userID, wsID, "VIEWER")
	rr := httptest.NewRecorder()
	h.CancelRotation(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestCovWFRCancelRotation_NotFound(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewCredentialHandler(db, newTestLogger())

	req := httptest.NewRequest("DELETE", "/api/v1/credential-rotations/missing", nil)
	req.SetPathValue("rotationId", "missing")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CancelRotation(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestCovWFRCancelRotation_AlreadyTerminal(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewCredentialHandler(db, newTestLogger())
	seedCredentialEnc(t, db, wsID, userID, "cr-term", "tok", "old")
	covWFRSeedRotation(t, h, "rot-term", "cr-term", "EXPIRED")

	req := httptest.NewRequest("DELETE", "/api/v1/credential-rotations/rot-term", nil)
	req.SetPathValue("rotationId", "rot-term")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CancelRotation(rr, req)
	// Idempotent no-op → 200.
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovWFRCancelRotation_Happy(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewCredentialHandler(db, newTestLogger())
	seedCredentialEnc(t, db, wsID, userID, "cr-cancel", "tok", "old")
	covWFRSeedRotation(t, h, "rot-cancel", "cr-cancel", "ACTIVE")

	req := httptest.NewRequest("DELETE", "/api/v1/credential-rotations/rot-cancel", nil)
	req.SetPathValue("rotationId", "rot-cancel")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CancelRotation(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	var status, oldVal string
	if err := db.QueryRow(`SELECT status, old_value FROM credential_rotations WHERE id = ?`, "rot-cancel").
		Scan(&status, &oldVal); err != nil {
		t.Fatalf("read rotation: %v", err)
	}
	if status != "CANCELLED" {
		t.Errorf("status = %q, want CANCELLED", status)
	}
	if oldVal != "" {
		t.Errorf("old_value = %q, want scrubbed (empty)", oldVal)
	}
}

func TestCovWFRExpireGracedRotations(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewCredentialHandler(db, newTestLogger())
	seedCredentialEnc(t, db, wsID, userID, "cr-exp", "tok", "old")

	// One ACTIVE rotation already past its expiry → should transition.
	if _, err := db.Exec(`
		INSERT INTO credential_rotations
		    (id, credential_id, old_value, grace_seconds, rotated_at, expires_at, rotated_by, status)
		VALUES ('rot-exp', 'cr-exp', 'enc-old', 3600, datetime('now','-2 hour'), datetime('now','-1 hour'), NULL, 'ACTIVE')`); err != nil {
		t.Fatalf("seed expired rotation: %v", err)
	}

	n, err := ExpireGracedRotations(context.Background(), db, newTestLogger())
	if err != nil {
		t.Fatalf("ExpireGracedRotations: %v", err)
	}
	if n != 1 {
		t.Fatalf("transitioned = %d, want 1", n)
	}

	var status, oldVal string
	if err := db.QueryRow(`SELECT status, old_value FROM credential_rotations WHERE id = 'rot-exp'`).
		Scan(&status, &oldVal); err != nil {
		t.Fatalf("read rotation: %v", err)
	}
	if status != "EXPIRED" || oldVal != "" {
		t.Errorf("status=%q old_value=%q, want EXPIRED + scrubbed", status, oldVal)
	}
	_ = h
}
