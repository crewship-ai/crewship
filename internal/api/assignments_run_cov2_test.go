package api

// Second coverage pass for assignments_run.go. Targets the branches the
// first pass (assignments_run_cov_test.go) left out:
//
//   - Create's PR-F24 bound-token gates (workspace / crew / chat mismatch)
//   - the "assigner crew unresolvable" 403 and the connection-check DB error
//   - the mission-linked side effects (comment, assignee, activity, hub
//     broadcast) including the "Lead" name fallback and task truncation
//   - runAssignment past GetOrCreateContainer (fake container provider +
//     StopAccepting forces a fast, deterministic execution error)
//   - the backup-in-progress refusal
//   - finishAssignment's terminal-entry, queue-pump-error, mission-callback
//     and completion-comment branches
//
// No Docker, no network: the orchestrator gets a fake provider.ContainerProvider.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/ws"
)

// covAsg2Provider is a minimal ContainerProvider whose EnsureCrewRuntime
// always succeeds, so runAssignment proceeds past container creation.
type covAsg2Provider struct{}

func (covAsg2Provider) EnsureCrewRuntime(context.Context, provider.CrewConfig) (string, error) {
	return "container-asg2", nil
}
func (covAsg2Provider) StopCrewRuntime(context.Context, string) error   { return nil }
func (covAsg2Provider) RemoveCrewRuntime(context.Context, string) error { return nil }
func (covAsg2Provider) ContainerStatus(context.Context, string) (*provider.ContainerStatus, error) {
	return nil, nil
}
func (covAsg2Provider) ContainerStats(context.Context, string) (*provider.ContainerMetrics, error) {
	return nil, nil
}
func (covAsg2Provider) Exec(context.Context, provider.ExecConfig) (*provider.ExecResult, error) {
	return nil, errors.New("no exec in tests")
}
func (covAsg2Provider) ExecInspect(context.Context, string) (bool, int, error) {
	return false, 0, nil
}
func (covAsg2Provider) CrewContainerName(_ string, slug string) string { return "crew-" + slug }
func (covAsg2Provider) CopyToContainer(context.Context, string, string, io.Reader) error {
	return nil
}

var _ provider.ContainerProvider = covAsg2Provider{}

// covAsg2FailEmitter fails every Emit so the handler's journal warn/error
// branches run.
type covAsg2FailEmitter struct{}

func (covAsg2FailEmitter) Emit(context.Context, journal.Entry) (string, error) {
	return "", errors.New("journal down")
}
func (covAsg2FailEmitter) Flush(context.Context) error { return nil }

// covAsg2Callback records the OnAssignmentCompleted invocation and fails.
type covAsg2Callback struct {
	gotID, gotStatus string
}

func (c *covAsg2Callback) OnAssignmentCompleted(_ context.Context, id, status, _, _ string) error {
	c.gotID, c.gotStatus = id, status
	return errors.New("callback boom")
}

func covAsg2Hub(t *testing.T) *ws.Hub {
	t.Helper()
	return ws.NewHub(newTestLogger(), nil, ws.NopValidatorForTests, ws.NopSessionsForTests)
}

// covAsg2Post sends the Create request with an optional bound-token
// workspace stamped into the context.
func covAsg2Post(t *testing.T, h *AssignmentHandler, body, boundWS string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/internal/assignments", strings.NewReader(body))
	if boundWS != "" {
		req = req.WithContext(context.WithValue(req.Context(), ctxInternalTokenWS, boundWS))
	}
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	return rr
}

// covAsg2WaitTerminal polls until the assignment reaches a terminal state so
// the async runAssignment goroutine can't outlive the test DB.
func covAsg2WaitTerminal(t *testing.T, h *AssignmentHandler, assignmentID string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var status string
		if err := h.db.QueryRow(`SELECT status FROM assignments WHERE id = ?`, assignmentID).Scan(&status); err == nil {
			if status == "COMPLETED" || status == "FAILED" {
				return status
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("assignment %s never reached a terminal state", assignmentID)
	return ""
}

// ---- Create: PR-F24 bound-token gates ----

func TestAsgCov2_BoundTokenWorkspaceMismatch403(t *testing.T) {
	h, wsID, crewID, _, _, chatID := covAsgRig(t)
	body := `{"target_slug":"asg-worker","task":"t","crew_id":"` + crewID + `","workspace_id":"` + wsID + `","chat_id":"` + chatID + `"}`
	rr := covAsg2Post(t, h, body, "ws-other")
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "workspace_id does not match") {
		t.Errorf("body = %q", rr.Body.String())
	}
}

// covAsg2OtherWorkspace seeds a second workspace with a unique slug.
func covAsg2OtherWorkspace(t *testing.T, h *AssignmentHandler, id string) string {
	t.Helper()
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', ?)`, id, id); err != nil {
		t.Fatalf("insert other workspace: %v", err)
	}
	return id
}

func TestAsgCov2_BoundTokenForeignCrew403(t *testing.T) {
	h, wsID, _, _, _, chatID := covAsgRig(t)
	// Crew living in a different workspace than the bound token.
	otherWS := covAsg2OtherWorkspace(t, h, "ws-asg2-other")
	foreignCrew := seedCrewRow(t, h.db, "crew-asg2-foreign", otherWS, "F", "asg2-foreign")

	body := `{"target_slug":"asg-worker","task":"t","crew_id":"` + foreignCrew + `","workspace_id":"` + wsID + `","chat_id":"` + chatID + `"}`
	rr := covAsg2Post(t, h, body, wsID)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "crew does not belong") {
		t.Errorf("body = %q", rr.Body.String())
	}
}

func TestAsgCov2_BoundTokenForeignChat403(t *testing.T) {
	h, wsID, crewID, _, _, _ := covAsgRig(t)
	otherWS := covAsg2OtherWorkspace(t, h, "ws-asg2-chatws")
	otherCrew := seedCrewRow(t, h.db, "crew-asg2-chatws", otherWS, "C", "asg2-chatws")
	foreignAgent := seedAgentRow(t, h.db, "agent-asg2-chatws", otherWS, otherCrew, "FA", "asg2-fa", "AGENT")
	if _, err := h.db.Exec(`
		INSERT INTO chats (id, agent_id, workspace_id, title, mode, status, started_at, created_at, updated_at)
		VALUES ('chat-asg2-foreign', ?, ?, 'x', 'MISSION', 'ACTIVE', datetime('now'), datetime('now'), datetime('now'))`,
		foreignAgent, otherWS); err != nil {
		t.Fatalf("seed foreign chat: %v", err)
	}
	body := `{"target_slug":"asg-worker","task":"t","crew_id":"` + crewID + `","workspace_id":"` + wsID + `","chat_id":"chat-asg2-foreign"}`
	rr := covAsg2Post(t, h, body, wsID)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "chat does not belong") {
		t.Errorf("body = %q", rr.Body.String())
	}
}

// ---- Create: assigner crew unresolvable + connection-check DB error ----

func TestAsgCov2_AssignerCrewUnresolvable403(t *testing.T) {
	h, wsID, crewID, _, _, chatID := covAsgRig(t)
	// Detach the chat from its agent: the first lookup (chats.agent_id)
	// still yields NULL→"" and the JOIN-based crew resolution returns no
	// rows, which must be answered with the explicit cross-crew denial.
	if _, err := h.db.Exec(`UPDATE chats SET agent_id = NULL WHERE id = ?`, chatID); err != nil {
		t.Skipf("chats.agent_id not nullable in this schema: %v", err)
	}
	body := `{"target_slug":"asg-worker","task":"t","crew_id":"` + crewID + `","workspace_id":"` + wsID + `","chat_id":"` + chatID + `"}`
	rr := covAsg2Post(t, h, body, "")
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "cannot verify crew connection") {
		t.Errorf("body = %q", rr.Body.String())
	}
}

func TestAsgCov2_ConnectionCheckDBError500(t *testing.T) {
	h, wsID, _, _, _, chatID := covAsgRig(t)
	otherCrew := seedCrewRow(t, h.db, "crew-asg2-conn", wsID, "O", "asg2-conn")
	seedAgentRow(t, h.db, "agent-asg2-conn", wsID, otherCrew, "O", "asg2-conn-a", "AGENT")
	// Break only the crew_connections table so the handler reaches
	// AreCrewsConnected and gets a real (non-ErrNoRows) DB error.
	if _, err := h.db.Exec(`ALTER TABLE crew_connections RENAME TO crew_connections_broken`); err != nil {
		t.Fatalf("rename crew_connections: %v", err)
	}
	body := `{"target_slug":"asg2-conn-a","task":"t","crew_id":"` + otherCrew + `","workspace_id":"` + wsID + `","chat_id":"` + chatID + `"}`
	rr := covAsg2Post(t, h, body, "")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

// ---- Create: mission-linked side effects ----

func TestAsgCov2_MissionLinkedCreate(t *testing.T) {
	h, wsID, crewID, leadID, workerID, _ := covAsgRig(t)
	h.hub = covAsg2Hub(t)
	h.journal = covAsg2FailEmitter{} // exercises the journal-warn branches too

	// The mission shares its id with the chat (group_id linkage).
	missionID := "chat-asg2-mission"
	if _, err := h.db.Exec(`
		INSERT INTO chats (id, agent_id, workspace_id, title, mode, status, started_at, created_at, updated_at)
		VALUES (?, ?, ?, 'm', 'MISSION', 'ACTIVE', datetime('now'), datetime('now'), datetime('now'))`,
		missionID, leadID, wsID); err != nil {
		t.Fatalf("seed mission chat: %v", err)
	}
	if _, err := h.db.Exec(`
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'trace-asg2', 'M', 'IN_PROGRESS', datetime('now'), datetime('now'))`,
		missionID, wsID, crewID, leadID); err != nil {
		t.Fatalf("seed mission: %v", err)
	}
	// Blank the lead's name so the comment author falls back to "Lead".
	if _, err := h.db.Exec(`UPDATE agents SET name = '' WHERE id = ?`, leadID); err != nil {
		t.Fatalf("blank lead name: %v", err)
	}

	longTask := strings.Repeat("x", 350) // > 300 → comment preview truncation, > 120 → journal summary truncation
	body := `{"target_slug":"asg-worker","task":"` + longTask + `","crew_id":"` + crewID + `","workspace_id":"` + wsID + `","chat_id":"` + missionID + `"}`
	rr := covAsg2Post(t, h, body, "")
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	var out map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	asgID := out["assignment_id"]
	if asgID == "" {
		t.Fatal("no assignment_id in response")
	}

	// Comment posted with the "Lead" fallback + truncated preview.
	var commentBody string
	if err := h.db.QueryRow(`SELECT body FROM mission_comments WHERE mission_id = ?`, missionID).Scan(&commentBody); err != nil {
		t.Fatalf("query mission comment: %v", err)
	}
	if !strings.Contains(commentBody, "**Lead assigned work to Worker**") {
		t.Errorf("comment = %q", commentBody)
	}
	if !strings.Contains(commentBody, "...") {
		t.Errorf("comment preview not truncated: %q", commentBody)
	}

	// Assignee moved to the target agent.
	var assigneeID, assigneeType string
	if err := h.db.QueryRow(`SELECT assignee_id, assignee_type FROM missions WHERE id = ?`, missionID).
		Scan(&assigneeID, &assigneeType); err != nil {
		t.Fatalf("query mission assignee: %v", err)
	}
	if assigneeID != workerID || assigneeType != "agent" {
		t.Errorf("assignee = %s/%s, want %s/agent", assigneeID, assigneeType, workerID)
	}

	// Activity row recorded.
	var action string
	if err := h.db.QueryRow(`SELECT action FROM mission_activity WHERE mission_id = ?`, missionID).Scan(&action); err != nil {
		t.Fatalf("query mission activity: %v", err)
	}
	if action != "assignee_changed" {
		t.Errorf("action = %q", action)
	}

	covAsg2WaitTerminal(t, h, asgID) // let the async goroutine finish before teardown
}

// ---- runAssignment: execution past container creation ----

// covAsg2Target builds the targetAgentInfo for the seeded worker.
func covAsg2Target(workerID string) targetAgentInfo {
	return targetAgentInfo{
		ID: workerID, Slug: "asg-worker", Name: "Worker",
		CLIAdapter: "CLAUDE_CODE", ToolProfile: "CODING", TimeoutSeconds: 5, CrewSlug: "asg",
	}
}

func covAsg2InsertAssignment(t *testing.T, h *AssignmentHandler, id, wsID, chatID, leadID, workerID string) {
	t.Helper()
	if _, err := h.db.Exec(`
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, group_id, created_at)
		VALUES (?, ?, ?, ?, ?, 'task', 'PENDING', ?, datetime('now'))`,
		id, wsID, chatID, leadID, workerID, chatID); err != nil {
		t.Fatalf("insert assignment: %v", err)
	}
}

func TestAsgCov2_RunAssignment_ExecutionError(t *testing.T) {
	h, wsID, crewID, leadID, workerID, chatID := covAsgRig(t)
	orch := orchestrator.New(covAsg2Provider{}, nil, newTestLogger())
	orch.StopAccepting() // RunAgent fails fast and deterministically
	h.orch = orch

	asgID := "asg2-run-exec"
	covAsg2InsertAssignment(t, h, asgID, wsID, chatID, leadID, workerID)

	body := createAssignmentBody{
		TargetSlug: "asg-worker", Task: "do it", CrewID: crewID,
		WorkspaceID: wsID, ChatID: chatID,
	}
	h.runAssignment(context.Background(), asgID, body, covAsg2Target(workerID), nil)

	var status, errMsg string
	if err := h.db.QueryRow(`SELECT status, COALESCE(error_message,'') FROM assignments WHERE id = ?`, asgID).
		Scan(&status, &errMsg); err != nil {
		t.Fatalf("query assignment: %v", err)
	}
	if status != "FAILED" {
		t.Errorf("status = %q, want FAILED", status)
	}
	if !strings.Contains(errMsg, "execution error") || !strings.Contains(errMsg, "not accepting new runs") {
		t.Errorf("error_message = %q", errMsg)
	}
}

func TestAsgCov2_RunAssignment_LeadPlanning(t *testing.T) {
	h, wsID, crewID, leadID, workerID, chatID := covAsgRig(t)
	orch := orchestrator.New(covAsg2Provider{}, nil, newTestLogger())
	orch.StopAccepting()
	h.orch = orch

	asgID := "asg2-run-lead"
	covAsg2InsertAssignment(t, h, asgID, wsID, chatID, leadID, workerID)

	body := createAssignmentBody{
		TargetSlug: "asg-worker", Task: "plan it", CrewID: crewID,
		WorkspaceID: wsID, ChatID: chatID, LeadPlanning: true,
	}
	h.runAssignment(context.Background(), asgID, body, covAsg2Target(workerID), nil)

	var status string
	if err := h.db.QueryRow(`SELECT status FROM assignments WHERE id = ?`, asgID).Scan(&status); err != nil {
		t.Fatalf("query assignment: %v", err)
	}
	if status != "FAILED" {
		t.Errorf("status = %q, want FAILED", status)
	}
}

func TestAsgCov2_RunAssignment_BackupGuardRefusal(t *testing.T) {
	h, wsID, crewID, leadID, workerID, chatID := covAsgRig(t)
	h.orch = orchestrator.New(covAsg2Provider{}, nil, newTestLogger())

	// Hold the durable backup lock for the workspace.
	expires := time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339)
	if _, err := h.db.Exec(`
		INSERT INTO backup_locks (workspace_id, acquired_at, acquired_by, expires_at)
		VALUES (?, datetime('now'), 'test', ?)`, wsID, expires); err != nil {
		t.Fatalf("insert backup lock: %v", err)
	}

	asgID := "asg2-run-guard"
	covAsg2InsertAssignment(t, h, asgID, wsID, chatID, leadID, workerID)

	body := createAssignmentBody{
		TargetSlug: "asg-worker", Task: "t", CrewID: crewID,
		WorkspaceID: wsID, ChatID: chatID,
	}
	h.runAssignment(context.Background(), asgID, body, covAsg2Target(workerID), nil)

	var status, errMsg string
	if err := h.db.QueryRow(`SELECT status, COALESCE(error_message,'') FROM assignments WHERE id = ?`, asgID).
		Scan(&status, &errMsg); err != nil {
		t.Fatalf("query assignment: %v", err)
	}
	if status != "FAILED" {
		t.Errorf("status = %q, want FAILED", status)
	}
	if !strings.Contains(errMsg, "backed up") {
		t.Errorf("error_message = %q", errMsg)
	}
}

// ---- finishAssignment ----

func TestAsgCov2_FinishAssignment_CompletedWithRunID(t *testing.T) {
	h, wsID, _, leadID, workerID, chatID := covAsgRig(t)
	h.hub = covAsg2Hub(t)
	cb := &covAsg2Callback{}
	h.missionCallback = cb

	asgID := "asg2-fin-ok"
	covAsg2InsertAssignment(t, h, asgID, wsID, chatID, leadID, workerID)

	// noopEmitter rejects run.* entries → the terminal-entry error branch
	// runs; the COMPLETED payload (exit_code) is still built first.
	h.finishAssignment(context.Background(), asgID, "run-asg2", chatID, "asg-worker", wsID, "all done", "")

	var status, result string
	if err := h.db.QueryRow(`SELECT status, COALESCE(result_summary,'') FROM assignments WHERE id = ?`, asgID).
		Scan(&status, &result); err != nil {
		t.Fatalf("query assignment: %v", err)
	}
	if status != "COMPLETED" || result != "all done" {
		t.Errorf("status/result = %q/%q", status, result)
	}
	if cb.gotID != asgID || cb.gotStatus != "COMPLETED" {
		t.Errorf("mission callback got %q/%q", cb.gotID, cb.gotStatus)
	}
}

func TestAsgCov2_FinishAssignment_MissionComment_Handoff(t *testing.T) {
	h, wsID, crewID, leadID, workerID, _ := covAsgRig(t)

	missionID := "chat-asg2-fin-mission"
	if _, err := h.db.Exec(`
		INSERT INTO chats (id, agent_id, workspace_id, title, mode, status, started_at, created_at, updated_at)
		VALUES (?, ?, ?, 'm', 'MISSION', 'ACTIVE', datetime('now'), datetime('now'), datetime('now'))`,
		missionID, leadID, wsID); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	if _, err := h.db.Exec(`
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'trace-fin', 'M', 'IN_PROGRESS', datetime('now'), datetime('now'))`,
		missionID, wsID, crewID, leadID); err != nil {
		t.Fatalf("seed mission: %v", err)
	}
	asgID := "asg2-fin-handoff"
	covAsg2InsertAssignment(t, h, asgID, wsID, missionID, leadID, workerID)

	result := "preamble\n---HANDOFF---\nsummary: shipped the feature\nconfidence: high\nartifacts: pr#42\n---END HANDOFF---"
	h.finishAssignment(context.Background(), asgID, "", missionID, "asg-worker", wsID, result, "")

	var commentBody string
	if err := h.db.QueryRow(`SELECT body FROM mission_comments WHERE mission_id = ?`, missionID).Scan(&commentBody); err != nil {
		t.Fatalf("query comment: %v", err)
	}
	if !strings.Contains(commentBody, "completed their work** (confidence: high)") ||
		!strings.Contains(commentBody, "shipped the feature") ||
		!strings.Contains(commentBody, "**Artifacts:** pr#42") {
		t.Errorf("comment = %q", commentBody)
	}
	var action string
	if err := h.db.QueryRow(`SELECT action FROM mission_activity WHERE mission_id = ?`, missionID).Scan(&action); err != nil {
		t.Fatalf("query activity: %v", err)
	}
	if action != "task_completed" {
		t.Errorf("action = %q", action)
	}
}

func TestAsgCov2_FinishAssignment_MissionComment_LongPlainResult(t *testing.T) {
	h, wsID, crewID, leadID, workerID, _ := covAsgRig(t)

	missionID := "chat-asg2-fin-long"
	if _, err := h.db.Exec(`
		INSERT INTO chats (id, agent_id, workspace_id, title, mode, status, started_at, created_at, updated_at)
		VALUES (?, ?, ?, 'm', 'MISSION', 'ACTIVE', datetime('now'), datetime('now'), datetime('now'))`,
		missionID, leadID, wsID); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	if _, err := h.db.Exec(`
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'trace-fin2', 'M', 'IN_PROGRESS', datetime('now'), datetime('now'))`,
		missionID, wsID, crewID, leadID); err != nil {
		t.Fatalf("seed mission: %v", err)
	}
	asgID := "asg2-fin-long"
	covAsg2InsertAssignment(t, h, asgID, wsID, missionID, leadID, workerID)

	h.finishAssignment(context.Background(), asgID, "", missionID, "asg-worker", wsID, strings.Repeat("r", 600), "")

	var commentBody string
	if err := h.db.QueryRow(`SELECT body FROM mission_comments WHERE mission_id = ?`, missionID).Scan(&commentBody); err != nil {
		t.Fatalf("query comment: %v", err)
	}
	if !strings.Contains(commentBody, "completed their work") || !strings.Contains(commentBody, "...") {
		t.Errorf("comment not truncated plain summary: %q", commentBody)
	}
}

func TestAsgCov2_FinishAssignment_PumpLookupError(t *testing.T) {
	h, wsID, _, leadID, workerID, chatID := covAsgRig(t)
	asgID := "asg2-fin-pumperr"
	covAsg2InsertAssignment(t, h, asgID, wsID, chatID, leadID, workerID)

	// Break the agents table AFTER seeding so the UPDATE on assignments
	// still works but crewIDForAssignment's JOIN errors out (cerr branch).
	if _, err := h.db.Exec(`ALTER TABLE agents RENAME TO agents_broken_asg2`); err != nil {
		t.Fatalf("rename agents: %v", err)
	}
	t.Cleanup(func() {
		_, _ = h.db.Exec(`ALTER TABLE agents_broken_asg2 RENAME TO agents`)
	})

	h.finishAssignment(context.Background(), asgID, "", chatID, "asg-worker", wsID, "", "boom")

	var status string
	if err := h.db.QueryRow(`SELECT status FROM assignments WHERE id = ?`, asgID).Scan(&status); err != nil {
		t.Fatalf("query assignment: %v", err)
	}
	if status != "FAILED" {
		t.Errorf("status = %q, want FAILED", status)
	}
}

// fmt is used by covAsg2Target callers indirectly; keep the import honest.
var _ = fmt.Sprintf
