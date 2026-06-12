package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// admin_cov_test.go covers the remaining AdminHandler branches: DB
// errors on Stats/ListUsers/ListWorkspaces and the nil->[] result
// normalisation. Helpers prefixed covAD.

func covADReq(role, wsID string) *http.Request {
	req := httptest.NewRequest("GET", "/api/v1/admin/x", nil)
	return req.WithContext(withWorkspace(req.Context(), wsID, role))
}

func TestCovAD_Stats_DBError(t *testing.T) {
	db := setupTestDB(t)
	h := NewAdminHandler(db, newTestLogger())
	db.Close()
	rr := httptest.NewRecorder()
	h.Stats(rr, covADReq("OWNER", "ws"))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}

func TestCovAD_ListUsers_DBError(t *testing.T) {
	db := setupTestDB(t)
	h := NewAdminHandler(db, newTestLogger())
	db.Close()
	rr := httptest.NewRecorder()
	h.ListUsers(rr, covADReq("OWNER", "ws"))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}

func TestCovAD_ListUsers_EmptyIsArray(t *testing.T) {
	db := setupTestDB(t)
	h := NewAdminHandler(db, newTestLogger())
	rr := httptest.NewRecorder()
	h.ListUsers(rr, covADReq("OWNER", "ws-without-members"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var out []json.RawMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode (must be [] not null): %v; body=%s", err, rr.Body.String())
	}
	if len(out) != 0 {
		t.Errorf("len = %d, want 0", len(out))
	}
}

func TestCovAD_ListWorkspaces_DBError(t *testing.T) {
	db := setupTestDB(t)
	h := NewAdminHandler(db, newTestLogger())
	db.Close()
	rr := httptest.NewRecorder()
	h.ListWorkspaces(rr, covADReq("OWNER", "ws"))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}

func TestCovAD_ListWorkspaces_EmptyIsArray(t *testing.T) {
	db := setupTestDB(t)
	h := NewAdminHandler(db, newTestLogger())
	rr := httptest.NewRecorder()
	h.ListWorkspaces(rr, covADReq("OWNER", "ws-that-does-not-exist"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Body.String(); got == "null\n" || got == "null" {
		t.Errorf("body = %q, want JSON array", got)
	}
	var out []json.RawMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("len = %d, want 0", len(out))
	}
}
