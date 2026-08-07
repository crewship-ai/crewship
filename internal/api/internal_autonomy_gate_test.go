package api

// Tests for the #1768 autonomy gate on the sidecar-facing creation routes.
//
// Every gated route gets three things here:
//
//  1. it is REFUSED or HELD at a restrictive autonomy level,
//  2. it PROCEEDS at a permissive one,
//  3. for the held arms, the created thing is provably INERT — a crew that
//     cannot host an agent, an agent that cannot serve a message, a schedule
//     the scheduler will not pick up, a mission that cannot start — and the
//     operator's approve is what releases it.
//
// (3) is the part worth having. A test that only checks the status code would
// pass against a gate that returns 202 and then lets the thing act anyway,
// which is the bug class this issue is about.

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/harbormaster"
	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/policy"
)

// autonomyRig builds a workspace with one crew pinned at `level`, plus a
// policy.Resolver reading the same DB — the production wiring, so a change to
// the crews.autonomy_level column is what drives the gate rather than a stub.
func autonomyRig(t *testing.T, level string) (db *sql.DB, wsID, crewID, userID string, res *policy.Resolver) {
	t.Helper()
	db = setupTestDB(t)
	userID = seedTestUser(t, db)
	wsID = seedTestWorkspace(t, db, userID)
	crewID = seedCrewRow(t, db, "cr-gate", wsID, "Gate", "gate")
	execOrFatal(t, db, `UPDATE crews SET autonomy_level = ? WHERE id = ?`, level, crewID)
	return db, wsID, crewID, userID, policy.NewResolver(db)
}

// boundInternalReq builds a request that looks like a crew-bound (crwv1)
// sidecar call: requireInternal puts the token's workspace and crew in the
// context, and the gate resolves its policy subject from the crew binding.
func boundInternalReq(method, target, body, wsID, crewID string) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	ctx := context.WithValue(r.Context(), ctxInternalTokenWS, wsID)
	ctx = context.WithValue(ctx, ctxInternalTokenCrew, crewID)
	return r.WithContext(ctx)
}

func gatedInternalHandler(t *testing.T, db *sql.DB, res *policy.Resolver) *InternalHandler {
	t.Helper()
	h := NewInternalHandler(db, "tok", testLogger())
	h.SetPolicyResolver(res)
	return h
}

// decodeGateBody pulls the fields every gated route now returns.
func decodeGateBody(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, string(raw))
	}
	return out
}

// approveHold decides the pending autonomy-gate approval for targetID through
// the real POST /approvals/{id}/decide handler, so the release path under test
// is the one an operator actually uses.
func approveHold(t *testing.T, db *sql.DB, wsID, userID, approvalID, status string) {
	t.Helper()
	ah := NewApprovalsHandler(db, testLogger(), nil)
	req := withWorkspaceUser(httptest.NewRequest(http.MethodPost,
		"/api/v1/approvals/"+approvalID+"/decide",
		strings.NewReader(`{"status":"`+status+`"}`)), userID, wsID, "OWNER")
	req.SetPathValue("id", approvalID)
	rr := httptest.NewRecorder()
	ah.Decide(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("approvals decide: status = %d, body=%s", rr.Code, rr.Body.String())
	}
}

func gateCountRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count (%s): %v", query, err)
	}
	return n
}

// ── crew_create ─────────────────────────────────────────────────────────────

// TestAutonomyGate_CreateCrew_StrictRejects is the sharpest case in #1768: an
// agent in a strict crew could create a crew that was born `guided` (the v101
// column default), then create an agent inside it and act there. Strict now
// refuses outright, and no row is written.
func TestAutonomyGate_CreateCrew_StrictRejects(t *testing.T) {
	db, wsID, crewID, _, res := autonomyRig(t, "strict")
	h := gatedInternalHandler(t, db, res)

	req := boundInternalReq(http.MethodPost, "/?workspace_id="+wsID, `{"name":"Escape"}`, wsID, crewID)
	rr := httptest.NewRecorder()
	h.CreateCrew(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	body := decodeGateBody(t, rr.Body.Bytes())
	if body["autonomy_level"] != "strict" {
		t.Errorf("403 body must name the autonomy level so the CLI can suggest a fix; got %v", body)
	}
	if n := gateCountRows(t, db, `SELECT COUNT(*) FROM crews WHERE slug = 'escape'`); n != 0 {
		t.Errorf("a rejected crew_create wrote %d rows; want 0", n)
	}
}

// TestAutonomyGate_CreateCrew_GuidedHoldsAndPinsStrict covers the held arm and
// the inertness that makes it a gate rather than a status code: the new crew
// is pinned to strict, so agent_create and routine_schedule_create inside it
// are themselves Rejected — the crew cannot be populated until an operator
// approves, and approving restores the PARENT's level, never a higher one.
func TestAutonomyGate_CreateCrew_GuidedHoldsAndPinsStrict(t *testing.T) {
	db, wsID, crewID, userID, res := autonomyRig(t, "guided")
	h := gatedInternalHandler(t, db, res)

	req := boundInternalReq(http.MethodPost, "/?workspace_id="+wsID, `{"name":"Held"}`, wsID, crewID)
	rr := httptest.NewRecorder()
	h.CreateCrew(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rr.Code, rr.Body.String())
	}
	body := decodeGateBody(t, rr.Body.Bytes())
	newCrewID, _ := body["id"].(string)
	approvalID, _ := body["approval_id"].(string)
	if newCrewID == "" || approvalID == "" {
		t.Fatalf("held create must return both the crew id and the approval to decide; got %v", body)
	}
	if body["pending_review"] != true {
		t.Errorf("pending_review = %v, want true", body["pending_review"])
	}

	// INERT: the level is what makes it so.
	var level string
	if err := db.QueryRow(`SELECT autonomy_level FROM crews WHERE id = ?`, newCrewID).Scan(&level); err != nil {
		t.Fatalf("read new crew level: %v", err)
	}
	if level != "strict" {
		t.Fatalf("held crew autonomy_level = %q, want strict — a guided child could host an agent immediately", level)
	}

	// …and the sentinel really does close the escape: creating an agent in
	// the new crew is refused while it is held.
	agentReq := boundInternalReq(http.MethodPost, "/?workspace_id="+wsID,
		`{"crew_id":"`+newCrewID+`","name":"Sneak"}`, wsID, newCrewID)
	agentRR := httptest.NewRecorder()
	h.CreateAgent(agentRR, agentReq)
	if agentRR.Code != http.StatusForbidden {
		t.Fatalf("agent_create into a held crew = %d, want 403 — the autonomy escape is still open; body=%s",
			agentRR.Code, agentRR.Body.String())
	}

	// The operator's decision is on the standard approvals surface, and a
	// blocking inbox waitpoint points at it.
	if n := gateCountRows(t, db,
		`SELECT COUNT(*) FROM approvals_queue WHERE id = ? AND kind = ? AND status = 'pending'`,
		approvalID, string(harbormaster.KindAutonomyGate)); n != 1 {
		t.Errorf("expected one pending autonomy_gate approval, got %d", n)
	}
	if n := gateCountRows(t, db,
		`SELECT COUNT(*) FROM inbox_items WHERE source_id = ? AND kind = ? AND blocking = 1 AND state != 'resolved'`,
		newCrewID, inbox.KindWaitpoint); n != 1 {
		t.Errorf("expected one unresolved blocking waitpoint for the held crew, got %d", n)
	}

	// RELEASE.
	approveHold(t, db, wsID, userID, approvalID, "approved")
	if err := db.QueryRow(`SELECT autonomy_level FROM crews WHERE id = ?`, newCrewID).Scan(&level); err != nil {
		t.Fatalf("re-read new crew level: %v", err)
	}
	if level != "guided" {
		t.Errorf("approved crew autonomy_level = %q, want guided (the creating crew's level)", level)
	}
	if n := gateCountRows(t, db,
		`SELECT COUNT(*) FROM inbox_items WHERE source_id = ? AND state = 'resolved'`, newCrewID); n != 1 {
		t.Errorf("approving must resolve the blocking waitpoint; unresolved rows remain")
	}
}

// TestAutonomyGate_CreateCrew_FullProceedsAndInherits pins the permissive arm:
// full autonomy creates the crew live, with a non-blocking notice, and the
// child inherits the parent's level rather than the column default.
func TestAutonomyGate_CreateCrew_FullProceedsAndInherits(t *testing.T) {
	db, wsID, crewID, _, res := autonomyRig(t, "full")
	h := gatedInternalHandler(t, db, res)

	req := boundInternalReq(http.MethodPost, "/?workspace_id="+wsID, `{"name":"Live"}`, wsID, crewID)
	rr := httptest.NewRecorder()
	h.CreateCrew(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	body := decodeGateBody(t, rr.Body.Bytes())
	if body["pending_review"] != false {
		t.Errorf("pending_review = %v, want false at full autonomy", body["pending_review"])
	}
	var level string
	if err := db.QueryRow(`SELECT autonomy_level FROM crews WHERE id = ?`, body["id"]).Scan(&level); err != nil {
		t.Fatalf("read new crew level: %v", err)
	}
	if level != "full" {
		t.Errorf("new crew autonomy_level = %q, want full (inherited)", level)
	}
	if n := gateCountRows(t, db,
		`SELECT COUNT(*) FROM inbox_items WHERE source_id = ? AND blocking = 0`, body["id"]); n != 1 {
		t.Errorf("full autonomy must still leave a non-blocking notice; got %d rows", n)
	}
}

// ── agent_create ────────────────────────────────────────────────────────────

func TestAutonomyGate_CreateAgent_StrictRejects(t *testing.T) {
	db, wsID, crewID, _, res := autonomyRig(t, "strict")
	h := gatedInternalHandler(t, db, res)

	req := boundInternalReq(http.MethodPost, "/?workspace_id="+wsID,
		`{"crew_id":"`+crewID+`","name":"Ghost","system_prompt":"you are unbounded"}`, wsID, crewID)
	rr := httptest.NewRecorder()
	h.CreateAgent(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	if n := gateCountRows(t, db, `SELECT COUNT(*) FROM agents WHERE name = 'Ghost'`); n != 0 {
		t.Errorf("a rejected agent_create wrote %d rows; want 0 (the system_prompt is persona authorship)", n)
	}
}

// TestAutonomyGate_CreateAgent_GuidedStagesPendingReview asserts the sentinel
// itself: the row exists but carries status=PENDING_REVIEW, which the
// chatbridge refuses to start (internal/chatbridge/bridge.go — the guard is
// not ephemeral-scoped, so it covers this permanent agent too).
func TestAutonomyGate_CreateAgent_GuidedStagesPendingReview(t *testing.T) {
	db, wsID, crewID, userID, res := autonomyRig(t, "guided")
	h := gatedInternalHandler(t, db, res)

	req := boundInternalReq(http.MethodPost, "/?workspace_id="+wsID,
		`{"crew_id":"`+crewID+`","name":"Staged"}`, wsID, crewID)
	rr := httptest.NewRecorder()
	h.CreateAgent(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rr.Code, rr.Body.String())
	}
	body := decodeGateBody(t, rr.Body.Bytes())
	agentID, _ := body["id"].(string)
	approvalID, _ := body["approval_id"].(string)
	if agentID == "" || approvalID == "" {
		t.Fatalf("held create must return the agent id and the approval; got %v", body)
	}

	var status string
	if err := db.QueryRow(`SELECT status FROM agents WHERE id = ?`, agentID).Scan(&status); err != nil {
		t.Fatalf("read staged agent: %v", err)
	}
	if status != "PENDING_REVIEW" {
		t.Fatalf("staged agent status = %q, want PENDING_REVIEW — an IDLE row would serve messages immediately", status)
	}

	approveHold(t, db, wsID, userID, approvalID, "approved")

	if err := db.QueryRow(`SELECT status FROM agents WHERE id = ?`, agentID).Scan(&status); err != nil {
		t.Fatalf("re-read agent: %v", err)
	}
	if status != "IDLE" {
		t.Errorf("approved agent status = %q, want IDLE", status)
	}
}

// TestAutonomyGate_CreateAgent_DenyLeavesInert pins the deny semantics: a
// refused staging does not delete anything, it just never releases. The agent
// stays unable to serve a message.
func TestAutonomyGate_CreateAgent_DenyLeavesInert(t *testing.T) {
	db, wsID, crewID, userID, res := autonomyRig(t, "guided")
	h := gatedInternalHandler(t, db, res)

	req := boundInternalReq(http.MethodPost, "/?workspace_id="+wsID,
		`{"crew_id":"`+crewID+`","name":"Refused"}`, wsID, crewID)
	rr := httptest.NewRecorder()
	h.CreateAgent(rr, req)
	body := decodeGateBody(t, rr.Body.Bytes())
	agentID, _ := body["id"].(string)
	approvalID, _ := body["approval_id"].(string)

	approveHold(t, db, wsID, userID, approvalID, "denied")

	var status string
	if err := db.QueryRow(`SELECT status FROM agents WHERE id = ?`, agentID).Scan(&status); err != nil {
		t.Fatalf("read denied agent: %v", err)
	}
	if status != "PENDING_REVIEW" {
		t.Errorf("denied agent status = %q, want PENDING_REVIEW (denied = stays inert)", status)
	}
}

func TestAutonomyGate_CreateAgent_FullGoesLive(t *testing.T) {
	db, wsID, crewID, _, res := autonomyRig(t, "full")
	h := gatedInternalHandler(t, db, res)

	req := boundInternalReq(http.MethodPost, "/?workspace_id="+wsID,
		`{"crew_id":"`+crewID+`","name":"Live"}`, wsID, crewID)
	rr := httptest.NewRecorder()
	h.CreateAgent(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	body := decodeGateBody(t, rr.Body.Bytes())
	var status string
	if err := db.QueryRow(`SELECT status FROM agents WHERE id = ?`, body["id"]).Scan(&status); err != nil {
		t.Fatalf("read agent: %v", err)
	}
	if status != "IDLE" {
		t.Errorf("agent status at full autonomy = %q, want IDLE", status)
	}
}

// ── mission_create ──────────────────────────────────────────────────────────

// TestAutonomyGate_MissionCreate_StrictBlocksStart is the inertness proof for
// missions: the row is written (a strict crew can still PLAN work) but Start
// refuses, so the MissionEngine is never handed the mission and nothing it
// planned dispatches.
//
// CONTRACT CHANGE: this test ran at `guided` until the #1768 matrix rebalance.
// mission_create now answers AutoLogInbox at guided — a mission creates no
// principal and is pinned to the caller's own crew, so blocking it stopped
// ordinary planning work without closing anything (the escape #1768 fixed runs
// through crew_create + agent_create, not here). The held arm itself is
// unchanged and still worth proving, so the test moved DOWN to the level that
// still holds rather than being deleted or softened.
func TestAutonomyGate_MissionCreate_StrictBlocksStart(t *testing.T) {
	db, wsID, crewID, userID, res := autonomyRig(t, "strict")
	leadID := seedAgentRow(t, db, "lead-gate", wsID, crewID, "Lead", "lead-gate", "LEAD")
	h := NewInternalMissionHandler(db, nil, nil, testLogger())
	h.SetAutonomyGate(res, nil)

	createBody := `{"title":"Held mission","lead_agent_id":"` + leadID +
		`","crew_id":"` + crewID + `","workspace_id":"` + wsID + `"}`
	rr := httptest.NewRecorder()
	h.Create(rr, boundInternalReq(http.MethodPost, "/", createBody, wsID, crewID))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, want 202; body=%s", rr.Code, rr.Body.String())
	}
	body := decodeGateBody(t, rr.Body.Bytes())
	missionID, _ := body["id"].(string)
	approvalID, _ := body["approval_id"].(string)
	if missionID == "" || approvalID == "" {
		t.Fatalf("held mission must return its id and approval; got %v", body)
	}

	startReq := boundInternalReq(http.MethodPost, "/?workspace_id="+wsID, "", wsID, crewID)
	startReq.SetPathValue("missionId", missionID)
	startRR := httptest.NewRecorder()
	h.Start(startRR, startReq)
	if startRR.Code != http.StatusForbidden {
		t.Fatalf("start of a held mission = %d, want 403; body=%s", startRR.Code, startRR.Body.String())
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM missions WHERE id = ?`, missionID).Scan(&status); err != nil {
		t.Fatalf("read mission: %v", err)
	}
	if status != "PLANNING" {
		t.Fatalf("held mission status = %q, want PLANNING (a refused start must not have moved it)", status)
	}

	approveHold(t, db, wsID, userID, approvalID, "approved")

	startRR2 := httptest.NewRecorder()
	startReq2 := boundInternalReq(http.MethodPost, "/?workspace_id="+wsID, "", wsID, crewID)
	startReq2.SetPathValue("missionId", missionID)
	h.Start(startRR2, startReq2)
	if startRR2.Code != http.StatusOK {
		t.Fatalf("start after approval = %d, want 200; body=%s", startRR2.Code, startRR2.Body.String())
	}
}

// TestAutonomyGate_MissionCreate_TimedOutHoldStaysClosed is the fail-closed
// proof. harbormaster's sweeper flips a lapsed hold out of `pending`, so a
// gate phrased as "no pending row blocks me" would turn an unattended hold
// into a green light. Only `approved` releases.
//
// CONTRACT CHANGE: moved from `guided` to `strict` for the reason above —
// guided no longer holds a mission, so the level had to move for the assertion
// to keep testing what it says it tests.
func TestAutonomyGate_MissionCreate_TimedOutHoldStaysClosed(t *testing.T) {
	db, wsID, crewID, _, res := autonomyRig(t, "strict")
	leadID := seedAgentRow(t, db, "lead-timeout", wsID, crewID, "Lead", "lead-timeout", "LEAD")
	h := NewInternalMissionHandler(db, nil, nil, testLogger())
	h.SetAutonomyGate(res, nil)

	createBody := `{"title":"Lapsed","lead_agent_id":"` + leadID +
		`","crew_id":"` + crewID + `","workspace_id":"` + wsID + `"}`
	rr := httptest.NewRecorder()
	h.Create(rr, boundInternalReq(http.MethodPost, "/", createBody, wsID, crewID))
	body := decodeGateBody(t, rr.Body.Bytes())
	missionID, _ := body["id"].(string)

	execOrFatal(t, db, `UPDATE approvals_queue SET status = 'timeout' WHERE json_extract(payload, '$.target_id') = ?`, missionID)

	startReq := boundInternalReq(http.MethodPost, "/?workspace_id="+wsID, "", wsID, crewID)
	startReq.SetPathValue("missionId", missionID)
	startRR := httptest.NewRecorder()
	h.Start(startRR, startReq)
	if startRR.Code != http.StatusForbidden {
		t.Fatalf("start after a TIMED-OUT hold = %d, want 403 — the gate must fail closed; body=%s",
			startRR.Code, startRR.Body.String())
	}
}

func TestAutonomyGate_MissionCreate_TrustedStartsFreely(t *testing.T) {
	db, wsID, crewID, _, res := autonomyRig(t, "trusted")
	leadID := seedAgentRow(t, db, "lead-trusted", wsID, crewID, "Lead", "lead-trusted", "LEAD")
	h := NewInternalMissionHandler(db, nil, nil, testLogger())
	h.SetAutonomyGate(res, nil)

	createBody := `{"title":"Free","lead_agent_id":"` + leadID +
		`","crew_id":"` + crewID + `","workspace_id":"` + wsID + `"}`
	rr := httptest.NewRecorder()
	h.Create(rr, boundInternalReq(http.MethodPost, "/", createBody, wsID, crewID))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create at trusted = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	body := decodeGateBody(t, rr.Body.Bytes())
	missionID, _ := body["id"].(string)

	startReq := boundInternalReq(http.MethodPost, "/?workspace_id="+wsID, "", wsID, crewID)
	startReq.SetPathValue("missionId", missionID)
	startRR := httptest.NewRecorder()
	h.Start(startRR, startReq)
	if startRR.Code != http.StatusOK {
		t.Fatalf("start at trusted = %d, want 200; body=%s", startRR.Code, startRR.Body.String())
	}
}

// TestAutonomyGate_MissionCreate_GuidedNoticesAndStarts is the NEW contract at
// the DEFAULT autonomy level, and the other half of the test that moved to
// strict above. Guided proceeds — 201, startable, no approvals row — but must
// still leave the operator a non-blocking notice. Dropping the hold without
// keeping the notice would have traded a gate for silence, which is not the
// trade that was made.
func TestAutonomyGate_MissionCreate_GuidedNoticesAndStarts(t *testing.T) {
	db, wsID, crewID, _, res := autonomyRig(t, "guided")
	leadID := seedAgentRow(t, db, "lead-guided", wsID, crewID, "Lead", "lead-guided", "LEAD")
	h := NewInternalMissionHandler(db, nil, nil, testLogger())
	h.SetAutonomyGate(res, nil)

	createBody := `{"title":"Planned","lead_agent_id":"` + leadID +
		`","crew_id":"` + crewID + `","workspace_id":"` + wsID + `"}`
	rr := httptest.NewRecorder()
	h.Create(rr, boundInternalReq(http.MethodPost, "/", createBody, wsID, crewID))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create at guided = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	body := decodeGateBody(t, rr.Body.Bytes())
	missionID, _ := body["id"].(string)
	if body["pending_review"] != false {
		t.Errorf("pending_review = %v, want false — guided no longer holds a mission", body["pending_review"])
	}
	if body["decision"] != string(policy.DecisionAutoLogInbox) {
		t.Errorf("decision = %v, want auto_log_inbox", body["decision"])
	}
	if body["approval_id"] != "" {
		t.Errorf("approval_id = %v, want empty — nothing is waiting on a human", body["approval_id"])
	}
	if n := gateCountRows(t, db,
		`SELECT COUNT(*) FROM approvals_queue WHERE workspace_id = ? AND kind = ?`,
		wsID, string(harbormaster.KindAutonomyGate)); n != 0 {
		t.Errorf("guided mission_create queued %d approvals; want 0", n)
	}

	// Visibility survives the relaxation: one non-blocking inbox item.
	if n := gateCountRows(t, db,
		`SELECT COUNT(*) FROM inbox_items WHERE source_id = ? AND blocking = 0`, missionID); n != 1 {
		t.Errorf("guided mission_create left %d non-blocking notices; want 1", n)
	}
	if n := gateCountRows(t, db,
		`SELECT COUNT(*) FROM inbox_items WHERE source_id = ? AND blocking = 1`, missionID); n != 0 {
		t.Errorf("guided mission_create left %d BLOCKING items; want 0", n)
	}

	// …and Start is not gated, because no hold exists to consult.
	startReq := boundInternalReq(http.MethodPost, "/?workspace_id="+wsID, "", wsID, crewID)
	startReq.SetPathValue("missionId", missionID)
	startRR := httptest.NewRecorder()
	h.Start(startRR, startReq)
	if startRR.Code != http.StatusOK {
		t.Fatalf("start at guided = %d, want 200; body=%s", startRR.Code, startRR.Body.String())
	}
}

// ── routine_schedule_create ─────────────────────────────────────────────────

func gatedRoutineAdapter(t *testing.T, db *sql.DB, res *policy.Resolver) *RoutineInternalAdapter {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	pipes := NewPipelineHandler(db, logger, nil, nil)
	pipes.SetScheduleStore(pipeline.NewScheduleStore(db))
	a := NewRoutineInternalAdapter(pipes)
	a.SetAutonomyGate(res, nil)
	return a
}

func TestAutonomyGate_Schedule_StrictRejects(t *testing.T) {
	db, wsID, crewID, _, res := autonomyRig(t, "strict")
	seedPipelineRow(t, db, wsID, "pl-gate", "gate-pipe")
	a := gatedRoutineAdapter(t, db, res)

	rr := httptest.NewRecorder()
	a.CreateSchedule(rr, boundInternalReq(http.MethodPost, "/?workspace_id="+wsID,
		`{"cron_expr":"*/5 * * * *","target_pipeline_slug":"gate-pipe"}`, wsID, crewID))

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	if n := gateCountRows(t, db, `SELECT COUNT(*) FROM pipeline_schedules WHERE workspace_id = ?`, wsID); n != 0 {
		t.Errorf("a rejected schedule create wrote %d rows; want 0", n)
	}
}

// TestAutonomyGate_Schedule_HeldCreatesDisabled proves the sentinel: the
// scheduler's pick-up query is `WHERE enabled = 1`, so a row written with
// enabled=0 never fires, whatever the caller asked for.
//
// CONTRACT CHANGE: this ran at `guided` until the #1768 matrix rebalance.
// routine_schedule_create now answers AutoLogInbox at guided, so NO autonomy
// level holds a schedule any more — strict refuses, everything below notices.
// The held machinery is still live though, reached by gateInternalAction's
// fail-closed fallback when the policy resolver is unwired, so the test is
// driven from there rather than deleted. That is the case that matters most:
// a cron entry created while the gate is broken must not fire.
func TestAutonomyGate_Schedule_HeldCreatesDisabled(t *testing.T) {
	db, wsID, crewID, userID, _ := autonomyRig(t, "guided")
	seedPipelineRow(t, db, wsID, "pl-gate", "gate-pipe")
	// nil resolver == gate not wired == hold, whatever the crew's level says.
	a := gatedRoutineAdapter(t, db, nil)

	rr := httptest.NewRecorder()
	// The caller explicitly asks for enabled:true — the gate must overwrite
	// it, not merge with it.
	a.CreateSchedule(rr, boundInternalReq(http.MethodPost, "/?workspace_id="+wsID,
		`{"cron_expr":"*/5 * * * *","target_pipeline_slug":"gate-pipe","enabled":true}`, wsID, crewID))

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rr.Code, rr.Body.String())
	}
	body := decodeGateBody(t, rr.Body.Bytes())
	schedID, _ := body["id"].(string)
	approvalID, _ := body["approval_id"].(string)
	if schedID == "" || approvalID == "" {
		t.Fatalf("held schedule must return its id and approval; got %v", body)
	}

	var enabled int
	if err := db.QueryRow(`SELECT enabled FROM pipeline_schedules WHERE id = ?`, schedID).Scan(&enabled); err != nil {
		t.Fatalf("read schedule: %v", err)
	}
	if enabled != 0 {
		t.Fatalf("held schedule enabled = %d, want 0 — an enabled cron entry fires without anyone approving it", enabled)
	}

	approveHold(t, db, wsID, userID, approvalID, "approved")

	if err := db.QueryRow(`SELECT enabled FROM pipeline_schedules WHERE id = ?`, schedID).Scan(&enabled); err != nil {
		t.Fatalf("re-read schedule: %v", err)
	}
	if enabled != 1 {
		t.Errorf("approved schedule enabled = %d, want 1", enabled)
	}
}

func TestAutonomyGate_Schedule_FullEnabled(t *testing.T) {
	db, wsID, crewID, _, res := autonomyRig(t, "full")
	seedPipelineRow(t, db, wsID, "pl-gate", "gate-pipe")
	a := gatedRoutineAdapter(t, db, res)

	rr := httptest.NewRecorder()
	a.CreateSchedule(rr, boundInternalReq(http.MethodPost, "/?workspace_id="+wsID,
		`{"cron_expr":"*/5 * * * *","target_pipeline_slug":"gate-pipe"}`, wsID, crewID))

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	body := decodeGateBody(t, rr.Body.Bytes())
	var enabled int
	if err := db.QueryRow(`SELECT enabled FROM pipeline_schedules WHERE id = ?`, body["id"]).Scan(&enabled); err != nil {
		t.Fatalf("read schedule: %v", err)
	}
	if enabled != 1 {
		t.Errorf("schedule at full autonomy enabled = %d, want 1", enabled)
	}
}

// TestAutonomyGate_Schedule_GuidedCreatesEnabledWithNotice is the NEW contract
// at the DEFAULT autonomy level. The schedule is created ENABLED — the
// forceScheduleDisabled body rewrite must not fire — and the operator's
// visibility comes from a non-blocking notice instead of a hold.
//
// The `enabled: true` in the body is deliberate: it proves the value survived
// rather than being coincidentally right, since forceScheduleDisabled
// overwrites rather than merges.
func TestAutonomyGate_Schedule_GuidedCreatesEnabledWithNotice(t *testing.T) {
	db, wsID, crewID, _, res := autonomyRig(t, "guided")
	seedPipelineRow(t, db, wsID, "pl-gate", "gate-pipe")
	a := gatedRoutineAdapter(t, db, res)

	rr := httptest.NewRecorder()
	a.CreateSchedule(rr, boundInternalReq(http.MethodPost, "/?workspace_id="+wsID,
		`{"cron_expr":"*/5 * * * *","target_pipeline_slug":"gate-pipe","enabled":true}`, wsID, crewID))

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	body := decodeGateBody(t, rr.Body.Bytes())
	schedID, _ := body["id"].(string)
	if schedID == "" {
		t.Fatalf("no schedule id in body: %v", body)
	}
	if body["pending_review"] != false {
		t.Errorf("pending_review = %v, want false", body["pending_review"])
	}
	if body["decision"] != string(policy.DecisionAutoLogInbox) {
		t.Errorf("decision = %v, want auto_log_inbox", body["decision"])
	}

	var enabled int
	if err := db.QueryRow(`SELECT enabled FROM pipeline_schedules WHERE id = ?`, schedID).Scan(&enabled); err != nil {
		t.Fatalf("read schedule: %v", err)
	}
	if enabled != 1 {
		t.Fatalf("guided schedule enabled = %d, want 1 — forceScheduleDisabled fired on the notice arm", enabled)
	}
	if n := gateCountRows(t, db,
		`SELECT COUNT(*) FROM approvals_queue WHERE workspace_id = ? AND kind = ?`,
		wsID, string(harbormaster.KindAutonomyGate)); n != 0 {
		t.Errorf("guided schedule create queued %d approvals; want 0", n)
	}
	if n := gateCountRows(t, db,
		`SELECT COUNT(*) FROM inbox_items WHERE source_id = ? AND blocking = 0`, schedID); n != 1 {
		t.Errorf("guided schedule create left %d non-blocking notices; want 1", n)
	}
}

// TestAutonomyGate_NilResolver_HoldsEvenWhatGuidedAllows pins the fail-closed
// fallback against the tidy-up that would break it.
//
// gateInternalAction seeds its decision as a LITERAL InboxApprove and only
// overwrites it when a resolver exists. Deriving that seed from the matrix
// (`Policy{AutonomyLevel: AutonomyGuided}.DecideAction(action)`) reads like
// removing duplication, and after the rebalance it would convert an unwired
// resolver from "hold everything" into "wave mission_create and
// routine_schedule_create through" — fail-open on a wiring bug. A nil resolver
// means THE GATE IS NOT WIRED; that is not the same question as what a guided
// crew may do, and the two must not share a source.
func TestAutonomyGate_NilResolver_HoldsEvenWhatGuidedAllows(t *testing.T) {
	// Both actions guided now allows without blocking. Under a nil resolver
	// each must still be held.
	t.Run("mission_create", func(t *testing.T) {
		db, wsID, crewID, _, _ := autonomyRig(t, "guided")
		leadID := seedAgentRow(t, db, "lead-nil", wsID, crewID, "Lead", "lead-nil", "LEAD")
		h := NewInternalMissionHandler(db, nil, nil, testLogger())
		// SetAutonomyGate deliberately NOT called: the router forgot to wire it.

		createBody := `{"title":"Unwired","lead_agent_id":"` + leadID +
			`","crew_id":"` + crewID + `","workspace_id":"` + wsID + `"}`
		rr := httptest.NewRecorder()
		h.Create(rr, boundInternalReq(http.MethodPost, "/", createBody, wsID, crewID))
		if rr.Code != http.StatusAccepted {
			t.Fatalf("unwired gate answered %d, want 202 (held); body=%s", rr.Code, rr.Body.String())
		}
		body := decodeGateBody(t, rr.Body.Bytes())
		missionID, _ := body["id"].(string)

		startReq := boundInternalReq(http.MethodPost, "/?workspace_id="+wsID, "", wsID, crewID)
		startReq.SetPathValue("missionId", missionID)
		startRR := httptest.NewRecorder()
		h.Start(startRR, startReq)
		if startRR.Code != http.StatusForbidden {
			t.Errorf("start under an unwired gate = %d, want 403", startRR.Code)
		}
	})

	t.Run("routine_schedule_create", func(t *testing.T) {
		db, wsID, crewID, _, _ := autonomyRig(t, "guided")
		seedPipelineRow(t, db, wsID, "pl-nil", "nil-pipe")
		a := gatedRoutineAdapter(t, db, nil)

		rr := httptest.NewRecorder()
		a.CreateSchedule(rr, boundInternalReq(http.MethodPost, "/?workspace_id="+wsID,
			`{"cron_expr":"*/5 * * * *","target_pipeline_slug":"nil-pipe","enabled":true}`, wsID, crewID))
		if rr.Code != http.StatusAccepted {
			t.Fatalf("unwired gate answered %d, want 202 (held); body=%s", rr.Code, rr.Body.String())
		}
		body := decodeGateBody(t, rr.Body.Bytes())
		var enabled int
		if err := db.QueryRow(`SELECT enabled FROM pipeline_schedules WHERE id = ?`, body["id"]).Scan(&enabled); err != nil {
			t.Fatalf("read schedule: %v", err)
		}
		if enabled != 0 {
			t.Errorf("schedule created under an unwired gate is enabled = %d; want 0", enabled)
		}
	})
}

// ── skill_create ────────────────────────────────────────────────────────────

// TestAutonomyGate_SkillAuthor_StagesAndRecordsDecision. This route was
// already inert (it only ever stages into .proposed behind a blocking
// ADMIN-addressed review item), so what the gate adds is the recorded verdict
// and the Rejected arm. The assertion here is that the staging contract holds
// AND the response now carries the decision.
func TestAutonomyGate_SkillAuthor_StagesAndRecordsDecision(t *testing.T) {
	db, wsID, crewID, _, res := autonomyRig(t, "guided")
	h := NewSkillProposedHandler(db, testLogger())
	h.SetStorageBasePath(t.TempDir())
	h.SetPolicyResolver(res)

	content := "---\nname: gate-skill\ndescription: Use when the operator wants to test the gate.\n---\n# Gate\n\n## When to use\nAlways in tests.\n"
	req := boundInternalReq(http.MethodPost, "/?crew_id="+crewID, `{"content":`+jsonString(content)+`}`, wsID, crewID)
	req = req.WithContext(context.WithValue(req.Context(), ctxWorkspaceID, wsID))
	rr := httptest.NewRecorder()
	h.Author(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	body := decodeGateBody(t, rr.Body.Bytes())
	if body["decision"] != string(policy.DecisionInboxApprove) {
		t.Errorf("decision = %v, want inbox_approve", body["decision"])
	}
	if body["pending_review"] != true {
		t.Errorf("pending_review = %v, want true — this route only ever stages", body["pending_review"])
	}
	// Still inert: nothing in the live registry.
	if n := gateCountRows(t, db, `SELECT COUNT(*) FROM skills WHERE slug = 'gate-skill'`); n != 0 {
		t.Errorf("authored skill reached the live registry (%d rows); it must stay staged", n)
	}
}

// TestAutonomyGate_SkillGenerate_HeldRefusesWithoutCrew documents the one
// behavioural regression this change introduces: a workspace-bound (crew-less)
// caller has no .proposed directory to stage into, so the held arm cannot
// offer "created but inert" and refuses instead, naming what the operator
// must do. Crew-bound callers — every real sidecar for a crew run — are
// unaffected.
func TestAutonomyGate_SkillGenerate_HeldRefusesWithoutCrew(t *testing.T) {
	db, wsID, _, _, res := autonomyRig(t, "guided")
	gen := &SkillGenerateHandler{db: db, logger: testLogger()}
	a := NewSkillInternalAdapter(gen)
	a.SetAutonomyGate(res, NewSkillProposedHandler(db, testLogger()))

	// No crew binding in context and no ?crew_id.
	r := httptest.NewRequest(http.MethodPost, "/?workspace_id="+wsID,
		strings.NewReader(`{"slug":"x","prompt":"y"}`))
	rr := httptest.NewRecorder()
	a.Generate(rr, r)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "staged for review") {
		t.Errorf("the refusal must name what the operator has to do; body=%s", rr.Body.String())
	}
	if n := gateCountRows(t, db, `SELECT COUNT(*) FROM skills WHERE slug = 'x'`); n != 0 {
		t.Errorf("refused generation still wrote %d registry rows", n)
	}
}

// TestAutonomyGate_SkillGenerate_FullTakesRegistryPath pins that the
// permissive arm does NOT go through the staging branch — it reaches the
// public Generate, which fails on the missing Anthropic credential (412)
// rather than on the staging refusal (403).
func TestAutonomyGate_SkillGenerate_FullTakesRegistryPath(t *testing.T) {
	db, wsID, crewID, _, res := autonomyRig(t, "full")
	gen := &SkillGenerateHandler{db: db, logger: testLogger()}
	a := NewSkillInternalAdapter(gen)
	a.SetAutonomyGate(res, NewSkillProposedHandler(db, testLogger()))

	rr := httptest.NewRecorder()
	a.Generate(rr, boundInternalReq(http.MethodPost, "/?workspace_id="+wsID,
		`{"slug":"x","prompt":"y"}`, wsID, crewID))

	if rr.Code == http.StatusForbidden {
		t.Fatalf("full autonomy took the held/staged branch; body=%s", rr.Body.String())
	}
	if rr.Code != http.StatusPreconditionFailed {
		t.Fatalf("status = %d, want 412 (reached Generate's Anthropic credential check); body=%s",
			rr.Code, rr.Body.String())
	}
}

// ── the invariant the whole gate rests on ───────────────────────────────────

// autonomyRank orders the trust dial from least to most permissive, so two
// crews' levels can be compared. Any level not listed ranks above `full`,
// which makes an unknown value fail the comparison rather than pass it.
func autonomyRank(level string) int {
	switch policy.AutonomyLevel(level) {
	case policy.AutonomyStrict:
		return 0
	case policy.AutonomyGuided:
		return 1
	case policy.AutonomyTrusted:
		return 2
	case policy.AutonomyFull:
		return 3
	}
	return 99
}

// TestAutonomyInvariant_ChildCrewNeverOutranksCreator is the load-bearing test
// of #1768, and it is deliberately not a test of any matrix cell.
//
// WHY IT IS NAMED FOR A PROPERTY AND NOT A ROW. The escape was: an agent in a
// strict crew creates a crew, the new crew takes `crews.autonomy_level DEFAULT
// 'guided'` (migration v101), the agent creates an agent in it and acts
// unbounded. What closes that is NOT the blocking at guided — it is (a) strict
// refusing crew_create outright and (b) an allowed crew INHERITING its
// creator's level instead of the column default. Those two together mean no
// created crew can ever be more permissive than its creator.
//
// The decision cells are a product question and will be re-tuned; this
// property must not be re-tunable. If a future matrix change reopens the
// escape, this test is what goes red, whatever the cells say.
//
// TWO CEILINGS, because the invariant has two halves and only both together
// close the hole:
//
//  1. the child's level never outranks the creating crew's, and
//  2. while the creation is still awaiting approval the child is pinned to
//     `strict` — a child born at the parent's level would be populatable
//     BEFORE the operator decided, which is the escape wearing a 202.
//
// Half (2) is the one with teeth today: half (1) is satisfied trivially
// whenever the code writes the parent's level, whereas (2) fails the moment
// the INSERT falls back to the column default.
func TestAutonomyInvariant_ChildCrewNeverOutranksCreator(t *testing.T) {
	for _, parent := range []string{"strict", "guided", "trusted", "full"} {
		t.Run("creator="+parent, func(t *testing.T) {
			db, wsID, crewID, userID, res := autonomyRig(t, parent)
			h := gatedInternalHandler(t, db, res)

			rr := httptest.NewRecorder()
			h.CreateCrew(rr, boundInternalReq(http.MethodPost, "/?workspace_id="+wsID,
				`{"name":"Child"}`, wsID, crewID))

			if rr.Code == http.StatusForbidden {
				// Refusal satisfies the invariant only if nothing was written.
				if n := gateCountRows(t, db, `SELECT COUNT(*) FROM crews WHERE slug = 'child'`); n != 0 {
					t.Fatalf("crew_create was refused but wrote %d rows", n)
				}
				return
			}
			if rr.Code != http.StatusCreated && rr.Code != http.StatusAccepted {
				t.Fatalf("unexpected status %d; body=%s", rr.Code, rr.Body.String())
			}

			body := decodeGateBody(t, rr.Body.Bytes())
			childID, _ := body["id"].(string)
			approvalID, _ := body["approval_id"].(string)
			held := body["pending_review"] == true
			if childID == "" {
				t.Fatalf("no child crew id in body: %v", body)
			}

			readLevel := func(when string) string {
				var lvl string
				if err := db.QueryRow(`SELECT autonomy_level FROM crews WHERE id = ?`, childID).Scan(&lvl); err != nil {
					t.Fatalf("read child level (%s): %v", when, err)
				}
				return lvl
			}

			// Ceiling 1 — at creation.
			lvl := readLevel("at creation")
			if autonomyRank(lvl) > autonomyRank(parent) {
				t.Fatalf("child crew was born %q under a %q creator — a crew created through the "+
					"internal API must never be more permissive than the crew that created it", lvl, parent)
			}

			if held {
				// Ceiling 2 — a crew awaiting approval is inert, not merely
				// no-more-permissive.
				if lvl != string(policy.AutonomyStrict) {
					t.Fatalf("held child crew is %q, want strict — it can be populated before "+
						"the operator has decided", lvl)
				}
				// Proven, not assumed: agent_create into it is refused.
				agentRR := httptest.NewRecorder()
				h.CreateAgent(agentRR, boundInternalReq(http.MethodPost, "/?workspace_id="+wsID,
					`{"crew_id":"`+childID+`","name":"Sneak"}`, wsID, childID))
				if agentRR.Code != http.StatusForbidden {
					t.Fatalf("agent_create into a held child = %d, want 403 — the escape is open; body=%s",
						agentRR.Code, agentRR.Body.String())
				}

				// Ceiling 1 again — releasing must not promote past the parent.
				approveHold(t, db, wsID, userID, approvalID, "approved")
				lvl = readLevel("after approval")
				if autonomyRank(lvl) > autonomyRank(parent) {
					t.Fatalf("approving promoted the child to %q under a %q creator", lvl, parent)
				}
			}
		})
	}
}

// ── releasing a hold (the happy path) ───────────────────────────────────────

// decideHold posts a decision through the real POST /approvals/{id}/decide
// handler and hands back the recorder, so a test can assert on a REFUSED
// decision (409) as well as an accepted one. approveHold is the fatal-on-error
// variant for the common case.
func decideHold(t *testing.T, db *sql.DB, wsID, userID, approvalID, status string) *httptest.ResponseRecorder {
	t.Helper()
	ah := NewApprovalsHandler(db, testLogger(), nil)
	req := withWorkspaceUser(httptest.NewRequest(http.MethodPost,
		"/api/v1/approvals/"+approvalID+"/decide",
		strings.NewReader(`{"status":"`+status+`"}`)), userID, wsID, "OWNER")
	req.SetPathValue("id", approvalID)
	rr := httptest.NewRecorder()
	ah.Decide(rr, req)
	return rr
}

// TestAutonomyGate_Approve_ReleasesEveryHeldArtefact is the happy path of the
// whole gate, in one place, per artefact.
//
// A gate that cannot be OPENED is a worse outage than a gate that never
// closed: every held crew, agent, mission and schedule would be stuck forever
// and nothing would be red. The per-artefact tests above each release their
// own hold, so this is not the only coverage — it is the one that fails with a
// message naming the release path when a shared change to
// applyAutonomyGateDecisionTx breaks one arm.
//
// Note the levels: mission and schedule are no longer held at `guided` after
// the matrix rebalance, so each arm is set up at whatever still holds it —
// strict for missions, an unwired resolver for schedules.
func TestAutonomyGate_Approve_ReleasesEveryHeldArtefact(t *testing.T) {
	t.Run("crew", func(t *testing.T) {
		db, wsID, crewID, userID, res := autonomyRig(t, "trusted")
		h := gatedInternalHandler(t, db, res)
		rr := httptest.NewRecorder()
		h.CreateCrew(rr, boundInternalReq(http.MethodPost, "/?workspace_id="+wsID, `{"name":"Rel"}`, wsID, crewID))
		body := decodeGateBody(t, rr.Body.Bytes())
		childID, _ := body["id"].(string)
		approvalID, _ := body["approval_id"].(string)

		approveHold(t, db, wsID, userID, approvalID, "approved")

		var lvl string
		if err := db.QueryRow(`SELECT autonomy_level FROM crews WHERE id = ?`, childID).Scan(&lvl); err != nil {
			t.Fatalf("read released crew: %v", err)
		}
		if lvl != "trusted" {
			t.Fatalf("released crew autonomy_level = %q, want trusted — approving must restore the "+
				"CREATING crew's level, neither leaving it pinned to strict nor promoting it past the parent", lvl)
		}
		if n := gateCountRows(t, db,
			`SELECT COUNT(*) FROM inbox_items WHERE source_id = ? AND state = 'resolved'`, childID); n != 1 {
			t.Errorf("approving must resolve the blocking waitpoint; got %d resolved rows", n)
		}
	})

	t.Run("agent", func(t *testing.T) {
		db, wsID, crewID, userID, res := autonomyRig(t, "guided")
		h := gatedInternalHandler(t, db, res)
		rr := httptest.NewRecorder()
		h.CreateAgent(rr, boundInternalReq(http.MethodPost, "/?workspace_id="+wsID,
			`{"crew_id":"`+crewID+`","name":"Rel"}`, wsID, crewID))
		body := decodeGateBody(t, rr.Body.Bytes())
		agentID, _ := body["id"].(string)
		approvalID, _ := body["approval_id"].(string)

		approveHold(t, db, wsID, userID, approvalID, "approved")

		var status string
		if err := db.QueryRow(`SELECT status FROM agents WHERE id = ?`, agentID).Scan(&status); err != nil {
			t.Fatalf("read released agent: %v", err)
		}
		if status != "IDLE" {
			t.Fatalf("released agent status = %q, want IDLE — it can still not serve a message", status)
		}
	})

	t.Run("mission", func(t *testing.T) {
		// strict: the only level that still holds a mission.
		db, wsID, crewID, userID, res := autonomyRig(t, "strict")
		leadID := seedAgentRow(t, db, "lead-rel", wsID, crewID, "Lead", "lead-rel", "LEAD")
		h := NewInternalMissionHandler(db, nil, nil, testLogger())
		h.SetAutonomyGate(res, nil)
		rr := httptest.NewRecorder()
		h.Create(rr, boundInternalReq(http.MethodPost, "/",
			`{"title":"Rel","lead_agent_id":"`+leadID+`","crew_id":"`+crewID+
				`","workspace_id":"`+wsID+`"}`, wsID, crewID))
		body := decodeGateBody(t, rr.Body.Bytes())
		missionID, _ := body["id"].(string)
		approvalID, _ := body["approval_id"].(string)

		start := func() int {
			req := boundInternalReq(http.MethodPost, "/?workspace_id="+wsID, "", wsID, crewID)
			req.SetPathValue("missionId", missionID)
			srr := httptest.NewRecorder()
			h.Start(srr, req)
			return srr.Code
		}
		if code := start(); code != http.StatusForbidden {
			t.Fatalf("start before approval = %d, want 403", code)
		}
		approveHold(t, db, wsID, userID, approvalID, "approved")
		if code := start(); code != http.StatusOK {
			t.Fatalf("start after approval = %d, want 200 — the hold never opens", code)
		}
	})

	t.Run("schedule", func(t *testing.T) {
		// Unwired resolver: the only remaining route to a held schedule.
		db, wsID, crewID, userID, _ := autonomyRig(t, "guided")
		seedPipelineRow(t, db, wsID, "pl-rel", "rel-pipe")
		a := gatedRoutineAdapter(t, db, nil)
		rr := httptest.NewRecorder()
		a.CreateSchedule(rr, boundInternalReq(http.MethodPost, "/?workspace_id="+wsID,
			`{"cron_expr":"*/5 * * * *","target_pipeline_slug":"rel-pipe"}`, wsID, crewID))
		body := decodeGateBody(t, rr.Body.Bytes())
		schedID, _ := body["id"].(string)
		approvalID, _ := body["approval_id"].(string)

		approveHold(t, db, wsID, userID, approvalID, "approved")

		var enabled int
		if err := db.QueryRow(`SELECT enabled FROM pipeline_schedules WHERE id = ?`, schedID).Scan(&enabled); err != nil {
			t.Fatalf("read released schedule: %v", err)
		}
		if enabled != 1 {
			t.Fatalf("released schedule enabled = %d, want 1 — the scheduler will never pick it up", enabled)
		}
	})
}

// TestAutonomyGate_Decide_IsTerminal pins that a decided hold cannot be
// decided again — in either direction.
//
// The release is not idempotent in itself (the crew arm writes an absolute
// level, the agent arm a conditional UPDATE), so what has to hold is that
// Decide never runs it twice. harbormaster.DecideTx is the chokepoint: it
// answers ErrNotPending, Decide turns that into 409, and the side effect is
// never reached because it rides the same transaction. The cases that matter
// are the ones where a second decision would UNDO the first: approve-then-deny
// (would it re-pin the crew?) and deny/timeout-then-approve (would a refused
// or lapsed hold resurrect?).
func TestAutonomyGate_Decide_IsTerminal(t *testing.T) {
	newHeldCrew := func(t *testing.T) (*sql.DB, string, string, string, string, *InternalHandler) {
		t.Helper()
		db, wsID, crewID, userID, res := autonomyRig(t, "trusted")
		h := gatedInternalHandler(t, db, res)
		rr := httptest.NewRecorder()
		h.CreateCrew(rr, boundInternalReq(http.MethodPost, "/?workspace_id="+wsID, `{"name":"Twice"}`, wsID, crewID))
		if rr.Code != http.StatusAccepted {
			t.Fatalf("setup: create = %d, want 202; body=%s", rr.Code, rr.Body.String())
		}
		body := decodeGateBody(t, rr.Body.Bytes())
		childID, _ := body["id"].(string)
		approvalID, _ := body["approval_id"].(string)
		return db, wsID, userID, childID, approvalID, h
	}
	level := func(t *testing.T, db *sql.DB, id string) string {
		t.Helper()
		var lvl string
		if err := db.QueryRow(`SELECT autonomy_level FROM crews WHERE id = ?`, id).Scan(&lvl); err != nil {
			t.Fatalf("read crew level: %v", err)
		}
		return lvl
	}

	t.Run("approve twice does not double-apply", func(t *testing.T) {
		db, wsID, userID, childID, approvalID, _ := newHeldCrew(t)
		approveHold(t, db, wsID, userID, approvalID, "approved")
		if got := level(t, db, childID); got != "trusted" {
			t.Fatalf("after first approve: %q, want trusted", got)
		}
		second := decideHold(t, db, wsID, userID, approvalID, "approved")
		if second.Code != http.StatusConflict {
			t.Errorf("second approve = %d, want 409 (already decided); body=%s", second.Code, second.Body.String())
		}
		if got := level(t, db, childID); got != "trusted" {
			t.Errorf("second approve changed the crew to %q; a decided hold must be terminal", got)
		}
	})

	t.Run("deny after approve cannot re-pin", func(t *testing.T) {
		db, wsID, userID, childID, approvalID, _ := newHeldCrew(t)
		approveHold(t, db, wsID, userID, approvalID, "approved")
		rr := decideHold(t, db, wsID, userID, approvalID, "denied")
		if rr.Code != http.StatusConflict {
			t.Errorf("deny after approve = %d, want 409; body=%s", rr.Code, rr.Body.String())
		}
		if got := level(t, db, childID); got != "trusted" {
			t.Errorf("a late deny re-pinned the released crew to %q", got)
		}
	})

	t.Run("approve after deny cannot resurrect", func(t *testing.T) {
		db, wsID, userID, childID, approvalID, _ := newHeldCrew(t)
		approveHold(t, db, wsID, userID, approvalID, "denied")
		if got := level(t, db, childID); got != "strict" {
			t.Fatalf("denied crew is %q, want strict (denied = stays inert)", got)
		}
		rr := decideHold(t, db, wsID, userID, approvalID, "approved")
		if rr.Code != http.StatusConflict {
			t.Errorf("approve after deny = %d, want 409; body=%s", rr.Code, rr.Body.String())
		}
		if got := level(t, db, childID); got != "strict" {
			t.Errorf("approve after deny released the crew to %q — a refused hold must stay refused", got)
		}
	})

	t.Run("approve after timeout cannot resurrect", func(t *testing.T) {
		db, wsID, userID, childID, approvalID, _ := newHeldCrew(t)
		// What harbormaster's sweeper does to an unattended hold.
		execOrFatal(t, db, `UPDATE approvals_queue SET status = 'timeout' WHERE id = ?`, approvalID)
		rr := decideHold(t, db, wsID, userID, approvalID, "approved")
		if rr.Code != http.StatusConflict {
			t.Errorf("approve after timeout = %d, want 409; body=%s", rr.Code, rr.Body.String())
		}
		if got := level(t, db, childID); got != "strict" {
			t.Errorf("approving a lapsed hold released the crew to %q — the gate must fail closed", got)
		}
	})
}

// jsonString quotes s for embedding in a hand-built JSON body.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
