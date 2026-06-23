package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// issue_handler_create_cov_test.go covers the remaining IssueHandler.Create
// branches: crew-lookup 500, prefix derivation from slug, counter/lead/
// routine/parent 500s, the no-lead 400, routine_id normalisation, and the
// insert failure path (via an ABORT trigger). Helpers prefixed covIC.

func covICPost(t *testing.T, h *IssueHandler, userID, wsID, crewID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/crews/"+crewID+"/issues", bytes.NewBufferString(body))
	req.SetPathValue("crewId", crewID)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	return rr
}

func covICHandler(t *testing.T) (*IssueHandler, *sql.DB, string, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID, wsID, crewID, _, _ := seedIssueFixtures(t, db)
	h := NewIssueHandler(db, nil, nil, newTestLogger())
	return h, db, userID, wsID, crewID
}

func TestCovIC_Create_CrewLookupDBError(t *testing.T) {
	h, db, userID, wsID, crewID := covICHandler(t)
	// Replace crews with an incompatible shape so the SELECT fails with a
	// real error (not ErrNoRows) after BeginTx succeeded.
	execOrFatal(t, db, `ALTER TABLE crews RENAME TO covic_crews_bak`)
	rr := covICPost(t, h, userID, wsID, crewID, `{"title":"x"}`)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovIC_Create_PrefixDerivedFromSlug(t *testing.T) {
	h, db, userID, wsID, _ := covICHandler(t)
	// Crew without issue_prefix, slug >= 3 chars -> first 3 upper-cased.
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('covic-c1', ?, 'Long', 'platform')`, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role) VALUES ('covic-lead1', ?, 'covic-c1', 'L', 'covic-l1', 'LEAD')`, wsID)
	rr := covICPost(t, h, userID, wsID, "covic-c1", `{"title":"derived prefix"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Identifier *string `json:"identifier"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Identifier == nil || !strings.HasPrefix(*resp.Identifier, "PLA-") {
		t.Errorf("identifier = %v, want PLA-*", resp.Identifier)
	}

	// Short slug (< 3 chars) -> whole slug upper-cased.
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('covic-c2', ?, 'Short', 'qa')`, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role) VALUES ('covic-lead2', ?, 'covic-c2', 'L', 'covic-l2', 'LEAD')`, wsID)
	rr = covICPost(t, h, userID, wsID, "covic-c2", `{"title":"short prefix"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Identifier == nil || !strings.HasPrefix(*resp.Identifier, "QA-") {
		t.Errorf("identifier = %v, want QA-*", resp.Identifier)
	}
}

func TestCovIC_Create_CounterUpsertDBError(t *testing.T) {
	h, db, userID, wsID, crewID := covICHandler(t)
	execOrFatal(t, db, `ALTER TABLE issue_counters RENAME TO covic_counters_bak`)
	rr := covICPost(t, h, userID, wsID, crewID, `{"title":"x"}`)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovIC_Create_NoLeadAgent400(t *testing.T) {
	h, db, userID, wsID, _ := covICHandler(t)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('covic-noleads', ?, 'NL', 'noleads')`, wsID)
	rr := covICPost(t, h, userID, wsID, "covic-noleads", `{"title":"x"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "no lead agent") {
		t.Errorf("body = %s, want no-lead message", rr.Body.String())
	}
}

func TestCovIC_Create_LeadLookupDBError(t *testing.T) {
	h, db, userID, wsID, crewID := covICHandler(t)
	execOrFatal(t, db, `ALTER TABLE agents RENAME TO covic_agents_bak`)
	rr := covICPost(t, h, userID, wsID, crewID, `{"title":"x"}`)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovIC_Create_EmptyRoutineIDNormalised(t *testing.T) {
	h, _, userID, wsID, crewID := covICHandler(t)
	// routine_id:"" must behave like no routine at all.
	rr := covICPost(t, h, userID, wsID, crewID, `{"title":"x","routine_id":""}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovIC_Create_RoutineValidateDBError(t *testing.T) {
	h, db, userID, wsID, crewID := covICHandler(t)
	execOrFatal(t, db, `ALTER TABLE pipelines RENAME TO covic_pipelines_bak`)
	rr := covICPost(t, h, userID, wsID, crewID, `{"title":"x","routine_id":"r1"}`)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovIC_Create_RoutineBoundWithDefaultInputs(t *testing.T) {
	h, db, userID, wsID, crewID := covICHandler(t)
	pipeID := seedTestPipeline(t, h, wsID, "covic-p")
	// Valid routine_id, no routine_inputs -> defaults to "{}".
	rr := covICPost(t, h, userID, wsID, crewID, `{"title":"x","routine_id":"`+pipeID+`"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var routineID, inputs sql.NullString
	if err := db.QueryRow(`SELECT routine_id, routine_inputs_json FROM missions WHERE id = ?`, resp.ID).Scan(&routineID, &inputs); err != nil {
		t.Fatalf("read mission: %v", err)
	}
	if routineID.String != pipeID || inputs.String != "{}" {
		t.Errorf("routine = %q inputs = %q, want %s / {}", routineID.String, inputs.String, pipeID)
	}
}

func TestCovIC_Create_ParentValidateDBError(t *testing.T) {
	h, db, userID, wsID, crewID := covICHandler(t)
	// The parent check SELECTs from missions; an ABORT trigger cannot fire
	// on SELECT, so break the query instead by renaming the table. The
	// handler reaches the parent check before the INSERT.
	execOrFatal(t, db, `ALTER TABLE missions RENAME TO covic_missions_bak`)
	rr := covICPost(t, h, userID, wsID, crewID, `{"title":"x","parent_issue_id":"p1"}`)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovIC_Create_InsertDBError(t *testing.T) {
	h, db, userID, wsID, crewID := covICHandler(t)
	execOrFatal(t, db, `CREATE TRIGGER covic_fail_insert BEFORE INSERT ON missions BEGIN SELECT RAISE(ABORT, 'covic boom'); END`)
	rr := covICPost(t, h, userID, wsID, crewID, `{"title":"x"}`)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovIC_Create_LabelInsertDBError(t *testing.T) {
	h, db, userID, wsID, crewID := covICHandler(t)
	execOrFatal(t, db, `CREATE TRIGGER covic_fail_label BEFORE INSERT ON mission_labels BEGIN SELECT RAISE(ABORT, 'covic label boom'); END`)
	rr := covICPost(t, h, userID, wsID, crewID, `{"title":"x","labels":["lbl-1"]}`)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}
