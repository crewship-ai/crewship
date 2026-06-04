package api

// Remaining-branch coverage for five handlers that already have earlier
// *_test.go / *_cov_test.go files:
//
//   mission_handler.go       (Metrics)
//   task_state.go            (Restart / Resume / Clone)
//   agents_hire.go           (Hire / Rehire)
//   workspaces_membership.go (AddMember / RemoveMember / ListMembers /
//                             ListInvitations / CreateInvitation)
//   agent_config.go          (loadAgentData + resolver helpers)
//
// The existing files cover the primary happy paths, the RBAC gates, the
// 400/404/409 validation branches, and the tenant-isolation guards. This
// file fills the gaps they LEFT: additional happy sub-branches, edge
// inputs, and — the bulk of the new coverage — the DB-error 500 paths,
// driven by closing the *sql.DB before invoking an otherwise-valid
// request (fault injection). setupTestDB already registers a t.Cleanup
// Close, and database/sql.Close is idempotent, so an explicit Close here
// is safe.
//
// SKIPPED (documented, not tested here):
//   - missionEngine != nil branches in Start/Restart/Resume (ValidateDAG,
//     StartMission, rollback-on-engine-failure). MissionHandler.missionEngine
//     is the concrete *orchestrator.MissionEngine, not an interface, so a
//     test in package api can only pass nil. Exercising the non-nil edges
//     would require a live orchestrator (Docker/container runtime) and
//     belongs in an orchestrator integration test.
//   - license.CheckMemberLimit PaymentRequired branch in AddMember /
//     CreateInvitation. WorkspaceHandler.license is a concrete
//     *license.License; constructing a member-limit-exceeding license is an
//     internal/license concern, out of scope for handler coverage.
//
// RULES: package api; reuse existing rig/seed helpers; new helpers prefixed
// covRem; every test func prefixed TestCovRem.

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/policy"
)

// -----------------------------------------------------------------------------
// covRem helpers
// -----------------------------------------------------------------------------

// covRemMissionHandler builds a MissionHandler (nil hub + nil engine) plus a
// seeded user/workspace/crew/lead. Mirrors newMissionHandlerForTasks but
// returns the raw *sql.DB so fault-injection tests can Close it.
func covRemMissionHandler(t *testing.T) (*MissionHandler, *sql.DB, string, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-covrem", wsID, "CovRem", "covrem")
	seedAgentRow(t, db, "lead-covrem", wsID, crewID, "Lead", "lead-covrem", "LEAD")
	h := NewMissionHandler(db, nil, nil, newTestLogger())
	return h, db, userID, wsID, crewID
}

// covRemMissionReq builds a request carrying the given role + path values.
func covRemMissionReq(userID, wsID, role, crewID, missionID string) *http.Request {
	req := httptest.NewRequest("POST", "/", nil)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", missionID)
	return req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, role))
}

// covRemSeedMission inserts a mission + synthetic chat with the given status.
func covRemSeedMission(t *testing.T, db *sql.DB, id, wsID, crewID, leadID, status string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES (?, ?, ?, 'MISSION', 'ACTIVE')`,
		id, leadID, wsID); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'M', ?, datetime('now'), datetime('now'))`,
		id, wsID, crewID, leadID, "trace-"+id, status); err != nil {
		t.Fatalf("seed mission: %v", err)
	}
}

// covRemSeedTask inserts a mission_tasks row.
func covRemSeedTask(t *testing.T, db *sql.DB, id, missionID, status string, order int, dependsOn string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO mission_tasks (id, mission_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, datetime('now'), datetime('now'))`,
		id, missionID, "Task "+id, status, order, dependsOn); err != nil {
		t.Fatalf("seed task %s: %v", id, err)
	}
}

// -----------------------------------------------------------------------------
// mission_handler.go — Metrics
// -----------------------------------------------------------------------------

// Empty workspace: every aggregate query COALESCEs to 0; status 200.
func TestCovRemMissionMetrics_EmptyWorkspace(t *testing.T) {
	h, _, userID, wsID, _ := covRemMissionHandler(t)
	req := httptest.NewRequest("GET", "/api/v1/mission-metrics", nil)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "MEMBER"))
	rr := httptest.NewRecorder()
	h.Metrics(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var m map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["total_missions"].(float64) != 0 {
		t.Errorf("total_missions = %v, want 0", m["total_missions"])
	}
}

// Populated aggregates: a COMPLETED mission within 24h plus a FAILED one,
// each carrying tasks with tokens/cost/completion so the token/cost,
// avg-completion, and task-stat queries return non-zero rows.
func TestCovRemMissionMetrics_PopulatedAggregates(t *testing.T) {
	h, db, userID, wsID, crewID := covRemMissionHandler(t)
	leadID := "lead-covrem"

	// COMPLETED mission inside the 24h window with created/completed stamps.
	covRemSeedMission(t, db, "m-done", wsID, crewID, leadID, "COMPLETED")
	if _, err := db.Exec(`UPDATE missions SET created_at = datetime('now','-1 hour'), completed_at = datetime('now') WHERE id = 'm-done'`); err != nil {
		t.Fatalf("stamp completed mission: %v", err)
	}
	// FAILED mission updated inside the window.
	covRemSeedMission(t, db, "m-fail", wsID, crewID, leadID, "FAILED")
	if _, err := db.Exec(`UPDATE missions SET updated_at = datetime('now') WHERE id = 'm-fail'`); err != nil {
		t.Fatalf("stamp failed mission: %v", err)
	}
	// In-progress mission so active_missions is non-zero.
	covRemSeedMission(t, db, "m-active", wsID, crewID, leadID, "IN_PROGRESS")

	// A completed task in the window with tokens + cost on the done mission.
	covRemSeedTask(t, db, "t-done", "m-done", "COMPLETED", 0, "[]")
	if _, err := db.Exec(`UPDATE mission_tasks SET completed_at = datetime('now'), tokens_used = 500, estimated_cost = 0.25 WHERE id = 't-done'`); err != nil {
		t.Fatalf("stamp completed task: %v", err)
	}
	// A failed task in the window on the failed mission.
	covRemSeedTask(t, db, "t-fail", "m-fail", "FAILED", 0, "[]")
	if _, err := db.Exec(`UPDATE mission_tasks SET updated_at = datetime('now') WHERE id = 't-fail'`); err != nil {
		t.Fatalf("stamp failed task: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/mission-metrics", nil)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "MEMBER"))
	rr := httptest.NewRecorder()
	h.Metrics(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var m map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["total_missions"].(float64) != 3 {
		t.Errorf("total_missions = %v, want 3", m["total_missions"])
	}
	if m["completed_24h"].(float64) != 1 {
		t.Errorf("completed_24h = %v, want 1", m["completed_24h"])
	}
	if m["failed_24h"].(float64) != 1 {
		t.Errorf("failed_24h = %v, want 1", m["failed_24h"])
	}
	if m["total_tokens_24h"].(float64) != 500 {
		t.Errorf("total_tokens_24h = %v, want 500", m["total_tokens_24h"])
	}
	if m["tasks_completed_24h"].(float64) != 1 {
		t.Errorf("tasks_completed_24h = %v, want 1", m["tasks_completed_24h"])
	}
	if m["tasks_failed_24h"].(float64) != 1 {
		t.Errorf("tasks_failed_24h = %v, want 1", m["tasks_failed_24h"])
	}
}

// DB closed before the first metrics query → 500. The handler's fallback
// CASE query also fails on the closed DB, so the totals branch returns the
// 500 path (not the SQLite-FILTER fallback).
func TestCovRemMissionMetrics_DBError500(t *testing.T) {
	h, db, userID, wsID, _ := covRemMissionHandler(t)
	db.Close()
	req := httptest.NewRequest("GET", "/api/v1/mission-metrics", nil)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "MEMBER"))
	rr := httptest.NewRecorder()
	h.Metrics(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", rr.Code, rr.Body.String())
	}
}

// -----------------------------------------------------------------------------
// task_state.go — Restart
// -----------------------------------------------------------------------------

// Happy restart of a COMPLETED mission whose only task already completed:
// the task stays COMPLETED, mission flips to PLANNING. Exercises the
// COMPLETED-stays-completed branch of the reset UPDATE plus
// unblockCompletedDeps with no blocked tasks (engine nil).
func TestCovRemRestart_CompletedTaskStaysCompleted(t *testing.T) {
	h, db, userID, wsID, crewID := covRemMissionHandler(t)
	covRemSeedMission(t, db, "m-r", wsID, crewID, "lead-covrem", "COMPLETED")
	covRemSeedTask(t, db, "t-keep", "m-r", "COMPLETED", 0, "[]")
	covRemSeedTask(t, db, "t-reset", "m-r", "FAILED", 1, `["t-keep"]`)

	rr := httptest.NewRecorder()
	h.Restart(rr, covRemMissionReq(userID, wsID, "OWNER", crewID, "m-r"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	var keepStatus, resetStatus string
	if err := db.QueryRow(`SELECT status FROM mission_tasks WHERE id = 't-keep'`).Scan(&keepStatus); err != nil {
		t.Fatalf("read t-keep: %v", err)
	}
	if keepStatus != "COMPLETED" {
		t.Errorf("t-keep status = %q, want COMPLETED (completed tasks are not reset)", keepStatus)
	}
	// t-reset depends on the (still) COMPLETED t-keep, so unblockCompletedDeps
	// should leave it PENDING, not BLOCKED.
	if err := db.QueryRow(`SELECT status FROM mission_tasks WHERE id = 't-reset'`).Scan(&resetStatus); err != nil {
		t.Fatalf("read t-reset: %v", err)
	}
	if resetStatus != "PENDING" {
		t.Errorf("t-reset status = %q, want PENDING (dep already completed)", resetStatus)
	}
}

// DB closed before the claim UPDATE → 500 on the "claim mission" branch.
func TestCovRemRestart_DBError500(t *testing.T) {
	h, db, userID, wsID, crewID := covRemMissionHandler(t)
	covRemSeedMission(t, db, "m-r5", wsID, crewID, "lead-covrem", "COMPLETED")
	db.Close()
	rr := httptest.NewRecorder()
	h.Restart(rr, covRemMissionReq(userID, wsID, "OWNER", crewID, "m-r5"))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", rr.Code, rr.Body.String())
	}
}

// -----------------------------------------------------------------------------
// task_state.go — Resume
// -----------------------------------------------------------------------------

// Resume a FAILED mission where a FAILED task has a BLOCKED downstream
// dependent: the BFS cascade resets both, and because the dependency is
// itself reset (not COMPLETED) the dependent comes back BLOCKED. Exercises
// the reverse-dep cascade + the BLOCKED-initial-status branch (engine nil).
func TestCovRemResume_CascadesDownstreamDependent(t *testing.T) {
	h, db, userID, wsID, crewID := covRemMissionHandler(t)
	covRemSeedMission(t, db, "m-res", wsID, crewID, "lead-covrem", "FAILED")
	covRemSeedTask(t, db, "t-failed", "m-res", "FAILED", 0, "[]")
	covRemSeedTask(t, db, "t-child", "m-res", "BLOCKED", 1, `["t-failed"]`)

	rr := httptest.NewRecorder()
	h.Resume(rr, covRemMissionReq(userID, wsID, "OWNER", crewID, "m-res"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["reset_tasks"].(float64) != 2 {
		t.Errorf("reset_tasks = %v, want 2 (failed + cascaded child)", resp["reset_tasks"])
	}
	var childStatus string
	if err := db.QueryRow(`SELECT status FROM mission_tasks WHERE id = 't-child'`).Scan(&childStatus); err != nil {
		t.Fatalf("read t-child: %v", err)
	}
	if childStatus != "BLOCKED" {
		t.Errorf("t-child status = %q, want BLOCKED (dep was reset, not completed)", childStatus)
	}
	// Mission should be back to IN_PROGRESS (engine nil → no rollback fires).
	var mStatus string
	if err := db.QueryRow(`SELECT status FROM missions WHERE id = 'm-res'`).Scan(&mStatus); err != nil {
		t.Fatalf("read mission: %v", err)
	}
	if mStatus != "IN_PROGRESS" {
		t.Errorf("mission status = %q, want IN_PROGRESS", mStatus)
	}
}

// Resume where the reset task's dependency is already COMPLETED → the task
// comes back PENDING (the all-deps-complete branch of resetStatusMap).
func TestCovRemResume_DependencyCompletedYieldsPending(t *testing.T) {
	h, db, userID, wsID, crewID := covRemMissionHandler(t)
	covRemSeedMission(t, db, "m-res2", wsID, crewID, "lead-covrem", "FAILED")
	covRemSeedTask(t, db, "t-ok", "m-res2", "COMPLETED", 0, "[]")
	covRemSeedTask(t, db, "t-broke", "m-res2", "FAILED", 1, `["t-ok"]`)

	rr := httptest.NewRecorder()
	h.Resume(rr, covRemMissionReq(userID, wsID, "OWNER", crewID, "m-res2"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var brokeStatus string
	if err := db.QueryRow(`SELECT status FROM mission_tasks WHERE id = 't-broke'`).Scan(&brokeStatus); err != nil {
		t.Fatalf("read t-broke: %v", err)
	}
	if brokeStatus != "PENDING" {
		t.Errorf("t-broke status = %q, want PENDING (dep already COMPLETED)", brokeStatus)
	}
}

// DB closed before the claim UPDATE → 500 on the "claim mission for resume"
// branch.
func TestCovRemResume_DBError500(t *testing.T) {
	h, db, userID, wsID, crewID := covRemMissionHandler(t)
	covRemSeedMission(t, db, "m-res5", wsID, crewID, "lead-covrem", "FAILED")
	db.Close()
	rr := httptest.NewRecorder()
	h.Resume(rr, covRemMissionReq(userID, wsID, "OWNER", crewID, "m-res5"))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", rr.Code, rr.Body.String())
	}
}

// -----------------------------------------------------------------------------
// task_state.go — Clone
// -----------------------------------------------------------------------------

// Clone a mission that has zero tasks: the origTasks loop is empty, the new
// mission + synthetic chat are still created, status 201.
func TestCovRemClone_NoTasks(t *testing.T) {
	h, db, userID, wsID, crewID := covRemMissionHandler(t)
	covRemSeedMission(t, db, "m-clone0", wsID, crewID, "lead-covrem", "COMPLETED")

	rr := httptest.NewRecorder()
	h.Clone(rr, covRemMissionReq(userID, wsID, "OWNER", crewID, "m-clone0"))
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["status"] != "PLANNING" {
		t.Errorf("clone status = %q, want PLANNING", resp["status"])
	}
	// A new mission row should exist with title "M (copy)".
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM missions WHERE id = ? AND title = 'M (copy)'`, resp["id"]).Scan(&n); err != nil {
		t.Fatalf("verify clone row: %v", err)
	}
	if n != 1 {
		t.Errorf("clone mission rows = %d, want 1", n)
	}
}

// DB closed before the original-mission SELECT → 500.
func TestCovRemClone_DBError500(t *testing.T) {
	h, db, userID, wsID, crewID := covRemMissionHandler(t)
	covRemSeedMission(t, db, "m-clone5", wsID, crewID, "lead-covrem", "COMPLETED")
	db.Close()
	rr := httptest.NewRecorder()
	h.Clone(rr, covRemMissionReq(userID, wsID, "OWNER", crewID, "m-clone5"))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", rr.Code, rr.Body.String())
	}
}

// -----------------------------------------------------------------------------
// agents_hire.go — Hire
// -----------------------------------------------------------------------------

// Malformed JSON body → 400 (the readJSON branch).
func TestCovRemHire_BadJSON(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, _ := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)

	req := httptest.NewRequest("POST", "/api/v1/agents/hire", strings.NewReader(`{NOPE`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "MANAGER"))
	rr := httptest.NewRecorder()
	h.Hire(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
}

// Both crew_id and crew_slug set → 400 mutually-exclusive branch.
func TestCovRemHire_CrewIDAndSlugMutuallyExclusive(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)
	rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
		"crew_id":       crewID,
		"crew_slug":     "hire-crew",
		"template_slug": "docs-writer",
		"reason":        "ambiguous crew ref",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
}

// Missing template_slug → 400.
func TestCovRemHire_MissingTemplate(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)
	rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
		"crew_id": crewID,
		"reason":  "no template",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
}

// crew_slug resolution path (crewLookupB == "slug") with an explicit model
// and a parent_lead_id in the same crew → 201, parent edge persisted.
func TestCovRemHire_BySlugWithParentLead(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	// A LEAD in the same crew to parent the ephemeral.
	seedAgentRow(t, db, "lead-hire", wsID, crewID, "Lead", "lead-hire", "LEAD")
	h := newHireHandler(t, db)

	rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
		"crew_slug":      "hire-crew",
		"template_slug":  "docs-writer",
		"reason":         "slug + parent",
		"model":          "claude-opus-4-7",
		"parent_lead_id": "lead-hire",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}
	var resp hireResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var parent sql.NullString
	if err := db.QueryRow(`SELECT parent_lead_id FROM agents WHERE id = ?`, resp.ID).Scan(&parent); err != nil {
		t.Fatalf("read parent: %v", err)
	}
	if !parent.Valid || parent.String != "lead-hire" {
		t.Errorf("parent_lead_id = %v, want lead-hire", parent)
	}
	// Provider should have been inferred from the claude-* model.
	var provider sql.NullString
	if err := db.QueryRow(`SELECT llm_provider FROM agents WHERE id = ?`, resp.ID).Scan(&provider); err != nil {
		t.Fatalf("read provider: %v", err)
	}
	if !provider.Valid || provider.String != "ANTHROPIC" {
		t.Errorf("llm_provider = %v, want ANTHROPIC", provider)
	}
}

// parent_lead_id pointing at an AGENT (not a LEAD) → 400.
func TestCovRemHire_ParentLeadNotALead(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	seedAgentRow(t, db, "plain-agent", wsID, crewID, "Plain", "plain-agent", "AGENT")
	h := newHireHandler(t, db)
	rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
		"crew_id":        crewID,
		"template_slug":  "docs-writer",
		"reason":         "bad parent",
		"parent_lead_id": "plain-agent",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
}

// parent_lead_id is a LEAD but in a different crew → 400 (cross-crew guard).
func TestCovRemHire_ParentLeadDifferentCrew(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	// Second crew in the same workspace, with its own LEAD.
	otherCrew := seedCrewRow(t, db, "crew-other-hire", wsID, "Other", "other-hire")
	seedAgentRow(t, db, "lead-other", wsID, otherCrew, "LeadO", "lead-other", "LEAD")
	h := newHireHandler(t, db)
	rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
		"crew_id":        crewID,
		"template_slug":  "docs-writer",
		"reason":         "cross-crew parent",
		"parent_lead_id": "lead-other",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
}

// Missing template (unknown slug) → 404.
func TestCovRemHire_TemplateNotFound404(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)
	rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
		"crew_id":       crewID,
		"template_slug": "does-not-exist",
		"reason":        "missing template",
	})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", rr.Code, rr.Body.String())
	}
}

// "full" autonomy → 201, journal-only, NO inbox row (DecisionAutoLogJournal
// / AutoJournal switch arm has no inbox write).
func TestCovRemHire_FullAutonomyJournalOnly(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "full", 5)
	h := newHireHandler(t, db)
	rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
		"crew_id":       crewID,
		"template_slug": "docs-writer",
		"reason":        "full autonomy hire",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}
	var resp hireResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	var inbox int
	_ = db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE source_id = ?`, resp.ID).Scan(&inbox)
	if inbox != 0 {
		t.Errorf("inbox rows = %d on full autonomy, want 0 (journal only)", inbox)
	}
}

// TTL above the 24h cap is clamped to maxHireTTLMinutes; a huge value still
// succeeds (no 400) and expires within ~24h of now.
func TestCovRemHire_TTLClampedToMax(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)
	rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
		"crew_id":       crewID,
		"template_slug": "docs-writer",
		"reason":        "huge ttl",
		"ttl_minutes":   999999,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}
	var resp hireResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	exp, err := time.Parse(time.RFC3339, *resp.ExpiresAt)
	if err != nil {
		t.Fatalf("parse expires_at: %v", err)
	}
	// maxHireTTLMinutes == 24h; allow a minute of slack.
	if exp.After(time.Now().Add(24*time.Hour + time.Minute)) {
		t.Errorf("expires_at %v exceeds the 24h cap", exp)
	}
}

// DB closed before the crew lookup → 500 (load-crew branch).
func TestCovRemHire_DBError500(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)
	db.Close()
	rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
		"crew_id":       crewID,
		"template_slug": "docs-writer",
		"reason":        "db down",
	})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", rr.Code, rr.Body.String())
	}
}

// -----------------------------------------------------------------------------
// agents_hire.go — Rehire
// -----------------------------------------------------------------------------

// Malformed JSON → 400.
func TestCovRemRehire_BadJSON(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	agentID := seedEphemeralAgent(t, db, wsID, crewID, "reh-eph-aaa", nil, nil, "[x] hire: y")
	h := newHireHandler(t, db)

	req := httptest.NewRequest("POST", "/api/v1/agents/"+agentID+"/rehire", strings.NewReader(`{BAD`))
	req.SetPathValue("agentId", agentID)
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "MANAGER"))
	rr := httptest.NewRecorder()
	h.Rehire(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
}

// Missing reason → 400.
func TestCovRemRehire_MissingReason(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	agentID := seedEphemeralAgent(t, db, wsID, crewID, "reh-eph-bbb", nil, nil, "[x] hire: y")
	h := newHireHandler(t, db)
	rr := postRehire(t, h, userID, wsID, "MANAGER", agentID, map[string]any{"ttl_minutes": 30})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
}

// Rehire of a still-LIVE ephemeral (expired_at NULL) skips the quota path
// entirely (wasGhost == false) and succeeds even when at the configured max,
// because extending a live agent does not add a slot. Exercises the
// wasGhost-false branch.
func TestCovRemRehire_LiveAgentSkipsQuota(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 1)
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	// One live ephemeral fills the quota of 1; rehiring IT (still live)
	// must still pass because it doesn't claim a new slot.
	agentID := seedEphemeralAgent(t, db, wsID, crewID, "live-eph-zzz", &future, nil, "[x] hire: live")
	h := newHireHandler(t, db)

	rr := postRehire(t, h, userID, wsID, "MANAGER", agentID, map[string]any{
		"ttl_minutes": 120,
		"reason":      "extend the live agent",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var resp hireResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Status is echoed from the persisted row (IDLE from the seed).
	if resp.Status != "IDLE" {
		t.Errorf("status = %q, want IDLE (echoed from row)", resp.Status)
	}
}

// Rehire TTL of 0 clamps up to the default floor (defaultHireTTLMinutes).
func TestCovRemRehire_ZeroTTLClampsToDefault(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	past := "2024-01-01T00:00:00Z"
	agentID := seedEphemeralAgent(t, db, wsID, crewID, "reh-eph-ccc", &past, &past, "[x] hire: y")
	h := newHireHandler(t, db)

	before := time.Now().UTC()
	rr := postRehire(t, h, userID, wsID, "MANAGER", agentID, map[string]any{
		"ttl_minutes": 0,
		"reason":      "default ttl",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var resp hireResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	exp, err := time.Parse(time.RFC3339, *resp.ExpiresAt)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// Default floor is 30 minutes; the new expiry should be ~30 min out.
	wantMin := before.Add(time.Duration(defaultHireTTLMinutes-1) * time.Minute)
	wantMax := before.Add(time.Duration(defaultHireTTLMinutes+1) * time.Minute)
	if exp.Before(wantMin) || exp.After(wantMax) {
		t.Errorf("expires_at %v not within ~%dm default window", exp, defaultHireTTLMinutes)
	}
}

// DB closed before the agent load → 500.
func TestCovRemRehire_DBError500(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	agentID := seedEphemeralAgent(t, db, wsID, crewID, "reh-eph-ddd", nil, nil, "[x] hire: y")
	h := newHireHandler(t, db)
	db.Close()
	rr := postRehire(t, h, userID, wsID, "MANAGER", agentID, map[string]any{
		"ttl_minutes": 30,
		"reason":      "db down",
	})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", rr.Code, rr.Body.String())
	}
}

// -----------------------------------------------------------------------------
// workspaces_membership.go — DB-error 500 paths + remaining sub-branches
// -----------------------------------------------------------------------------

// ListMembers with the DB closed → 500.
func TestCovRemListMembers_DBError500(t *testing.T) {
	h, userID, wsID := membershipRig(t)
	h.db.Close()
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/workspaces/"+wsID+"/members", nil),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ListMembers(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", rr.Code, rr.Body.String())
	}
}

// AddMember with the DB closed (after passing role + validation) → 500 on
// the existing-member check query.
func TestCovRemAddMember_DBError500(t *testing.T) {
	h, userID, wsID := membershipRig(t)
	h.db.Close()
	body := strings.NewReader(`{"user_id":"someone","role":"MEMBER"}`)
	req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/members", body),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AddMember(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", rr.Code, rr.Body.String())
	}
}

// RemoveMember with the DB closed → 500 on the member-lookup query.
func TestCovRemRemoveMember_DBError500(t *testing.T) {
	h, userID, wsID := membershipRig(t)
	h.db.Close()
	req := withWorkspaceUser(httptest.NewRequest("DELETE", "/api/v1/workspaces/"+wsID+"/members/m1", nil),
		userID, wsID, "OWNER")
	req.SetPathValue("memberId", "m1")
	rr := httptest.NewRecorder()
	h.RemoveMember(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", rr.Code, rr.Body.String())
	}
}

// ListInvitations with the DB closed → 500.
func TestCovRemListInvitations_DBError500(t *testing.T) {
	h, userID, wsID := membershipRig(t)
	h.db.Close()
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/workspaces/"+wsID+"/invitations", nil),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ListInvitations(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", rr.Code, rr.Body.String())
	}
}

// CreateInvitation with the DB closed → 500 on the existing-member-by-email
// check.
func TestCovRemCreateInvitation_DBError500(t *testing.T) {
	h, userID, wsID := membershipRig(t)
	h.db.Close()
	body := strings.NewReader(`{"email":"new@example.com","role":"MEMBER"}`)
	req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/invitations", body),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CreateInvitation(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", rr.Code, rr.Body.String())
	}
}

// CreateInvitation for an email that already belongs to a member → 409
// (the existing-member-by-email conflict branch).
func TestCovRemCreateInvitation_ExistingMember409(t *testing.T) {
	h, userID, wsID := membershipRig(t)
	// The seeded OWNER has email test@example.com — invite that exact email.
	body := strings.NewReader(`{"email":"test@example.com","role":"MEMBER"}`)
	req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/invitations", body),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CreateInvitation(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", rr.Code, rr.Body.String())
	}
}

// CreateInvitation twice for the same fresh email → second is 409 on the
// active-invitation conflict branch.
func TestCovRemCreateInvitation_DuplicateActive409(t *testing.T) {
	h, userID, wsID := membershipRig(t)
	mk := func() *httptest.ResponseRecorder {
		body := strings.NewReader(`{"email":"dup@example.com","role":"MEMBER"}`)
		req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/invitations", body),
			userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.CreateInvitation(rr, req)
		return rr
	}
	if rr := mk(); rr.Code != http.StatusCreated {
		t.Fatalf("first invite status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}
	if rr := mk(); rr.Code != http.StatusConflict {
		t.Fatalf("second invite status = %d, want 409; body: %s", rr.Code, rr.Body.String())
	}
}

// CreateInvitation with an empty role defaults to MEMBER (the req.Role == ""
// branch), succeeding with role MEMBER persisted.
func TestCovRemCreateInvitation_EmptyRoleDefaultsToMember(t *testing.T) {
	h, userID, wsID := membershipRig(t)
	body := strings.NewReader(`{"email":"defaultrole@example.com"}`)
	req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/invitations", body),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CreateInvitation(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}
	var got invitationResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if got.Role != "MEMBER" {
		t.Errorf("default role = %q, want MEMBER", got.Role)
	}
}

// -----------------------------------------------------------------------------
// agent_config.go — loadAgentData + resolver helpers
// -----------------------------------------------------------------------------

// loadAgentData against a closed DB returns a non-nil error (not ErrNoRows),
// driving resolveAgentConfigWithOpener's 500 branch when surfaced via
// ResolveAgent.
func TestCovRemResolveAgent_DBError500(t *testing.T) {
	ensureEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "crew-cfg500", wsID, "Cfg", "cfg500")
	seedAgentRow(t, db, "agent-cfg500", wsID, "crew-cfg500", "A", "a-cfg500", "AGENT")
	h := NewInternalHandler(db, "tok", newTestLogger())
	db.Close()

	req := httptest.NewRequest("GET", "/api/v1/internal/agents/agent-cfg500/resolve", nil)
	req.SetPathValue("agentId", "agent-cfg500")
	w := httptest.NewRecorder()
	h.ResolveAgent(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", w.Code, w.Body.String())
	}
}

// resolveCrewMembers with a crewless agent returns the empty-slice
// short-circuit (data.crewID invalid).
func TestCovRemResolveCrewMembers_NoCrew(t *testing.T) {
	ensureEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedAgentRow(t, db, "agent-solo", wsID, "", "Solo", "solo", "AGENT")
	h := NewInternalHandler(db, "tok", newTestLogger())
	req := httptest.NewRequest("GET", "/x", nil)
	data, err := h.loadAgentData(req, "agent-solo")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	members, err := h.resolveCrewMembers(req, data, "agent-solo")
	if err != nil {
		t.Fatalf("resolveCrewMembers: %v", err)
	}
	if len(members) != 0 {
		t.Errorf("members = %d, want 0 (crewless agent)", len(members))
	}
}

// resolveCrewMembers for a LEAD agent with a peer member that has an enabled
// MCP binding → the LEAD-enrichment branch attaches an integration entry.
func TestCovRemResolveCrewMembers_LeadEnrichment(t *testing.T) {
	ensureEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "crew-lead-en", wsID, "LeadEn", "leaden")
	seedAgentRow(t, db, "lead-en", wsID, "crew-lead-en", "Lead", "lead-en", "LEAD")
	seedAgentRow(t, db, "member-en", wsID, "crew-lead-en", "Mem", "member-en", "AGENT")

	// A workspace MCP server + an enabled binding on the peer member.
	if _, err := db.Exec(`INSERT INTO workspace_mcp_servers (id, workspace_id, name, display_name, transport, command, enabled, created_at, updated_at)
		VALUES ('ws-mcp-en', ?, 'gh', 'GitHub', 'stdio', 'npx', 1, datetime('now'), datetime('now'))`, wsID); err != nil {
		t.Fatalf("seed ws mcp: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO agent_mcp_bindings (id, agent_id, mcp_server_id, mcp_server_scope, enabled)
		VALUES ('bind-en', 'member-en', 'ws-mcp-en', 'workspace', 1)`); err != nil {
		t.Fatalf("seed binding: %v", err)
	}

	h := NewInternalHandler(db, "tok", newTestLogger())
	req := httptest.NewRequest("GET", "/x", nil)
	data, err := h.loadAgentData(req, "lead-en")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	members, err := h.resolveCrewMembers(req, data, "lead-en")
	if err != nil {
		t.Fatalf("resolveCrewMembers: %v", err)
	}
	if len(members) != 1 {
		t.Fatalf("members = %d, want 1", len(members))
	}
	if len(members[0].Integrations) != 1 || members[0].Integrations[0].ServerName != "gh" {
		t.Errorf("integrations = %+v, want one entry for server 'gh'", members[0].Integrations)
	}
}

// resolveCrewMembers with the DB closed returns an error (the QueryContext
// error branch — non-fatal at the caller, but the helper surfaces it).
func TestCovRemResolveCrewMembers_DBError(t *testing.T) {
	ensureEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "crew-cm-err", wsID, "CM", "cmerr")
	seedAgentRow(t, db, "agent-cm-err", wsID, "crew-cm-err", "A", "a-cm-err", "AGENT")
	h := NewInternalHandler(db, "tok", newTestLogger())
	req := httptest.NewRequest("GET", "/x", nil)
	data, err := h.loadAgentData(req, "agent-cm-err")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	db.Close()
	if _, err := h.resolveCrewMembers(req, data, "agent-cm-err"); err == nil {
		t.Errorf("expected error from resolveCrewMembers on closed DB")
	}
}

// resolveAgentCredentials returns ACTIVE creds and skips a PENDING-sentinel
// row; a closed DB surfaces the query error.
func TestCovRemResolveAgentCredentials_ActiveOnly(t *testing.T) {
	ensureEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "crew-cred", wsID, "Cred", "cred")
	seedAgentRow(t, db, "agent-cred", wsID, "crew-cred", "A", "a-cred", "AGENT")

	encActive, err := encryption.Encrypt("real-token-value")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	// ACTIVE credential, assigned to the agent.
	if _, err := db.Exec(`INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, status, created_by)
		VALUES ('cr-active', ?, 'Tok', ?, 'API_KEY', 'ACTIVE', ?)`, wsID, encActive, userID); err != nil {
		t.Fatalf("seed active cred: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority)
		VALUES ('ac-1', 'agent-cred', 'cr-active', 'TOK', 0)`); err != nil {
		t.Fatalf("assign cred: %v", err)
	}
	// PENDING credential — filtered by the status='ACTIVE' SQL guard.
	if _, err := db.Exec(`INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, status, created_by)
		VALUES ('cr-pending', ?, 'Pend', ?, 'API_KEY', 'PENDING', ?)`, wsID, encActive, userID); err != nil {
		t.Fatalf("seed pending cred: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority)
		VALUES ('ac-2', 'agent-cred', 'cr-pending', 'PEND', 1)`); err != nil {
		t.Fatalf("assign pending cred: %v", err)
	}

	h := NewInternalHandler(db, "tok", newTestLogger())
	req := httptest.NewRequest("GET", "/x", nil)
	creds, err := h.resolveAgentCredentials(req, "agent-cred")
	if err != nil {
		t.Fatalf("resolveAgentCredentials: %v", err)
	}
	if len(creds) != 1 || creds[0].EnvVar != "TOK" || creds[0].Value != "real-token-value" {
		t.Errorf("creds = %+v, want exactly the ACTIVE TOK entry", creds)
	}

	// Closed DB → error branch.
	db.Close()
	if _, err := h.resolveAgentCredentials(req, "agent-cred"); err == nil {
		t.Errorf("expected error from resolveAgentCredentials on closed DB")
	}
}

// resolveInstalledSkills returns the SKILL.md blob for an enabled skill and
// surfaces a query error on a closed DB.
func TestCovRemResolveInstalledSkills(t *testing.T) {
	ensureEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "crew-sk", wsID, "Sk", "sk")
	seedAgentRow(t, db, "agent-sk", wsID, "crew-sk", "A", "a-sk", "AGENT")

	if _, err := db.Exec(`INSERT INTO skills (id, name, slug, display_name, category, source, content, description, vendor)
		VALUES ('sk-1', 'Deploy', 'deploy', 'Deploy', 'ops', 'custom', '# Deploy
body', 'Deploy things', 'acme')`); err != nil {
		t.Fatalf("seed skill: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO agent_skills (id, agent_id, skill_id, enabled) VALUES ('as-1', 'agent-sk', 'sk-1', 1)`); err != nil {
		t.Fatalf("assign skill: %v", err)
	}

	h := NewInternalHandler(db, "tok", newTestLogger())
	req := httptest.NewRequest("GET", "/x", nil)
	skills, err := h.resolveInstalledSkills(req, "agent-sk")
	if err != nil {
		t.Fatalf("resolveInstalledSkills: %v", err)
	}
	if len(skills) != 1 || skills[0].Slug != "deploy" {
		t.Fatalf("skills = %+v, want one 'deploy' entry", skills)
	}
	if !strings.Contains(skills[0].Content, "name: \"deploy\"") {
		t.Errorf("synthesised SKILL.md missing frontmatter name; got:\n%s", skills[0].Content)
	}

	db.Close()
	if _, err := h.resolveInstalledSkills(req, "agent-sk"); err == nil {
		t.Errorf("expected error from resolveInstalledSkills on closed DB")
	}
}

// lookupCrewNamesForWorkspace returns id→name for live crews, an empty map
// on a closed DB.
func TestCovRemLookupCrewNames(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "crew-n1", wsID, "Alpha", "alpha")
	seedCrewRow(t, db, "crew-n2", wsID, "Beta", "beta")

	h := NewInternalHandler(db, "tok", newTestLogger())
	req := httptest.NewRequest("GET", "/x", nil)
	names := h.lookupCrewNamesForWorkspace(req, wsID)
	if names["crew-n1"] != "Alpha" || names["crew-n2"] != "Beta" {
		t.Errorf("names = %v, want Alpha/Beta", names)
	}

	db.Close()
	if got := h.lookupCrewNamesForWorkspace(req, wsID); len(got) != 0 {
		t.Errorf("closed DB should yield empty map, got %v", got)
	}
}

// reconstructSKILLMD: content that already starts with frontmatter passes
// through verbatim; content without frontmatter gets a synthesised header,
// and a display_name equal to the slug is omitted.
func TestCovRemReconstructSKILLMD(t *testing.T) {
	withFrontmatter := "---\nname: x\n---\nbody"
	if got := reconstructSKILLMD("x", "v", "X", "desc", withFrontmatter); got != withFrontmatter {
		t.Errorf("frontmatter passthrough changed content:\n%s", got)
	}

	got := reconstructSKILLMD("deploy", "acme", "deploy", "ship\nit", "# Body")
	if !strings.HasPrefix(got, "---\n") {
		t.Errorf("expected synthesised frontmatter, got:\n%s", got)
	}
	if strings.Contains(got, "display_name:") {
		t.Errorf("display_name equal to slug should be omitted; got:\n%s", got)
	}
	// Newlines in the description are collapsed to spaces.
	if !strings.Contains(got, "description: \"ship it\"") {
		t.Errorf("description not collapsed/quoted; got:\n%s", got)
	}
	if !strings.Contains(got, "vendor: \"acme\"") {
		t.Errorf("vendor missing; got:\n%s", got)
	}
}

// yamlQuote escapes quotes and backslashes.
func TestCovRemYamlQuote(t *testing.T) {
	if got := yamlQuote(`a"b\c`); got != `"a\"b\\c"` {
		t.Errorf("yamlQuote = %q, want %q", got, `"a\"b\\c"`)
	}
	if got := yamlQuote("plain"); got != `"plain"` {
		t.Errorf("yamlQuote(plain) = %q", got)
	}
}

// Compile-time use of policy so the import stays referenced even if a future
// edit drops the only direct usage. (newHireHandler already calls
// policy.NewResolver, but keep this defensive like agents_hire_test.go.)
var _ = policy.DecisionInboxApprove
