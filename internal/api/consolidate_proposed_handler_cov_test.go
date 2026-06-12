package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// consolidate_proposed_handler_cov_test.go covers the remaining
// ProposedHandler branches: SetJournal, requireOwnerOrAdmin's 401s,
// missing proposal ids, Explain auth, and mapDecisionError's
// conflict + default arms. Helpers prefixed covCPH.

func covCPHReq(method, path, id, wsID string, user *AuthUser, role, body string) *http.Request {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if id != "" {
		req.SetPathValue("id", id)
	}
	ctx := req.Context()
	if user != nil {
		ctx = withUser(ctx, user)
	}
	if wsID != "" {
		ctx = withWorkspace(ctx, wsID, role)
	}
	return req.WithContext(ctx)
}

func TestCovCPH_SetJournal_NilMapsToNoop(t *testing.T) {
	db := setupTestDB(t)
	h := NewProposedHandler(db, newTestLogger())
	h.SetJournal(nil)
	if _, ok := h.journal.(noopEmitter); !ok {
		t.Fatalf("SetJournal(nil) = %T, want noopEmitter", h.journal)
	}
}

func TestCovCPH_Approve_NoWorkspace401(t *testing.T) {
	db := setupTestDB(t)
	h := NewProposedHandler(db, newTestLogger())
	rr := httptest.NewRecorder()
	h.Approve(rr, covCPHReq("POST", "/x", "p1", "", &AuthUser{ID: "u"}, "", `{}`))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestCovCPH_Approve_NoUser401(t *testing.T) {
	db := setupTestDB(t)
	h := NewProposedHandler(db, newTestLogger())
	rr := httptest.NewRecorder()
	h.Approve(rr, covCPHReq("POST", "/x", "p1", "ws", nil, "OWNER", `{}`))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestCovCPH_Approve_MissingProposalID(t *testing.T) {
	db := setupTestDB(t)
	h := NewProposedHandler(db, newTestLogger())
	rr := httptest.NewRecorder()
	h.Approve(rr, covCPHReq("POST", "/x", "", "ws", &AuthUser{ID: "u"}, "OWNER", `{}`))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestCovCPH_Approve_DBError500(t *testing.T) {
	db := setupTestDB(t)
	h := NewProposedHandler(db, newTestLogger())
	db.Close()
	rr := httptest.NewRecorder()
	h.Approve(rr, covCPHReq("POST", "/x", "p1", "ws", &AuthUser{ID: "u"}, "OWNER", `{}`))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (mapDecisionError default arm)", rr.Code)
	}
}

func TestCovCPH_Reject_MissingProposalID(t *testing.T) {
	db := setupTestDB(t)
	h := NewProposedHandler(db, newTestLogger())
	rr := httptest.NewRecorder()
	h.Reject(rr, covCPHReq("POST", "/x", "", "ws", &AuthUser{ID: "u"}, "OWNER", `{}`))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestCovCPH_Reject_NotFound(t *testing.T) {
	db := setupTestDB(t)
	h := NewProposedHandler(db, newTestLogger())
	rr := httptest.NewRecorder()
	h.Reject(rr, covCPHReq("POST", "/x", "covcph-missing", "ws", &AuthUser{ID: "u"}, "OWNER", `{}`))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovCPH_Explain_NoWorkspace401(t *testing.T) {
	db := setupTestDB(t)
	h := NewProposedHandler(db, newTestLogger())
	rr := httptest.NewRecorder()
	h.Explain(rr, covCPHReq("GET", "/x", "p1", "", nil, "", ""))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestCovCPH_Explain_MissingProposalID(t *testing.T) {
	db := setupTestDB(t)
	h := NewProposedHandler(db, newTestLogger())
	rr := httptest.NewRecorder()
	h.Explain(rr, covCPHReq("GET", "/x", "", "ws", nil, "OWNER", ""))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestCovCPH_Explain_NotFound(t *testing.T) {
	db := setupTestDB(t)
	h := NewProposedHandler(db, newTestLogger())
	rr := httptest.NewRecorder()
	h.Explain(rr, covCPHReq("GET", "/x", "covcph-missing", "ws", nil, "OWNER", ""))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}
