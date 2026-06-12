package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// covWTJSONString marshals a raw JSON snippet into a JSON string literal
// so it can be embedded as the template_json field value.
func covWTJSONString(raw string) string {
	b, _ := json.Marshal(raw)
	return string(b)
}

// workflow_templates_handler_cov_test.go covers the remaining
// WorkflowTemplateHandler branches: DB errors on insert/update/delete/
// get, the unique-name 409s, and isUniqueConstraintErr's nil arm.
// Prefix covWT.

const covWTValidStages = `[{"name":"Open","type":"open","position":1},{"name":"Done","type":"completed","position":2}]`

func covWTReq(method, id, wsID, body string) *http.Request {
	req := httptest.NewRequest(method, "/api/v1/workflow-templates", bytes.NewBufferString(body))
	if id != "" {
		req.SetPathValue("id", id)
	}
	return req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
}

func covWTCreate(t *testing.T, h *WorkflowTemplateHandler, wsID, name string) string {
	t.Helper()
	rr := httptest.NewRecorder()
	h.Create(rr, covWTReq("POST", "", wsID, `{"name":"`+name+`","template_json":`+covWTJSONString(covWTValidStages)+`}`))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create %s: status = %d, body=%s", name, rr.Code, rr.Body.String())
	}
	var resp workflowTemplateResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	return resp.ID
}

func TestCovWT_IsUniqueConstraintErr(t *testing.T) {
	if isUniqueConstraintErr(nil) {
		t.Errorf("nil must not be unique-constraint")
	}
	if !isUniqueConstraintErr(errors.New("UNIQUE constraint failed: workflow_templates.name")) {
		t.Errorf("UNIQUE message must match")
	}
}

func TestCovWT_Create_DuplicateName409(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewWorkflowTemplateHandler(db, nil, newTestLogger())
	covWTCreate(t, h, wsID, "covwt-dup")

	rr := httptest.NewRecorder()
	h.Create(rr, covWTReq("POST", "", wsID, `{"name":"covwt-dup","template_json":`+covWTJSONString(covWTValidStages)+`}`))
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovWT_Create_InsertDBError(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewWorkflowTemplateHandler(db, nil, newTestLogger())
	execOrFatal(t, db, `CREATE TRIGGER covwt_fail_ins BEFORE INSERT ON workflow_templates BEGIN SELECT RAISE(ABORT, 'covwt boom'); END`)

	rr := httptest.NewRecorder()
	h.Create(rr, covWTReq("POST", "", wsID, `{"name":"covwt-x","template_json":`+covWTJSONString(covWTValidStages)+`}`))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovWT_Get_DBError(t *testing.T) {
	db := setupTestDB(t)
	h := NewWorkflowTemplateHandler(db, nil, newTestLogger())
	db.Close()
	rr := httptest.NewRecorder()
	h.Get(rr, covWTReq("GET", "wt-x", "ws", ""))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}

func TestCovWT_List_DBError(t *testing.T) {
	db := setupTestDB(t)
	h := NewWorkflowTemplateHandler(db, nil, newTestLogger())
	db.Close()
	rr := httptest.NewRecorder()
	h.List(rr, covWTReq("GET", "", "ws", ""))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}

func TestCovWT_Update_DuplicateName409AndDBError(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewWorkflowTemplateHandler(db, nil, newTestLogger())
	covWTCreate(t, h, wsID, "covwt-a")
	idB := covWTCreate(t, h, wsID, "covwt-b")

	// Renaming B to A collides with the UNIQUE(workspace_id, name).
	rr := httptest.NewRecorder()
	h.Update(rr, covWTReq("PATCH", idB, wsID, `{"name":"covwt-a"}`))
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}

	// Generic update failure -> 500.
	execOrFatal(t, db, `CREATE TRIGGER covwt_fail_upd BEFORE UPDATE ON workflow_templates BEGIN SELECT RAISE(ABORT, 'covwt boom'); END`)
	rr = httptest.NewRecorder()
	h.Update(rr, covWTReq("PATCH", idB, wsID, `{"name":"covwt-c"}`))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovWT_Update_GetForUpdateDBError(t *testing.T) {
	db := setupTestDB(t)
	h := NewWorkflowTemplateHandler(db, nil, newTestLogger())
	db.Close()
	rr := httptest.NewRecorder()
	h.Update(rr, covWTReq("PATCH", "wt-x", "ws", `{"name":"n"}`))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}

func TestCovWT_Delete_DBError(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewWorkflowTemplateHandler(db, nil, newTestLogger())
	id := covWTCreate(t, h, wsID, "covwt-del")
	execOrFatal(t, db, `CREATE TRIGGER covwt_fail_del BEFORE DELETE ON workflow_templates BEGIN SELECT RAISE(ABORT, 'covwt boom'); END`)

	rr := httptest.NewRecorder()
	h.Delete(rr, covWTReq("DELETE", id, wsID, ""))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}
