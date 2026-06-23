package api

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/policy"
)

// crew_policy_cov_test.go covers the remaining CrewPolicyHandler
// branches: SetJournal, missing crew ids, lookup/update/list DB errors
// and the journal-emit warning path. Helpers prefixed covCP.

// covCPFailingEmitter always errors, to exercise the emit-warn branch.
type covCPFailingEmitter struct{}

func (covCPFailingEmitter) Emit(_ context.Context, _ journal.Entry) (string, error) {
	return "", errors.New("covcp emit boom")
}
func (covCPFailingEmitter) Flush(_ context.Context) error { return nil }

func covCPHandler(t *testing.T) (*CrewPolicyHandler, *sql.DB, string, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := "covcp-crew"
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'P', 'covcp-p')`, crewID, wsID)
	h := NewCrewPolicyHandler(db, policy.NewResolver(db), newTestLogger())
	return h, db, userID, wsID, crewID
}

func covCPReq(method, crewID, wsID, userID, body string) *http.Request {
	req := httptest.NewRequest(method, "/api/v1/crews/policy", bytes.NewBufferString(body))
	if crewID != "" {
		req.SetPathValue("crewId", crewID)
	}
	ctx := req.Context()
	if userID != "" {
		ctx = withUser(ctx, &AuthUser{ID: userID})
	}
	ctx = withWorkspace(ctx, wsID, "OWNER")
	return req.WithContext(ctx)
}

func TestCovCP_SetJournal(t *testing.T) {
	h, _, _, _, _ := covCPHandler(t)
	h.SetJournal(nil)
	if _, ok := h.journal.(noopEmitter); !ok {
		t.Fatalf("SetJournal(nil) = %T, want noopEmitter", h.journal)
	}
	h.SetJournal(covCPFailingEmitter{})
	if _, ok := h.journal.(covCPFailingEmitter); !ok {
		t.Fatalf("SetJournal kept %T", h.journal)
	}
}

func TestCovCP_Get_MissingCrewID(t *testing.T) {
	h, _, userID, wsID, _ := covCPHandler(t)
	rr := httptest.NewRecorder()
	h.Get(rr, covCPReq("GET", "", wsID, userID, ""))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestCovCP_Get_NotFound(t *testing.T) {
	h, _, userID, wsID, _ := covCPHandler(t)
	rr := httptest.NewRecorder()
	h.Get(rr, covCPReq("GET", "covcp-missing", wsID, userID, ""))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestCovCP_Get_LookupDBError(t *testing.T) {
	h, db, userID, wsID, crewID := covCPHandler(t)
	db.Close()
	rr := httptest.NewRecorder()
	h.Get(rr, covCPReq("GET", crewID, wsID, userID, ""))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}

func TestCovCP_Put_MissingCrewID(t *testing.T) {
	h, _, userID, wsID, _ := covCPHandler(t)
	rr := httptest.NewRecorder()
	h.Put(rr, covCPReq("PUT", "", wsID, userID, `{}`))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestCovCP_Put_InvalidJSON(t *testing.T) {
	h, _, userID, wsID, crewID := covCPHandler(t)
	rr := httptest.NewRecorder()
	h.Put(rr, covCPReq("PUT", crewID, wsID, userID, `{nope`))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestCovCP_Put_UpdateDBError(t *testing.T) {
	h, db, userID, wsID, crewID := covCPHandler(t)
	// Make the UPDATE fail while leaving body validation intact.
	execOrFatal(t, db, `CREATE TRIGGER covcp_fail_update BEFORE UPDATE ON crews BEGIN SELECT RAISE(ABORT, 'covcp boom'); END`)
	rr := httptest.NewRecorder()
	h.Put(rr, covCPReq("PUT", crewID, wsID, userID, `{"autonomy_level":"strict","behavior_mode":"block"}`))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovCP_Put_CrewNotFound(t *testing.T) {
	h, _, userID, wsID, _ := covCPHandler(t)
	rr := httptest.NewRecorder()
	h.Put(rr, covCPReq("PUT", "covcp-nope", wsID, userID, `{"autonomy_level":"strict","behavior_mode":"block"}`))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovCP_Put_JournalEmitFailureIsNonFatal(t *testing.T) {
	h, _, userID, wsID, crewID := covCPHandler(t)
	h.SetJournal(covCPFailingEmitter{})
	rr := httptest.NewRecorder()
	h.Put(rr, covCPReq("PUT", crewID, wsID, userID, `{"autonomy_level":"strict","behavior_mode":"block","reason":"lockdown"}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s (emit failure must not fail the request)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"autonomy_level":"strict"`) {
		t.Errorf("body = %s", rr.Body.String())
	}
}

func TestCovCP_List_QueryDBError(t *testing.T) {
	h, db, userID, wsID, _ := covCPHandler(t)
	db.Close()
	rr := httptest.NewRecorder()
	h.List(rr, covCPReq("GET", "", wsID, userID, ""))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}
