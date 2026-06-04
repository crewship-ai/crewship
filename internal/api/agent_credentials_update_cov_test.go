package api

// Coverage tests for the still-uncovered branches of
// agent_credentials.go (the internal-server-error paths reached when the
// underlying *sql.DB is closed) and agents_update.go (the scheduleUpdater
// callback branches + the update 500 path).
//
// Patterns mirror agent_credentials_test.go and agents_setters_status_test.go:
// table-free, sub-second, in-memory SQLite via setupTestDB, direct handler
// invocation with httptest. New helpers are prefixed covAC; all test funcs
// are prefixed TestCovAC. Existing symbols (fakeScheduler, seedAgentRow,
// withWorkspaceUser, seedCredentialEnc, …) are reused, never redefined.
//
// Skipped (not reachable from an api-package unit test): the scheduler
// daemon wiring behind ScheduleUpdater, and any Docker/provisioner branch
// — neither is touched by these handlers.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// covACRequest builds an OWNER-scoped request with the agentId path value
// already populated — the single most repeated bit of boilerplate here.
func covACRequest(t *testing.T, method, target, agentID, wsID, userID string, body []byte) *http.Request {
	t.Helper()
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, target, bytes.NewReader(body))
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	r.SetPathValue("agentId", agentID)
	return withWorkspaceUser(r, userID, wsID, "OWNER")
}

// ---------------------------------------------------------------------------
// agent_credentials.go — 500 paths (db closed)
// ---------------------------------------------------------------------------

func TestCovAC_ListCredentials_DBClosed500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedAgentRow(t, db, "ag-cov-1", wsID, "", "Cov", "cov", "AGENT")
	h := NewAgentHandler(db, newTestLogger())

	db.Close() // force agentExists() to error → 500

	req := covACRequest(t, "GET", "/api/v1/agents/ag-cov-1/credentials", "ag-cov-1", wsID, userID, nil)
	rr := httptest.NewRecorder()
	h.ListCredentials(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500; body: %s", rr.Code, rr.Body.String())
	}
}

func TestCovAC_AddCredential_DBClosed500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedAgentRow(t, db, "ag-cov-2", wsID, "", "Cov", "cov2", "AGENT")
	h := NewAgentHandler(db, newTestLogger())

	db.Close() // force agentExists() to error → 500 (before the JSON parse)

	body := []byte(`{"credential_id":"c","env_var_name":"X"}`)
	req := covACRequest(t, "POST", "/api/v1/agents/ag-cov-2/credentials", "ag-cov-2", wsID, userID, body)
	rr := httptest.NewRecorder()
	h.AddCredential(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500; body: %s", rr.Code, rr.Body.String())
	}
}

// AddCredential: agentExists() and the bad-request guard both pass, then
// credentialExists() runs. Dropping the credentials table (DB still open)
// lets the agent check succeed while the credential read errors → exercises
// the second 500 branch in AddCredential without a racy mid-flight close.
func TestCovAC_AddCredential_CredCheck500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedAgentRow(t, db, "ag-cov-3", wsID, "", "Cov", "cov3", "AGENT")
	h := NewAgentHandler(db, newTestLogger())

	// Drop the credentials table so agentExists() succeeds but
	// credentialExists() errors → the second 500 branch in AddCredential.
	if _, err := db.Exec(`DROP TABLE credentials`); err != nil {
		t.Fatalf("drop credentials: %v", err)
	}

	body := []byte(`{"credential_id":"c","env_var_name":"X"}`)
	req := covACRequest(t, "POST", "/api/v1/agents/ag-cov-3/credentials", "ag-cov-3", wsID, userID, body)
	rr := httptest.NewRecorder()
	h.AddCredential(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500; body: %s", rr.Code, rr.Body.String())
	}
}

func TestCovAC_RemoveCredential_DBClosed500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedAgentRow(t, db, "ag-cov-4", wsID, "", "Cov", "cov4", "AGENT")
	h := NewAgentHandler(db, newTestLogger())

	db.Close() // force the DELETE ExecContext() to error → 500

	req := covACRequest(t, "DELETE", "/api/v1/agents/ag-cov-4/credentials/x", "ag-cov-4", wsID, userID, nil)
	req.SetPathValue("assignmentId", "x")
	rr := httptest.NewRecorder()
	h.RemoveCredential(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500; body: %s", rr.Code, rr.Body.String())
	}
}

// ---------------------------------------------------------------------------
// agent_credentials.go — list scan/iteration happy path with a real row
// (covers the rows.Next/Scan body, exercised end-to-end via the handler).
// ---------------------------------------------------------------------------

func TestCovAC_ListCredentials_PopulatedRow(t *testing.T) {
	db := setupTestDB(t)
	setTestEncryptionKeyParallelSafe(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedAgentRow(t, db, "ag-cov-5", wsID, "", "Cov", "cov5", "AGENT")
	seedCredentialEnc(t, db, wsID, userID, "cred-cov-5", "tok", "secret")
	if _, err := db.Exec(`INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
		VALUES ('ac-cov-5', 'ag-cov-5', 'cred-cov-5', 'GH_TOKEN', 3, datetime('now'))`); err != nil {
		t.Fatalf("seed agent_credentials: %v", err)
	}
	h := NewAgentHandler(db, newTestLogger())

	req := covACRequest(t, "GET", "/api/v1/agents/ag-cov-5/credentials", "ag-cov-5", wsID, userID, nil)
	rr := httptest.NewRecorder()
	h.ListCredentials(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte(`"env_var_name":"GH_TOKEN"`)) ||
		!bytes.Contains(rr.Body.Bytes(), []byte(`"priority":3`)) {
		t.Errorf("response missing assigned-credential fields: %s", rr.Body.String())
	}
}

// ---------------------------------------------------------------------------
// agents_update.go — scheduleUpdater callback branches
// ---------------------------------------------------------------------------

// branch A: schedule_cron present + schedule_enabled present → callback fires
// with the body-provided enabled value (no DB read of schedule_enabled).
func TestCovAC_Update_ScheduleCronWithEnabled(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedAgentRow(t, db, "ag-cov-6", wsID, "", "Sched", "sched6", "AGENT")
	h := NewAgentHandler(db, newTestLogger())
	sched := &fakeScheduler{}
	h.SetScheduler(sched)

	body := []byte(`{"schedule_cron":"*/5 * * * *","schedule_prompt":"ping","schedule_enabled":true}`)
	req := covACRequest(t, "PATCH", "/api/v1/agents/ag-cov-6?workspace_id="+wsID, "ag-cov-6", wsID, userID, body)
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	if sched.calls != 1 || sched.gotAgentID != "ag-cov-6" || !sched.gotEnabled {
		t.Errorf("scheduler = %+v, want 1 call agentID=ag-cov-6 enabled=true", sched)
	}
}

// branch A': schedule_cron present but schedule_enabled absent → callback
// reads schedule_enabled from the DB (seed it as 1 so enabled resolves true).
func TestCovAC_Update_ScheduleCronReadsEnabledFromDB(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedAgentRow(t, db, "ag-cov-7", wsID, "", "Sched", "sched7", "AGENT")
	if _, err := db.Exec(`UPDATE agents SET schedule_enabled = 1 WHERE id = 'ag-cov-7'`); err != nil {
		t.Fatalf("set schedule_enabled: %v", err)
	}
	h := NewAgentHandler(db, newTestLogger())
	sched := &fakeScheduler{}
	h.SetScheduler(sched)

	body := []byte(`{"schedule_cron":"0 9 * * *"}`)
	req := covACRequest(t, "PATCH", "/api/v1/agents/ag-cov-7?workspace_id="+wsID, "ag-cov-7", wsID, userID, body)
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	if sched.calls != 1 || !sched.gotEnabled {
		t.Errorf("scheduler = %+v, want 1 call enabled=true (read from DB)", sched)
	}
}

// branch B: only schedule_enabled present → callback reads schedule_cron /
// schedule_prompt from the DB. Seed those so the NullString reads are valid.
func TestCovAC_Update_ScheduleEnabledOnly(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedAgentRow(t, db, "ag-cov-8", wsID, "", "Sched", "sched8", "AGENT")
	if _, err := db.Exec(`UPDATE agents SET schedule_cron = '1 2 3 4 5', schedule_prompt = 'do' WHERE id = 'ag-cov-8'`); err != nil {
		t.Fatalf("seed schedule fields: %v", err)
	}
	h := NewAgentHandler(db, newTestLogger())
	sched := &fakeScheduler{}
	h.SetScheduler(sched)

	body := []byte(`{"schedule_enabled":false}`)
	req := covACRequest(t, "PATCH", "/api/v1/agents/ag-cov-8?workspace_id="+wsID, "ag-cov-8", wsID, userID, body)
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	if sched.calls != 1 || sched.gotEnabled {
		t.Errorf("scheduler = %+v, want 1 call enabled=false", sched)
	}
}

// ---------------------------------------------------------------------------
// agents_update.go — 500 path (db closed mid-flight after edit-gate)
// ---------------------------------------------------------------------------

// With OWNER role canEditAgent() short-circuits without a DB query, so a
// closed DB makes agentExists() the first failing call → 500.
func TestCovAC_Update_DBClosed500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedAgentRow(t, db, "ag-cov-9", wsID, "", "Sched", "sched9", "AGENT")
	h := NewAgentHandler(db, newTestLogger())

	db.Close()

	body := []byte(`{"name":"x"}`)
	req := covACRequest(t, "PATCH", "/api/v1/agents/ag-cov-9?workspace_id="+wsID, "ag-cov-9", wsID, userID, body)
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500; body: %s", rr.Code, rr.Body.String())
	}
}
