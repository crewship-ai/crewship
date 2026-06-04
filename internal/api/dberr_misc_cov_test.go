package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// dberr_misc_cov_test.go exercises the DB-ERROR (500) branches of handlers
// not covered elsewhere. The technique: seed any rows needed to clear the
// 400/403/404 guards, build a fully valid request, then db.Close() right
// before invoking the handler so the FIRST query the handler reaches
// returns "sql: database is closed" → 500.
//
// All helpers introduced here are prefixed covDBM; all test funcs TestCovDBM.

// covDBMIDs returns stable user/workspace IDs used to populate request
// context. No rows are actually seeded: the DB is closed before the
// handler runs, so the first query fails before any guard that would
// need the row to exist.
func covDBMIDs() (userID, wsID string) {
	return "covdbm-user", "covdbm-ws"
}

func covDBMAssert500(t *testing.T, rr *httptest.ResponseRecorder) {
	t.Helper()
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

// ---------------------------------------------------------------------------
// escalation_handler.go
// ---------------------------------------------------------------------------

func TestCovDBM_CreateEscalation_DBErr(t *testing.T) {
	db := setupTestDB(t)
	logger := newTestLogger()
	db.Close()

	h := NewQueryHandler(db, nil, nil, "", logger)
	req := httptest.NewRequest("POST", "/api/v1/internal/escalations", jsonBody(map[string]string{
		"from_slug":    "eva",
		"reason":       "need creds",
		"crew_id":      "crew-1",
		"workspace_id": "ws-1",
		"chat_id":      "chat-1",
	}))
	rr := httptest.NewRecorder()
	h.CreateEscalation(rr, req)
	covDBMAssert500(t, rr)
}

func TestCovDBM_ResolveEscalation_DBErr(t *testing.T) {
	userID, wsID := covDBMIDs()
	db := setupTestDB(t) // fresh open just to construct; we close it
	logger := newTestLogger()
	db.Close()

	h := NewQueryHandler(db, nil, nil, "", logger)
	req := httptest.NewRequest("PATCH", "/api/v1/escalations/esc-1/resolve",
		jsonBody(map[string]string{"resolution": "approved", "action": "approve"}))
	req.SetPathValue("escalationId", "esc-1")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ResolveEscalation(rr, req)
	covDBMAssert500(t, rr)
}

func TestCovDBM_ListEscalations_DBErr(t *testing.T) {
	userID, wsID := covDBMIDs()
	db := setupTestDB(t)
	logger := newTestLogger()
	db.Close()

	h := NewQueryHandler(db, nil, nil, "", logger)
	req := httptest.NewRequest("GET", "/api/v1/crews/crew-1/escalations", nil)
	req.SetPathValue("crewId", "crew-1")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ListEscalations(rr, req)
	covDBMAssert500(t, rr)
}

// ---------------------------------------------------------------------------
// mcp_audit.go — List
// ---------------------------------------------------------------------------

func TestCovDBM_MCPAuditList_DBErr(t *testing.T) {
	userID, wsID := covDBMIDs()
	db := setupTestDB(t)
	logger := newTestLogger()
	db.Close()

	h := NewMCPAuditHandler(db, logger)
	req := httptest.NewRequest("GET", "/api/v1/mcp/audit", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	covDBMAssert500(t, rr)
}

// ---------------------------------------------------------------------------
// query_handler.go — ListPeerConversations
// ---------------------------------------------------------------------------

func TestCovDBM_ListPeerConversations_DBErr(t *testing.T) {
	userID, wsID := covDBMIDs()
	db := setupTestDB(t)
	logger := newTestLogger()
	db.Close()

	h := NewQueryHandler(db, nil, nil, "", logger)
	req := httptest.NewRequest("GET", "/api/v1/crews/crew-1/peer-conversations", nil)
	req.SetPathValue("crewId", "crew-1")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ListPeerConversations(rr, req)
	covDBMAssert500(t, rr)
}

// ---------------------------------------------------------------------------
// standup_handler.go — Standup (internal route, no workspace ctx → reaches
// fetchStandupConversations against the closed DB).
// ---------------------------------------------------------------------------

func TestCovDBM_Standup_DBErr(t *testing.T) {
	db := setupTestDB(t)
	logger := newTestLogger()
	db.Close()

	h := NewQueryHandler(db, nil, nil, "", logger)
	req := httptest.NewRequest("GET", "/api/v1/internal/standup?crew_id=crew-1", nil)
	rr := httptest.NewRecorder()
	h.Standup(rr, req)
	covDBMAssert500(t, rr)
}

// ---------------------------------------------------------------------------
// eval_handler.go — Replay / Regression / ListRuns 500
// ---------------------------------------------------------------------------

func TestCovDBM_EvalListRuns_DBErr(t *testing.T) {
	userID, wsID := covDBMIDs()
	db := setupTestDB(t)
	logger := newTestLogger()
	db.Close()

	h := NewEvalHandler(db, logger)
	req := httptest.NewRequest("GET", "/api/v1/eval/runs", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ListRuns(rr, req)
	covDBMAssert500(t, rr)
}

func TestCovDBM_EvalReplay_DBErr(t *testing.T) {
	userID, wsID := covDBMIDs()
	db := setupTestDB(t)
	logger := newTestLogger()
	db.Close()

	h := NewEvalHandler(db, logger)
	req := httptest.NewRequest("POST", "/api/v1/eval/replay",
		jsonBody(map[string]any{"mission_id": "m-1", "seed": 7}))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Replay(rr, req)
	covDBMAssert500(t, rr)
}

func TestCovDBM_EvalRegression_DBErr(t *testing.T) {
	userID, wsID := covDBMIDs()
	db := setupTestDB(t)
	logger := newTestLogger()
	db.Close()

	h := NewEvalHandler(db, logger)
	req := httptest.NewRequest("POST", "/api/v1/eval/regression",
		jsonBody(map[string]string{"baseline_mission_id": "m-1", "candidate_mission_id": "m-2"}))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Regression(rr, req)
	covDBMAssert500(t, rr)
}

// ---------------------------------------------------------------------------
// cartographer_handler.go — List / Get / Fork / Delete 500
// ---------------------------------------------------------------------------

func TestCovDBM_CartographerList_DBErr(t *testing.T) {
	userID, wsID := covDBMIDs()
	db := setupTestDB(t)
	logger := newTestLogger()
	db.Close()

	h := NewCartographerHandler(db, logger)
	req := httptest.NewRequest("GET", "/api/v1/missions/m-1/checkpoints", nil)
	req.SetPathValue("missionId", "m-1")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	covDBMAssert500(t, rr)
}

func TestCovDBM_CartographerGet_DBErr(t *testing.T) {
	userID, wsID := covDBMIDs()
	db := setupTestDB(t)
	logger := newTestLogger()
	db.Close()

	h := NewCartographerHandler(db, logger)
	req := httptest.NewRequest("GET", "/api/v1/checkpoints/cp-1", nil)
	req.SetPathValue("id", "cp-1")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	covDBMAssert500(t, rr)
}

func TestCovDBM_CartographerFork_DBErr(t *testing.T) {
	userID, wsID := covDBMIDs()
	db := setupTestDB(t)
	logger := newTestLogger()
	db.Close()

	h := NewCartographerHandler(db, logger)
	req := httptest.NewRequest("POST", "/api/v1/checkpoints/cp-1/fork",
		jsonBody(map[string]string{"label": "fork-1"}))
	req.SetPathValue("id", "cp-1")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Fork(rr, req)
	covDBMAssert500(t, rr)
}

func TestCovDBM_CartographerDelete_DBErr(t *testing.T) {
	userID, wsID := covDBMIDs()
	db := setupTestDB(t)
	logger := newTestLogger()
	db.Close()

	h := NewCartographerHandler(db, logger)
	req := httptest.NewRequest("DELETE", "/api/v1/checkpoints/cp-1", nil)
	req.SetPathValue("id", "cp-1")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	covDBMAssert500(t, rr)
}

// ---------------------------------------------------------------------------
// crew_messaging.go — ListMessages 500
// ---------------------------------------------------------------------------

func TestCovDBM_CrewMessagingListMessages_DBErr(t *testing.T) {
	db := setupTestDB(t)
	logger := newTestLogger()
	db.Close()

	h := NewCrewMessagingHandler(db, t.TempDir(), logger)
	req := httptest.NewRequest("GET", "/api/v1/internal/crew-messages?crew_id=crew-1", nil)
	rr := httptest.NewRecorder()
	h.ListMessages(rr, req)
	covDBMAssert500(t, rr)
}

// ---------------------------------------------------------------------------
// agent_learning.go — Get / Patch 500
// ---------------------------------------------------------------------------

func TestCovDBM_LearningGet_DBErr(t *testing.T) {
	userID, wsID := covDBMIDs()
	db := setupTestDB(t)
	logger := newTestLogger()
	db.Close()

	h := NewLearningHandler(db, logger)
	req := httptest.NewRequest("GET", "/api/v1/agents/agent-1/learning", nil)
	req.SetPathValue("agentId", "agent-1")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	covDBMAssert500(t, rr)
}

func TestCovDBM_LearningPatch_DBErr(t *testing.T) {
	userID, wsID := covDBMIDs()
	db := setupTestDB(t)
	logger := newTestLogger()
	db.Close()

	h := NewLearningHandler(db, logger)
	enabled := true
	req := httptest.NewRequest("PATCH", "/api/v1/agents/agent-1/learning",
		jsonBody(map[string]any{"enabled": enabled, "reason": "enable for audit"}))
	req.SetPathValue("agentId", "agent-1")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Patch(rr, req)
	covDBMAssert500(t, rr)
}

// ---------------------------------------------------------------------------
// task_state.go — Restart / Resume / Clone 500
// ---------------------------------------------------------------------------

func TestCovDBM_TaskRestart_DBErr(t *testing.T) {
	userID, wsID := covDBMIDs()
	db := setupTestDB(t)
	logger := newTestLogger()
	db.Close()

	h := NewMissionHandler(db, nil, nil, logger)
	req := httptest.NewRequest("POST", "/api/v1/crews/crew-1/missions/m-1/restart", nil)
	req.SetPathValue("crewId", "crew-1")
	req.SetPathValue("missionId", "m-1")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Restart(rr, req)
	covDBMAssert500(t, rr)
}

func TestCovDBM_TaskResume_DBErr(t *testing.T) {
	userID, wsID := covDBMIDs()
	db := setupTestDB(t)
	logger := newTestLogger()
	db.Close()

	h := NewMissionHandler(db, nil, nil, logger)
	req := httptest.NewRequest("POST", "/api/v1/crews/crew-1/missions/m-1/resume", nil)
	req.SetPathValue("crewId", "crew-1")
	req.SetPathValue("missionId", "m-1")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Resume(rr, req)
	covDBMAssert500(t, rr)
}

func TestCovDBM_TaskClone_DBErr(t *testing.T) {
	userID, wsID := covDBMIDs()
	db := setupTestDB(t)
	logger := newTestLogger()
	db.Close()

	h := NewMissionHandler(db, nil, nil, logger)
	req := httptest.NewRequest("POST", "/api/v1/crews/crew-1/missions/m-1/clone", nil)
	req.SetPathValue("crewId", "crew-1")
	req.SetPathValue("missionId", "m-1")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Clone(rr, req)
	covDBMAssert500(t, rr)
}

// ---------------------------------------------------------------------------
// mission_handler.go — Metrics 500
// ---------------------------------------------------------------------------

func TestCovDBM_MissionMetrics_DBErr(t *testing.T) {
	userID, wsID := covDBMIDs()
	db := setupTestDB(t)
	logger := newTestLogger()
	db.Close()

	h := NewMissionHandler(db, nil, nil, logger)
	req := httptest.NewRequest("GET", "/api/v1/missions/metrics", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Metrics(rr, req)
	covDBMAssert500(t, rr)
}

// ---------------------------------------------------------------------------
// crew_members.go — ListMembers / AddMember 500
// ---------------------------------------------------------------------------

func TestCovDBM_CrewListMembers_DBErr(t *testing.T) {
	userID, wsID := covDBMIDs()
	db := setupTestDB(t)
	logger := newTestLogger()
	db.Close()

	h := NewCrewHandler(db, logger)
	req := httptest.NewRequest("GET", "/api/v1/crews/crew-1/members", nil)
	req.SetPathValue("crewId", "crew-1")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ListMembers(rr, req)
	covDBMAssert500(t, rr)
}

func TestCovDBM_CrewAddMember_DBErr(t *testing.T) {
	userID, wsID := covDBMIDs()
	db := setupTestDB(t)
	logger := newTestLogger()
	db.Close()

	h := NewCrewHandler(db, logger)
	req := httptest.NewRequest("POST", "/api/v1/crews/crew-1/members",
		jsonBody(map[string]string{"user_id": "u-2"}))
	req.SetPathValue("crewId", "crew-1")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.AddMember(rr, req)
	covDBMAssert500(t, rr)
}

// ---------------------------------------------------------------------------
// pipelines_crud.go — List / Get / Delete 500
// ---------------------------------------------------------------------------

func TestCovDBM_PipelineList_DBErr(t *testing.T) {
	userID, wsID := covDBMIDs()
	db := setupTestDB(t)
	logger := newTestLogger()
	h := NewPipelineHandler(db, logger, nil, nil)
	db.Close()

	req := httptest.NewRequest("GET", "/api/v1/workspaces/ws-1/pipelines", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	covDBMAssert500(t, rr)
}

func TestCovDBM_PipelineGet_DBErr(t *testing.T) {
	userID, wsID := covDBMIDs()
	db := setupTestDB(t)
	logger := newTestLogger()
	h := NewPipelineHandler(db, logger, nil, nil)
	db.Close()

	req := httptest.NewRequest("GET", "/api/v1/workspaces/ws-1/pipelines/slug-1", nil)
	req.SetPathValue("slug", "slug-1")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	covDBMAssert500(t, rr)
}

func TestCovDBM_PipelineDelete_DBErr(t *testing.T) {
	userID, wsID := covDBMIDs()
	db := setupTestDB(t)
	logger := newTestLogger()
	h := NewPipelineHandler(db, logger, nil, nil)
	db.Close()

	req := httptest.NewRequest("DELETE", "/api/v1/workspaces/ws-1/pipelines/slug-1", nil)
	req.SetPathValue("slug", "slug-1")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	covDBMAssert500(t, rr)
}
