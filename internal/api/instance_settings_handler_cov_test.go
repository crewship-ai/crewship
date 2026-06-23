package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// instance_settings_handler_cov_test.go covers the remaining
// InstanceSettingsHandler branches: missing keys, DB errors on
// list/get/upsert/delete. Helpers prefixed covIS.

func covISReq(method, key, role, body string) *http.Request {
	req := httptest.NewRequest(method, "/api/v1/instance/settings", bytes.NewBufferString(body))
	if key != "" {
		req.SetPathValue("key", key)
	}
	return req.WithContext(withWorkspace(req.Context(), "ws", role))
}

func TestCovIS_List_DBError(t *testing.T) {
	db := setupTestDB(t)
	h := NewInstanceSettingsHandler(db, nil, newTestLogger())
	db.Close()
	rr := httptest.NewRecorder()
	h.List(rr, covISReq("GET", "", "OWNER", ""))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}

func TestCovIS_Get_MissingKey(t *testing.T) {
	db := setupTestDB(t)
	h := NewInstanceSettingsHandler(db, nil, newTestLogger())
	rr := httptest.NewRecorder()
	h.Get(rr, covISReq("GET", "", "OWNER", ""))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestCovIS_Get_DBError(t *testing.T) {
	db := setupTestDB(t)
	h := NewInstanceSettingsHandler(db, nil, newTestLogger())
	db.Close()
	rr := httptest.NewRecorder()
	h.Get(rr, covISReq("GET", "some.key", "OWNER", ""))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}

func TestCovIS_Put_MissingKey(t *testing.T) {
	db := setupTestDB(t)
	h := NewInstanceSettingsHandler(db, nil, newTestLogger())
	rr := httptest.NewRecorder()
	h.Put(rr, covISReq("PUT", "", "OWNER", `{"value":"x"}`))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestCovIS_Put_UpsertDBError(t *testing.T) {
	db := setupTestDB(t)
	h := NewInstanceSettingsHandler(db, nil, newTestLogger())
	execOrFatal(t, db, `CREATE TRIGGER covis_fail_upsert BEFORE INSERT ON app_settings BEGIN SELECT RAISE(ABORT, 'covis boom'); END`)
	rr := httptest.NewRecorder()
	h.Put(rr, covISReq("PUT", "banner.text", "OWNER", `{"value":"hello"}`))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovIS_Delete_MissingKey(t *testing.T) {
	db := setupTestDB(t)
	h := NewInstanceSettingsHandler(db, nil, newTestLogger())
	rr := httptest.NewRecorder()
	h.Delete(rr, covISReq("DELETE", "", "OWNER", ""))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestCovIS_Delete_DBError(t *testing.T) {
	db := setupTestDB(t)
	h := NewInstanceSettingsHandler(db, nil, newTestLogger())
	db.Close()
	rr := httptest.NewRecorder()
	h.Delete(rr, covISReq("DELETE", "banner.text", "OWNER", ""))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}
