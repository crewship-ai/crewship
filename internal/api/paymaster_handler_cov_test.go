package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
)

// paymaster_handler_cov_test.go covers the DB-error 500 branches of every
// paymaster read endpoint. Technique: the guard lookups (crew/mission
// scope checks) read crews/missions while the rollups read cost_ledger,
// so renaming cost_ledger fails ONLY the rollup query — the guards still
// pass — and db.Close() fails the guard itself. All helpers are prefixed
// covPay.

// covPayBreakLedger renames cost_ledger so any paymaster rollup query
// fails with "no such table" while every other table keeps working.
func covPayBreakLedger(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`ALTER TABLE cost_ledger RENAME TO cost_ledger_broken`); err != nil {
		t.Fatalf("rename cost_ledger: %v", err)
	}
}

func covPayAssert(t *testing.T, rr *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rr.Code != want {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, want, rr.Body.String())
	}
}

func TestCovPay_SpendByCrew_QueryError_500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	covPayBreakLedger(t, db)

	h := NewPaymasterHandler(db, newTestLogger())
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/paymaster/spend/by-crew", nil),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.SpendByCrew(rr, req)
	covPayAssert(t, rr, http.StatusInternalServerError)
}

func TestCovPay_SpendByAgent_CrewLookupError_500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	db.Close()

	h := NewPaymasterHandler(db, newTestLogger())
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/paymaster/spend/by-agent/crew-x", nil),
		userID, wsID, "OWNER")
	req.SetPathValue("crewId", "crew-x")
	rr := httptest.NewRecorder()
	h.SpendByAgent(rr, req)
	covPayAssert(t, rr, http.StatusInternalServerError)
}

func TestCovPay_SpendByAgent_LedgerError_500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "covpay-crew", wsID, "Pay Crew", "covpay-crew")
	covPayBreakLedger(t, db)

	h := NewPaymasterHandler(db, newTestLogger())
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/paymaster/spend/by-agent/"+crewID, nil),
		userID, wsID, "OWNER")
	req.SetPathValue("crewId", crewID)
	rr := httptest.NewRecorder()
	h.SpendByAgent(rr, req)
	covPayAssert(t, rr, http.StatusInternalServerError)
}

func TestCovPay_SpendByMission_LookupError_500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	db.Close()

	h := NewPaymasterHandler(db, newTestLogger())
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/paymaster/spend/by-mission/m-x", nil),
		userID, wsID, "OWNER")
	req.SetPathValue("missionId", "m-x")
	rr := httptest.NewRecorder()
	h.SpendByMission(rr, req)
	covPayAssert(t, rr, http.StatusInternalServerError)
}

func TestCovPay_SpendByMission_LedgerError_500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "covpay-mcrew", wsID, "Pay Crew", "covpay-mcrew")
	seedAgentRow(t, db, "covpay-lead", wsID, crewID, "Lead", "covpay-lead", "LEAD")
	execOrFatal(t, db, `INSERT INTO missions
		(id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at)
		VALUES ('covpay-mission', ?, ?, 'covpay-lead', 'covpay-trace', 'M', 'IN_PROGRESS', datetime('now'))`,
		wsID, crewID)
	covPayBreakLedger(t, db)

	h := NewPaymasterHandler(db, newTestLogger())
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/paymaster/spend/by-mission/covpay-mission", nil),
		userID, wsID, "OWNER")
	req.SetPathValue("missionId", "covpay-mission")
	rr := httptest.NewRecorder()
	h.SpendByMission(rr, req)
	covPayAssert(t, rr, http.StatusInternalServerError)
}

func TestCovPay_TopSpenders_QueryError_500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	covPayBreakLedger(t, db)

	h := NewPaymasterHandler(db, newTestLogger())
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/paymaster/top-spenders?limit=5", nil),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.TopSpenders(rr, req)
	covPayAssert(t, rr, http.StatusInternalServerError)
}

func TestCovPay_SubscriptionUsage_QueryError_500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	covPayBreakLedger(t, db)

	h := NewPaymasterHandler(db, newTestLogger())
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/paymaster/subscriptions", nil),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.SubscriptionUsage(rr, req)
	covPayAssert(t, rr, http.StatusInternalServerError)
}
