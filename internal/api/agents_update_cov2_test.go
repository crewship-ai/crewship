package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// agents_update_cov2_test.go — remaining Update branches: the
// edit-gate DB error (MANAGER path hits the DB), lead-promotion
// demote failure, crew validation DB error, the UPDATE failure, and
// the scheduleUpdater notification arms (cron-only and enabled-only
// PATCHes, with the DB fallback reads and the warn-on-error path).
// Helpers prefixed covAU2.

type covAU2Sched struct {
	calls []struct {
		agentID, cron, prompt string
		enabled               bool
	}
	err error
}

func (s *covAU2Sched) UpdateSchedule(_ context.Context, agentID, cron, prompt string, enabled bool) error {
	s.calls = append(s.calls, struct {
		agentID, cron, prompt string
		enabled               bool
	}{agentID, cron, prompt, enabled})
	return s.err
}

func covAU2Fixture(t *testing.T) (*AgentHandler, string, string, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "covau2-crew", wsID, "Crew", "covau2-crew")
	agentID := seedAgentRow(t, db, "covau2-ag", wsID, crewID, "Agent", "covau2-ag", "AGENT")
	return NewAgentHandler(db, newTestLogger()), userID, wsID, crewID, agentID
}

func covAU2Patch(h *AgentHandler, userID, wsID, agentID, role string, body map[string]any) *httptest.ResponseRecorder {
	req := withWorkspaceUser(
		httptest.NewRequest("PATCH", "/api/v1/agents/"+agentID, jsonBody(body)),
		userID, wsID, role)
	req.SetPathValue("agentId", agentID)
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	return rr
}

func TestCovAU2_EditGateDBError_500(t *testing.T) {
	h, userID, wsID, _, agentID := covAU2Fixture(t)
	h.db.Close()
	rr := covAU2Patch(h, userID, wsID, agentID, "MANAGER", map[string]any{"name": "X"})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovAU2_PromoteLead_DemoteFails_500(t *testing.T) {
	h, userID, wsID, crewID, agentID := covAU2Fixture(t)
	seedAgentRow(t, h.db, "covau2-lead", wsID, crewID, "Lead", "covau2-lead", "LEAD")
	execOrFatal(t, h.db, `CREATE TRIGGER covau2_block_upd BEFORE UPDATE ON agents
		BEGIN SELECT RAISE(ABORT, 'covau2 forced'); END`)
	rr := covAU2Patch(h, userID, wsID, agentID, "OWNER", map[string]any{"agent_role": "LEAD"})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
	// Existing lead must keep its role (demote was blocked).
	var role string
	if err := h.db.QueryRow(`SELECT agent_role FROM agents WHERE id = 'covau2-lead'`).Scan(&role); err != nil || role != "LEAD" {
		t.Errorf("existing lead role = %q err=%v, want LEAD", role, err)
	}
}

func TestCovAU2_CrewExistsDBError_500(t *testing.T) {
	h, userID, wsID, _, agentID := covAU2Fixture(t)
	execOrFatal(t, h.db, `ALTER TABLE crews RENAME TO crews_broken`)
	rr := covAU2Patch(h, userID, wsID, agentID, "OWNER", map[string]any{"crew_id": "covau2-crew"})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovAU2_UpdateExecError_500(t *testing.T) {
	h, userID, wsID, _, agentID := covAU2Fixture(t)
	execOrFatal(t, h.db, `CREATE TRIGGER covau2_block_upd2 BEFORE UPDATE ON agents
		BEGIN SELECT RAISE(ABORT, 'covau2 forced'); END`)
	rr := covAU2Patch(h, userID, wsID, agentID, "OWNER", map[string]any{"name": "Renamed"})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

// TestCovAU2_Scheduler_CronOnlyPatch_ReadsEnabledFromDB — PATCHing
// schedule_cron alone makes the handler read schedule_enabled from the
// DB before notifying the scheduler; a failing scheduler only warns.
func TestCovAU2_Scheduler_CronOnlyPatch_ReadsEnabledFromDB(t *testing.T) {
	h, userID, wsID, _, agentID := covAU2Fixture(t)
	execOrFatal(t, h.db, `UPDATE agents SET schedule_enabled = 1 WHERE id = ?`, agentID)
	sched := &covAU2Sched{err: contextCanceledErrForCovAU2()}
	h.SetScheduler(sched)

	rr := covAU2Patch(h, userID, wsID, agentID, "OWNER", map[string]any{
		"schedule_cron":   "0 9 * * 1",
		"schedule_prompt": "weekly standup",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (scheduler failure is warn-only); body=%s",
			rr.Code, rr.Body.String())
	}
	if len(sched.calls) != 1 {
		t.Fatalf("scheduler calls = %d, want 1", len(sched.calls))
	}
	c := sched.calls[0]
	if c.agentID != agentID || c.cron != "0 9 * * 1" || c.prompt != "weekly standup" || !c.enabled {
		t.Errorf("scheduler call = %+v, want cron+prompt with enabled from DB", c)
	}
}

// TestCovAU2_Scheduler_EnabledOnlyPatch_ReadsCronFromDB — PATCHing
// schedule_enabled alone pulls the stored cron + prompt.
func TestCovAU2_Scheduler_EnabledOnlyPatch_ReadsCronFromDB(t *testing.T) {
	h, userID, wsID, _, agentID := covAU2Fixture(t)
	execOrFatal(t, h.db,
		`UPDATE agents SET schedule_cron = '30 8 * * *', schedule_prompt = 'daily brief' WHERE id = ?`,
		agentID)
	sched := &covAU2Sched{}
	h.SetScheduler(sched)

	rr := covAU2Patch(h, userID, wsID, agentID, "OWNER", map[string]any{"schedule_enabled": true})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	if len(sched.calls) != 1 {
		t.Fatalf("scheduler calls = %d, want 1", len(sched.calls))
	}
	c := sched.calls[0]
	if c.cron != "30 8 * * *" || c.prompt != "daily brief" || !c.enabled {
		t.Errorf("scheduler call = %+v, want stored cron/prompt with enabled=true", c)
	}
}

// contextCanceledErrForCovAU2 returns a non-nil error for the scheduler
// stub without importing errors twice in this file's scope.
func contextCanceledErrForCovAU2() error { return context.Canceled }
