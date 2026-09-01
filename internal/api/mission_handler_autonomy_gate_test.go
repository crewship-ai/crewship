package api

// #2258 — the PUBLIC mission-start route (MissionHandler.Start, reached via
// POST /api/v1/crews/{crewId}/missions/{missionId}/start) never consulted the
// #1768 autonomy gate. Only the sidecar-facing internal route
// (InternalMissionHandler.Start) checked autonomyGateApproved, so any
// MANAGER hitting the public route could start a mission whose gate was
// pending, or explicitly denied by an OWNER, and the DB carried no marker to
// tell it apart from a mission that was never held at all.
//
// These tests drive the SAME fixtures internal_autonomy_gate_test.go uses
// for the internal route (autonomyRig, boundInternalReq, decodeGateBody,
// approveHold) so a mission is put under a real hold the same way an agent
// would, and then hit the public handler directly — no stubbing of
// autonomyGateApproved itself.

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// startHeldMissionViaPublicRoute puts a mission under a strict-crew autonomy
// hold through the real internal Create path, optionally decides the hold
// (approved/denied; "pending" leaves it undecided), then calls the PUBLIC
// Start handler and returns its response plus the mission's post-call DB
// status.
func startHeldMissionViaPublicRoute(t *testing.T, decision string) (code int, dbStatus string) {
	t.Helper()
	db, wsID, crewID, userID, res := autonomyRig(t, "strict")
	leadID := seedAgentRow(t, db, "lead-pub-"+decision, wsID, crewID, "Lead", "lead-pub-"+decision, "LEAD")

	ih := NewInternalMissionHandler(db, nil, nil, testLogger())
	ih.SetAutonomyGate(res, nil)

	createBody := `{"title":"Held mission","lead_agent_id":"` + leadID +
		`","crew_id":"` + crewID + `","workspace_id":"` + wsID + `"}`
	createRR := httptest.NewRecorder()
	ih.Create(createRR, boundInternalReq(http.MethodPost, "/", createBody, wsID, crewID))
	if createRR.Code != http.StatusAccepted {
		t.Fatalf("internal create (strict) status = %d, want 202; body=%s", createRR.Code, createRR.Body.String())
	}
	body := decodeGateBody(t, createRR.Body.Bytes())
	missionID, _ := body["id"].(string)
	approvalID, _ := body["approval_id"].(string)
	if missionID == "" || approvalID == "" {
		t.Fatalf("held mission must return its id and approval; got %v", body)
	}

	if decision != "pending" {
		approveHold(t, db, wsID, userID, approvalID, decision)
	}

	pub := NewMissionHandler(db, nil, nil, testLogger())
	rr := httptest.NewRecorder()
	pub.Start(rr, startMissionReq(t, userID, wsID, crewID, missionID, "MANAGER"))

	var status string
	if err := db.QueryRow(`SELECT status FROM missions WHERE id = ?`, missionID).Scan(&status); err != nil {
		t.Fatalf("read mission status: %v", err)
	}
	return rr.Code, status
}

// TestMissionStart_PublicRoute_PendingGateRefuses is the headline case from
// #2258: a mission staged under a hold that nobody has decided yet must not
// start through the public route, exactly as it does not start through the
// internal one.
func TestMissionStart_PublicRoute_PendingGateRefuses(t *testing.T) {
	code, status := startHeldMissionViaPublicRoute(t, "pending")
	if code != http.StatusForbidden {
		t.Fatalf("PUBLIC start of a pending-gate mission = %d, want 403 (a MANAGER must not be able to start a mission awaiting decision)", code)
	}
	if status != "PLANNING" {
		t.Fatalf("mission status = %q, want PLANNING (a refused start must not have moved it)", status)
	}
}

// TestMissionStart_PublicRoute_DeniedGateRefuses is the exact defect #2258
// reports live: an OWNER denies the gate, and the public route let a MANAGER
// start the mission anyway because applyAutonomyGateDecisionTx deliberately
// writes no marker on the mission row itself — the approvals row is the ONLY
// door, and this proves the public route now uses it too.
func TestMissionStart_PublicRoute_DeniedGateRefuses(t *testing.T) {
	code, status := startHeldMissionViaPublicRoute(t, "denied")
	if code != http.StatusForbidden {
		t.Fatalf("PUBLIC start of a denied-gate mission = %d, want 403 (an explicit deny must not be overridable by hitting the other route)", code)
	}
	if status != "PLANNING" {
		t.Fatalf("mission status = %q, want PLANNING (a refused start must not have moved it)", status)
	}
}

// TestMissionStart_PublicRoute_ApprovedGateStarts is the control: once an
// OWNER/ADMIN approves the hold, the public route must behave exactly as it
// did before this fix for an unheld mission — PLANNING -> IN_PROGRESS.
func TestMissionStart_PublicRoute_ApprovedGateStarts(t *testing.T) {
	code, status := startHeldMissionViaPublicRoute(t, "approved")
	if code != http.StatusOK {
		t.Fatalf("PUBLIC start of an approved-gate mission = %d, want 200; mission status=%s", code, status)
	}
	if status != "IN_PROGRESS" {
		t.Fatalf("mission status = %q, want IN_PROGRESS", status)
	}
}

// TestMissionStart_PublicRoute_NoGateRowStartsFreely covers the crew that was
// never under a hold at all (e.g. trusted autonomy): the gate helper must
// answer hasHold=false and let the public route proceed exactly as it always
// has, matching TestAutonomyGate_MissionCreate_TrustedStartsFreely for the
// internal route.
func TestMissionStart_PublicRoute_NoGateRowStartsFreely(t *testing.T) {
	db, wsID, crewID, userID, res := autonomyRig(t, "trusted")
	leadID := seedAgentRow(t, db, "lead-pub-free", wsID, crewID, "Lead", "lead-pub-free", "LEAD")

	ih := NewInternalMissionHandler(db, nil, nil, testLogger())
	ih.SetAutonomyGate(res, nil)

	createBody := `{"title":"Free","lead_agent_id":"` + leadID +
		`","crew_id":"` + crewID + `","workspace_id":"` + wsID + `"}`
	createRR := httptest.NewRecorder()
	ih.Create(createRR, boundInternalReq(http.MethodPost, "/", createBody, wsID, crewID))
	if createRR.Code != http.StatusCreated {
		t.Fatalf("internal create (trusted) status = %d, want 201; body=%s", createRR.Code, createRR.Body.String())
	}
	body := decodeGateBody(t, createRR.Body.Bytes())
	missionID, _ := body["id"].(string)

	if n := gateCountRows(t, db, `SELECT COUNT(*) FROM approvals_queue WHERE json_extract(payload, '$.target_id') = ?`, missionID); n != 0 {
		t.Fatalf("trusted create must leave no gate row, found %d", n)
	}

	pub := NewMissionHandler(db, nil, nil, testLogger())
	rr := httptest.NewRecorder()
	pub.Start(rr, startMissionReq(t, userID, wsID, crewID, missionID, "MANAGER"))
	if rr.Code != http.StatusOK {
		t.Fatalf("PUBLIC start with no gate row = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	var status string
	if err := db.QueryRow(`SELECT status FROM missions WHERE id = ?`, missionID).Scan(&status); err != nil {
		t.Fatalf("read mission status: %v", err)
	}
	if status != "IN_PROGRESS" {
		t.Fatalf("mission status = %q, want IN_PROGRESS", status)
	}
}
