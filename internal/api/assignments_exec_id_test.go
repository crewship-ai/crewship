package api

// Coverage for PRD-ISSUES-AND-ROUTINES-2026 work package B7 ("Hard
// termination (Tier 2)", #2356): runAssignment must persist the heavy
// agent exec's ExecID and container id onto the assignment row the moment
// the exec starts (via orchestrator.AgentRunRequest.OnExecStarted), so a
// later `stop --hard` can resolve it back to a pid. This is the "run row"
// half of B7 — issue_hard_stop_test.go covers the signalling half.

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/orchestrator"
)

func TestRunAssignment_PersistsExecIDAndContainerID(t *testing.T) {
	h, wsID, crewID, leadID, workerID, chatID := covAsgRig(t)
	h.orch = orchestrator.New(inbandAsgProvider{stream: `{"type":"result","subtype":"success","result":"done"}` + "\n"}, newInbandAsgState(), newTestLogger())
	insertAssignment(t, h.db, "asg-execid-1", wsID, chatID, leadID, workerID, "PENDING")

	h.runAssignment(context.Background(), "asg-execid-1", createAssignmentBody{
		TargetSlug: "asg-worker", Task: "t", CrewID: crewID, WorkspaceID: wsID, ChatID: chatID,
	}, targetAgentInfo{ID: workerID, Slug: "asg-worker", Name: "Worker", CrewSlug: "asg"})

	var execID, containerID string
	if err := h.db.QueryRow(
		`SELECT COALESCE(exec_id,''), COALESCE(exec_container_id,'') FROM assignments WHERE id = 'asg-execid-1'`).
		Scan(&execID, &containerID); err != nil {
		t.Fatalf("query assignment: %v", err)
	}
	// inbandAsgProvider.Exec returns ExecID "agent-exec" for the heavy agent
	// exec (discriminated by a non-empty Env, exactly like the setup execs
	// it tells apart) and EnsureCrewRuntime returns "container-inband".
	if execID != "agent-exec" {
		t.Errorf("assignments.exec_id = %q, want %q", execID, "agent-exec")
	}
	if containerID != "container-inband" {
		t.Errorf("assignments.exec_container_id = %q, want %q", containerID, "container-inband")
	}
}

// TestRunAssignment_NoContainerProvider_NeverSetsExecID is the control: a
// run that never reaches an exec (no orchestrator) must not leave a stale
// or fabricated exec_id behind.
func TestRunAssignment_NoContainerProvider_NeverSetsExecID(t *testing.T) {
	h, wsID, crewID, leadID, workerID, chatID := covAsgRig(t)
	h.orch = orchestrator.New(nil, newInbandAsgState(), newTestLogger())
	insertAssignment(t, h.db, "asg-execid-2", wsID, chatID, leadID, workerID, "PENDING")

	h.runAssignment(context.Background(), "asg-execid-2", createAssignmentBody{
		TargetSlug: "asg-worker", Task: "t", CrewID: crewID, WorkspaceID: wsID, ChatID: chatID,
	}, targetAgentInfo{ID: workerID, Slug: "asg-worker", Name: "Worker", CrewSlug: "asg"})

	var execID string
	if err := h.db.QueryRow(
		`SELECT COALESCE(exec_id,'') FROM assignments WHERE id = 'asg-execid-2'`).Scan(&execID); err != nil {
		t.Fatalf("query assignment: %v", err)
	}
	if execID != "" {
		t.Errorf("assignments.exec_id = %q, want empty (this run never reached an exec)", execID)
	}
}
