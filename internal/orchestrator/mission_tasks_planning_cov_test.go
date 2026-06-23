package orchestrator

// Coverage tests for mission_tasks_planning.go: ApproveTask validation and
// approve/reject flows, checkApprovalGate config branches, and
// dispatchLeadPlanning's passive-lead / missing-lead / dispatch-failure
// behavior.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestApproveTask_RequiresUserID(t *testing.T) {
	t.Parallel()
	e := newLifecycleEngine(t, covMissionDB(t))
	err := e.ApproveTask(context.Background(), "t1", "", true, "")
	if err == nil || !strings.Contains(err.Error(), "userID is required") {
		t.Fatalf("expected userID error, got %v", err)
	}
}

func TestApproveTask_LookupError(t *testing.T) {
	t.Parallel()
	e := newLifecycleEngine(t, covMissionDB(t))
	err := e.ApproveTask(context.Background(), "no-such-task", "u1", true, "")
	if err == nil || !strings.Contains(err.Error(), "lookup task") {
		t.Fatalf("expected lookup error, got %v", err)
	}
}

func TestApproveTask_MissionNotInProgress(t *testing.T) {
	t.Parallel()
	db := covMissionDB(t)
	covSeed(t, db)
	covMission(t, db, "m1", "REVIEW")
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db, `INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t1', 'm1', 'agent-worker', 'Held', 'AWAITING_APPROVAL', 1, '[]', ?, ?)`, now, now)
	e := newLifecycleEngine(t, db)
	err := e.ApproveTask(context.Background(), "t1", "u1", true, "")
	if !errors.Is(err, ErrInvalidTaskStatus) || !strings.Contains(err.Error(), "mission is REVIEW") {
		t.Fatalf("expected mission-status error, got %v", err)
	}
}

func TestApproveTask_TaskNotAwaitingApproval(t *testing.T) {
	t.Parallel()
	db := covMissionDB(t)
	covSeed(t, db)
	covMission(t, db, "m1", "IN_PROGRESS")
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db, `INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t1', 'm1', 'agent-worker', 'Running', 'IN_PROGRESS', 1, '[]', ?, ?)`, now, now)
	e := newLifecycleEngine(t, db)
	err := e.ApproveTask(context.Background(), "t1", "u1", true, "")
	if !errors.Is(err, ErrInvalidTaskStatus) || !strings.Contains(err.Error(), "expected AWAITING_APPROVAL") {
		t.Fatalf("expected task-status error, got %v", err)
	}
}

func TestApproveTask_ApproveUnblocksDependents(t *testing.T) {
	t.Parallel()
	db := covMissionDB(t)
	covSeed(t, db)
	ms := covMission(t, db, "m1", "IN_PROGRESS")
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db, `INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t1', 'm1', 'agent-worker', 'Held', 'AWAITING_APPROVAL', 1, '[]', ?, ?)`, now, now)
	mustExec(t, db, `INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t2', 'm1', 'agent-worker', 'Downstream', 'BLOCKED', 2, '["t1"]', ?, ?)`, now, now)

	e := newLifecycleEngine(t, db)
	e.mu.Lock()
	e.active["m1"] = ms
	e.mu.Unlock()

	if err := e.ApproveTask(context.Background(), "t1", "user-7", true, "looks good"); err != nil {
		t.Fatalf("ApproveTask: %v", err)
	}

	var status, approvalStatus, approvedBy, notes string
	if err := db.QueryRow(`SELECT status, approval_status, approved_by, evaluation_notes FROM mission_tasks WHERE id = 't1'`).
		Scan(&status, &approvalStatus, &approvedBy, &notes); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if status != "COMPLETED" || approvalStatus != "APPROVED" || approvedBy != "user-7" || notes != "looks good" {
		t.Errorf("approval not persisted: status=%q approval=%q by=%q notes=%q", status, approvalStatus, approvedBy, notes)
	}
	if got := covTaskStatus(t, db, "t2"); got != "PENDING" {
		t.Errorf("dependent task = %q, want PENDING after approval", got)
	}
	// Mission must stay IN_PROGRESS — t2 is still pending.
	if got := covMissionStatus(t, db, "m1"); got != "IN_PROGRESS" {
		t.Errorf("mission = %q, want IN_PROGRESS", got)
	}

	// Second approval attempt must fail — status no longer AWAITING_APPROVAL.
	if err := e.ApproveTask(context.Background(), "t1", "user-8", true, ""); !errors.Is(err, ErrInvalidTaskStatus) {
		t.Errorf("double approval must fail with ErrInvalidTaskStatus, got %v", err)
	}
}

func TestApproveTask_RejectFailsTaskAndDependents(t *testing.T) {
	t.Parallel()
	db := covMissionDB(t)
	covSeed(t, db)
	ms := covMission(t, db, "m1", "IN_PROGRESS")
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db, `INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t1', 'm1', 'agent-worker', 'Held', 'AWAITING_APPROVAL', 1, '[]', ?, ?)`, now, now)
	mustExec(t, db, `INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t2', 'm1', 'agent-worker', 'Downstream', 'BLOCKED', 2, '["t1"]', ?, ?)`, now, now)

	e := newLifecycleEngine(t, db)
	e.mu.Lock()
	e.active["m1"] = ms
	e.mu.Unlock()

	if err := e.ApproveTask(context.Background(), "t1", "user-7", false, "not good"); err != nil {
		t.Fatalf("ApproveTask(reject): %v", err)
	}
	var status, approvalStatus string
	db.QueryRow(`SELECT status, approval_status FROM mission_tasks WHERE id = 't1'`).Scan(&status, &approvalStatus)
	if status != "FAILED" || approvalStatus != "REJECTED" {
		t.Errorf("rejected task: status=%q approval=%q", status, approvalStatus)
	}
	if got := covTaskStatus(t, db, "t2"); got != "FAILED" {
		t.Errorf("dependent of rejected task = %q, want FAILED", got)
	}
	var errMsg string
	db.QueryRow(`SELECT error_message FROM mission_tasks WHERE id = 't2'`).Scan(&errMsg)
	if errMsg != "upstream task rejected" {
		t.Errorf("cascade reason = %q", errMsg)
	}
	// Both tasks terminal + one failed → completion check flips mission FAILED.
	if got := covMissionStatus(t, db, "m1"); got != "FAILED" {
		t.Errorf("mission = %q, want FAILED after rejection cascade", got)
	}
}

// ---- checkApprovalGate ----

func TestCheckApprovalGate_Branches(t *testing.T) {
	t.Parallel()

	newGateDB := func(t *testing.T, escalationCfg string, confidence any, approvalRequired int) (*MissionEngine, *missionState) {
		t.Helper()
		db := covMissionDB(t)
		covSeed(t, db)
		ms := covMission(t, db, "m1", "IN_PROGRESS")
		if escalationCfg != "" {
			mustExec(t, db, `UPDATE crews SET escalation_config = ? WHERE id = 'crew-1'`, escalationCfg)
		}
		now := time.Now().UTC().Format(time.RFC3339)
		mustExec(t, db, `INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, confidence, approval_required, created_at, updated_at)
			VALUES ('t1', 'm1', 'agent-worker', 'Gated', 'IN_PROGRESS', 1, '[]', ?, ?, ?, ?)`,
			confidence, approvalRequired, now, now)
		e := newLifecycleEngine(t, db)
		e.mu.Lock()
		e.active["m1"] = ms
		e.mu.Unlock()
		return e, ms
	}

	t.Run("missing task completes", func(t *testing.T) {
		t.Parallel()
		e := newLifecycleEngine(t, covMissionDB(t))
		if got := e.checkApprovalGate(context.Background(), "ghost", "m1"); got != "COMPLETED" {
			t.Errorf("got %q, want COMPLETED on lookup failure", got)
		}
	})

	t.Run("invalid escalation config ignored", func(t *testing.T) {
		t.Parallel()
		e, _ := newGateDB(t, `{broken`, 0.2, 0)
		if got := e.checkApprovalGate(context.Background(), "t1", "m1"); got != "COMPLETED" {
			t.Errorf("got %q, want COMPLETED when config is malformed", got)
		}
	})

	t.Run("auto approve above threshold", func(t *testing.T) {
		t.Parallel()
		e, _ := newGateDB(t, `{"auto_approve_threshold":0.9,"notify_threshold":0.8,"require_approval_below":0.5}`, 0.95, 1)
		// approval_required=1 would normally hold — auto-approve wins first.
		if got := e.checkApprovalGate(context.Background(), "t1", "m1"); got != "COMPLETED" {
			t.Errorf("got %q, want COMPLETED via auto-approve", got)
		}
	})

	t.Run("explicit flag holds", func(t *testing.T) {
		t.Parallel()
		e, _ := newGateDB(t, "", nil, 1)
		if got := e.checkApprovalGate(context.Background(), "t1", "m1"); got != "AWAITING_APPROVAL" {
			t.Errorf("got %q, want AWAITING_APPROVAL via explicit flag", got)
		}
	})

	t.Run("low confidence holds", func(t *testing.T) {
		t.Parallel()
		e, _ := newGateDB(t, `{"auto_approve_threshold":0.9,"notify_threshold":0.8,"require_approval_below":0.5}`, 0.3, 0)
		if got := e.checkApprovalGate(context.Background(), "t1", "m1"); got != "AWAITING_APPROVAL" {
			t.Errorf("got %q, want AWAITING_APPROVAL below threshold", got)
		}
	})

	t.Run("mid confidence notifies but completes", func(t *testing.T) {
		t.Parallel()
		e, _ := newGateDB(t, `{"auto_approve_threshold":0.9,"notify_threshold":0.8,"require_approval_below":0.5}`, 0.7, 0)
		if got := e.checkApprovalGate(context.Background(), "t1", "m1"); got != "COMPLETED" {
			t.Errorf("got %q, want COMPLETED in notify band", got)
		}
	})
}

// ---- dispatchLeadPlanning ----

func TestDispatchLeadPlanning_PassiveLeadSkips(t *testing.T) {
	t.Parallel()
	db := covMissionDB(t)
	covSeed(t, db)
	mustExec(t, db, `UPDATE agents SET lead_mode = 'passive' WHERE id = 'agent-lead'`)
	ms := covMission(t, db, "m1", "IN_PROGRESS")
	e := newLifecycleEngine(t, db)
	d := newCovDispatcher(nil)
	e.SetDispatcher(d)

	if err := e.dispatchLeadPlanning(context.Background(), ms); err != nil {
		t.Fatalf("passive lead must be a nil no-op, got %v", err)
	}
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM assignments`).Scan(&count)
	if count != 0 {
		t.Errorf("passive lead must not create assignments, got %d", count)
	}
	select {
	case req := <-d.ch:
		t.Errorf("passive lead must not dispatch, got %+v", req)
	default:
	}
}

func TestDispatchLeadPlanning_MissingLeadErrors(t *testing.T) {
	t.Parallel()
	db := covMissionDB(t)
	covSeed(t, db)
	ms := covMission(t, db, "m1", "IN_PROGRESS")
	ms.LeadAgentID = "agent-ghost"
	e := newLifecycleEngine(t, db)
	err := e.dispatchLeadPlanning(context.Background(), ms)
	if err == nil || !strings.Contains(err.Error(), "resolve lead agent") {
		t.Fatalf("expected resolve error, got %v", err)
	}
}

func TestDispatchLeadPlanning_DispatchFailureResetsFlag(t *testing.T) {
	t.Parallel()
	db := covMissionDB(t)
	covSeed(t, db)
	ms := covMission(t, db, "m1", "IN_PROGRESS")
	ms.planningDispatched = true
	e := newLifecycleEngine(t, db)
	d := newCovDispatcher(errors.New("lead container unavailable"))
	e.SetDispatcher(d)

	if err := e.dispatchLeadPlanning(context.Background(), ms); err != nil {
		t.Fatalf("dispatchLeadPlanning: %v", err)
	}
	select {
	case <-d.ch:
	case <-time.After(5 * time.Second):
		t.Fatal("dispatcher never invoked")
	}
	// The error goroutine resets planningDispatched so the loop retries.
	deadline := time.Now().Add(3 * time.Second)
	for {
		e.mu.Lock()
		v := ms.planningDispatched
		e.mu.Unlock()
		if !v {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("planningDispatched was never reset after dispatch failure")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
