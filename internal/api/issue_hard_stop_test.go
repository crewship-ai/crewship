package api

// Coverage for PRD-ISSUES-AND-ROUTINES-2026 work package B7 ("Hard
// termination (Tier 2)", #2356) and its B7b namespace fix (#2365):
//
//   - Persisting ExecResult.ExecID (and the container it ran in) onto the
//     assignment row the moment an exec starts (orchestrator.AgentRunRequest.
//     OnExecStarted, wired in assignments_run.go).
//   - Stop's ?hard=true path: end the run's own tmux session
//     (orchestrator.TmuxSessionName(agentSlug)) — a container-visible
//     identity — with a brand-new exec into the SAME container, escalating
//     to a process-group KILL on the session's own pane pids after a grace
//     period. #2365: the pre-fix path resolved execID to a pid via
//     provider.ExecPIDProvider and signalled THAT — but that pid is in the
//     HOST pid namespace (dockerd's ExecInspect), so a same-container `kill`
//     never finds it. providertest's FakeProvider models that namespace
//     split (ExecPID's hostPID is never a valid target for an in-container
//     kill), so these tests fail red against the pre-fix mechanism and pass
//     against the session-based one.
//   - Golden scenario 5b (§18): two DIFFERENT agents in one crew (different
//     tmux session names), stop one — the sibling's exec is provably
//     untouched, because a hard stop only ever names its OWN run's session.
import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/provider/providertest"
)

// seedSecondWorkerAgent adds a second, distinctly-slugged agent to the crew
// newTestIssueHandler already seeded ("worker") — needed because Tier 2 now
// signals by tmux session name (orchestrator.TmuxSessionName(agentSlug)):
// two assignments for the SAME agent slug would collide on one session name,
// which cannot happen for two genuinely live runs in production (setupTmuxExec
// kills any prior session of that name before starting a new one). The §18
// scenario 5b sibling must therefore be a different agent, exactly like two
// real agents sharing a crew container each own a differently-named session.
func seedSecondWorkerAgent(t *testing.T, db *sql.DB, wsID, crewID string) string {
	t.Helper()
	id := "agent-worker2"
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status, cli_adapter, temperature, timeout_seconds, tool_profile, memory_enabled)
		 VALUES (?, ?, ?, 'Worker2', 'worker2', 'AGENT', 'IDLE', 'CLAUDE_CODE', 0.7, 1800, 'CODING', 0)`,
		id, wsID, crewID); err != nil {
		t.Fatalf("insert second worker agent: %v", err)
	}
	return id
}

// recordingHardStopEmitter captures every journal.Entry Emit received, so a
// test can assert the exact hard-stop record (result, pid, exec/container
// ids) without a real journal writer.
type recordingHardStopEmitter struct {
	entries []journal.Entry
}

func (r *recordingHardStopEmitter) Emit(_ context.Context, e journal.Entry) (string, error) {
	r.entries = append(r.entries, e)
	return "evt-" + string(e.Type), nil
}
func (r *recordingHardStopEmitter) Flush(_ context.Context) error { return nil }

// seedRunningAssignmentWithExec inserts a RUNNING assignment attributed to
// missionID (mission_id + chat_id + group_id, matching every real dispatch
// path Stop's assignmentMatch reaches) whose exec_id/exec_container_id are
// already stamped — exactly what assignments_run.go's OnExecStarted
// callback would have written by the time the run is live.
func seedRunningAssignmentWithExec(t *testing.T, db *sql.DB, id, wsID, missionID, leadID, workerID, containerID, execID string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	// assignments.chat_id is FK'd to chats(id) — mirrors what
	// ensureMissionChat lazily inserts for a real mission dispatch (see
	// issue_stop_cancel_test.go's own seeding), keyed by the mission id
	// since that is what group_id/chat_id carry here.
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO chats (id, agent_id, workspace_id, title, mode, status, started_at, created_at, updated_at)
		VALUES (?, ?, ?, 'Mission chat', 'MISSION', 'ACTIVE', ?, ?, ?)
		ON CONFLICT(id) DO NOTHING`,
		missionID, leadID, wsID, now, now, now); err != nil {
		t.Fatalf("seed mission chat: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, group_id, mission_id, exec_id, exec_container_id, created_at, started_at)
		VALUES (?, ?, ?, ?, ?, 'do work', 'RUNNING', ?, ?, ?, ?, ?, ?)`,
		id, wsID, missionID, leadID, workerID, missionID, missionID, execID, containerID, now, now); err != nil {
		t.Fatalf("seed running assignment with exec: %v", err)
	}
}

func TestIssue_Stop_Hard_TerminatesTargetNotSibling(t *testing.T) {
	h, userID, wsID, crewID, leadID, workerID := newTestIssueHandler(t)
	emitter := &recordingHardStopEmitter{}
	h.SetJournal(emitter)

	fp := providertest.NewFakeProvider()
	h.SetContainer(fp)
	const sharedContainerID = "crew-shared-container"

	// Two DIFFERENT agents in one crew (§18 scenario 5b) — a hard stop
	// signals by tmux session name, and every agent run owns a session
	// named for its OWN slug, so the sibling here must be a distinct agent
	// (see seedSecondWorkerAgent's doc), not just a second exec. Each
	// "hold" exec, registered under its agent's own session, ignores
	// context cancellation and only stops when signalled by that session
	// name (or a process-group kill on its own pane pid) — see
	// providertest.HoldSessionCmd's doc.
	worker2ID := seedSecondWorkerAgent(t, h.db, wsID, crewID)
	targetSession := orchestrator.TmuxSessionName("worker")
	siblingSession := orchestrator.TmuxSessionName("worker2")
	targetExec, err := fp.Exec(context.Background(), provider.ExecConfig{ContainerID: sharedContainerID, Cmd: providertest.HoldSessionCmd(targetSession)})
	if err != nil {
		t.Fatalf("start target exec: %v", err)
	}
	siblingExec, err := fp.Exec(context.Background(), provider.ExecConfig{ContainerID: sharedContainerID, Cmd: providertest.HoldSessionCmd(siblingSession)})
	if err != nil {
		t.Fatalf("start sibling exec: %v", err)
	}

	// Two issues in the same crew — the target gets stopped, the sibling's
	// own issue is never touched by this request at all, exactly like an
	// operator stopping one issue must never reach a different issue's run.
	targetMissionID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-10", "IN_PROGRESS")
	siblingMissionID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-11", "IN_PROGRESS")

	seedRunningAssignmentWithExec(t, h.db, "a-target", wsID, targetMissionID, leadID, workerID, sharedContainerID, targetExec.ExecID)
	seedRunningAssignmentWithExec(t, h.db, "a-sibling", wsID, siblingMissionID, leadID, worker2ID, sharedContainerID, siblingExec.ExecID)

	// Sanity: both execs are actually running before Stop touches anything.
	if running, _, err := fp.ExecInspect(context.Background(), targetExec.ExecID); err != nil || !running {
		t.Fatalf("target exec not running before stop: running=%v err=%v", running, err)
	}
	if running, _, err := fp.ExecInspect(context.Background(), siblingExec.ExecID); err != nil || !running {
		t.Fatalf("sibling exec not running before stop: running=%v err=%v", running, err)
	}

	req := httptest.NewRequest("POST", "/?hard=true", nil)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("identifier", "ENG-10")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()

	start := time.Now()
	h.Stop(rr, req)
	elapsed := time.Since(start)

	if rr.Code != 200 {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	// §17 B7 accept line: "a stop terminates the target process within 5s".
	if elapsed >= 5*time.Second {
		t.Errorf("hard stop took %s, want < 5s", elapsed)
	}

	var resp struct {
		Status      string `json:"status"`
		RunsStopped int    `json:"runs_stopped"`
		Hard        bool   `json:"hard"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Hard {
		t.Errorf(`response "hard" = false, want true`)
	}
	if resp.RunsStopped != 1 {
		t.Errorf("runs_stopped = %d, want 1 (only the target issue's own assignment)", resp.RunsStopped)
	}

	// The target process actually stopped.
	if running, code, err := fp.ExecInspect(context.Background(), targetExec.ExecID); err != nil || running {
		t.Errorf("target exec still running after hard stop: running=%v code=%d err=%v", running, code, err)
	}

	// The sibling — a DIFFERENT issue's run sharing the same crew
	// container — must be completely unaffected: still running, and never
	// even looked at by Tier 1 or Tier 2.
	if running, _, err := fp.ExecInspect(context.Background(), siblingExec.ExecID); err != nil || !running {
		t.Errorf("sibling exec was affected by hard-stopping a different issue: running=%v err=%v", running, err)
	}
	var siblingCancelAt, siblingHardResult string
	if err := h.db.QueryRow(`SELECT COALESCE(cancel_requested_at,''), COALESCE(hard_stop_result,'') FROM assignments WHERE id='a-sibling'`).
		Scan(&siblingCancelAt, &siblingHardResult); err != nil {
		t.Fatalf("query a-sibling: %v", err)
	}
	if siblingCancelAt != "" || siblingHardResult != "" {
		t.Errorf("a-sibling touched by Stop on a different issue: cancel_requested_at=%q hard_stop_result=%q", siblingCancelAt, siblingHardResult)
	}

	// The target's own row: Tier 1 stamp AND Tier 2 result both landed.
	var targetCancelAt, targetHardResult string
	if err := h.db.QueryRow(`SELECT COALESCE(cancel_requested_at,''), COALESCE(hard_stop_result,'') FROM assignments WHERE id='a-target'`).
		Scan(&targetCancelAt, &targetHardResult); err != nil {
		t.Fatalf("query a-target: %v", err)
	}
	if targetCancelAt == "" {
		t.Errorf("a-target.cancel_requested_at not stamped (Tier 1 must fire regardless of Tier 2)")
	}
	if targetHardResult != hardStopTerminatedTerm && targetHardResult != hardStopTerminatedKill {
		t.Errorf("a-target.hard_stop_result = %q, want TERMINATED_TERM or TERMINATED_KILL", targetHardResult)
	}

	// The journal recorded what was signalled and the result — for the
	// target only.
	var hardStopEntries int
	for _, e := range emitter.entries {
		if e.Type != journal.EntryAssignmentHardStop {
			continue
		}
		hardStopEntries++
		refs, _ := e.Refs["assignment_id"].(string)
		if refs != "a-target" {
			t.Errorf("hard-stop journal entry for assignment_id=%q, want a-target only", refs)
		}
		if payload, ok := e.Payload["exec_id"].(string); !ok || payload != targetExec.ExecID {
			t.Errorf("hard-stop journal entry exec_id = %v, want %q", e.Payload["exec_id"], targetExec.ExecID)
		}
	}
	if hardStopEntries != 1 {
		t.Errorf("hard-stop journal entries = %d, want exactly 1", hardStopEntries)
	}
}

// TestIssue_Stop_Hard_UnsupportedProviderStillCancels proves the B6/Tier-1
// ordering guarantee: a provider that cannot resolve a pid (no container
// wired at all, here) must not prevent the Tier 1 stamp — the run still
// lands CANCELLED through the ordinary finishAssignment path, Tier 2 just
// has nothing more to add.
func TestIssue_Stop_Hard_UnsupportedProviderStillCancels(t *testing.T) {
	h, userID, wsID, crewID, leadID, workerID := newTestIssueHandler(t)
	// h.container is left nil — no SetContainer call.

	missionID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-20", "IN_PROGRESS")
	seedRunningAssignmentWithExec(t, h.db, "a-unsupported", wsID, missionID, leadID, workerID, "some-container", "some-exec")

	req := httptest.NewRequest("POST", "/?hard=true", nil)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("identifier", "ENG-20")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Stop(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	var cancelAt, hardResult string
	if err := h.db.QueryRow(`SELECT COALESCE(cancel_requested_at,''), COALESCE(hard_stop_result,'') FROM assignments WHERE id='a-unsupported'`).
		Scan(&cancelAt, &hardResult); err != nil {
		t.Fatalf("query a-unsupported: %v", err)
	}
	if cancelAt == "" {
		t.Errorf("cancel_requested_at not stamped when the provider has no Tier 2 support")
	}
	if hardResult != hardStopUnsupported {
		t.Errorf("hard_stop_result = %q, want %q", hardResult, hardStopUnsupported)
	}
}

// TestIssue_Stop_Hard_RunningWithoutExecYet_RecordsPendingExec covers the
// race window between assignments_run.go's RUNNING stamp (before ANY
// provisioning) and OnExecStarted (after EnsureProvisioned /
// buildCrewRuntimeConfig / GetOrCreateContainerCfg, which can take real
// wall-clock time on a cold container): a hard stop landing in that window
// must not silently do nothing. If the exec never shows up, it is recorded
// explicitly (PENDING_EXEC) rather than dropped — Tier 1's stamp still
// applies regardless.
func TestIssue_Stop_Hard_RunningWithoutExecYet_RecordsPendingExec(t *testing.T) {
	h, userID, wsID, crewID, leadID, workerID := newTestIssueHandler(t)
	fp := providertest.NewFakeProvider()
	h.SetContainer(fp)

	missionID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-40", "IN_PROGRESS")
	// RUNNING, but exec_id/exec_container_id are still empty — exactly the
	// row shape between the RUNNING stamp and OnExecStarted firing.
	seedRunningAssignmentWithExec(t, h.db, "a-pending-exec", wsID, missionID, leadID, workerID, "", "")

	req := httptest.NewRequest("POST", "/?hard=true", nil)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("identifier", "ENG-40")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()

	start := time.Now()
	h.Stop(rr, req)
	elapsed := time.Since(start)

	if rr.Code != 200 {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	// awaitExecID must actually wait out hardStopExecAppearTimeout rather
	// than giving up instantly — otherwise this test cannot tell "waited
	// and gave up" apart from "never looked".
	if elapsed < hardStopExecAppearTimeout {
		t.Errorf("hard stop returned in %s, want it to wait at least hardStopExecAppearTimeout (%s) before giving up", elapsed, hardStopExecAppearTimeout)
	}

	var cancelAt, hardResult string
	if err := h.db.QueryRow(`SELECT COALESCE(cancel_requested_at,''), COALESCE(hard_stop_result,'') FROM assignments WHERE id='a-pending-exec'`).
		Scan(&cancelAt, &hardResult); err != nil {
		t.Fatalf("query a-pending-exec: %v", err)
	}
	if cancelAt == "" {
		t.Errorf("cancel_requested_at not stamped (Tier 1 must fire regardless of the exec having started yet)")
	}
	if hardResult != hardStopPendingExec {
		t.Errorf("hard_stop_result = %q, want %q — a RUNNING target with no exec yet must be recorded, never silently skipped", hardResult, hardStopPendingExec)
	}
}

// TestIssue_Stop_Hard_RunningWithoutExecYet_CatchesLateExecStart is the
// positive case of the same race: the exec starts (and gets persisted)
// WHILE the hard stop is already waiting, and it still gets signalled.
func TestIssue_Stop_Hard_RunningWithoutExecYet_CatchesLateExecStart(t *testing.T) {
	h, userID, wsID, crewID, leadID, workerID := newTestIssueHandler(t)
	fp := providertest.NewFakeProvider()
	h.SetContainer(fp)

	missionID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-41", "IN_PROGRESS")
	seedRunningAssignmentWithExec(t, h.db, "a-late-exec", wsID, missionID, leadID, workerID, "", "")

	// workerID's slug is "worker" (newTestIssueHandler) — register the hold
	// exec under that agent's own tmux session name so the production
	// session-based signal (not a bare pid) actually reaches it.
	execRes, err := fp.Exec(context.Background(), provider.ExecConfig{ContainerID: "late-container", Cmd: providertest.HoldSessionCmd(orchestrator.TmuxSessionName("worker"))})
	if err != nil {
		t.Fatalf("start exec: %v", err)
	}

	// Simulate OnExecStarted landing shortly after Stop begins waiting —
	// well inside hardStopExecAppearTimeout, well after Stop's Tier 1
	// snapshot was already taken.
	go func() {
		time.Sleep(300 * time.Millisecond)
		if _, err := h.db.Exec(`UPDATE assignments SET exec_id = ?, exec_container_id = ? WHERE id = 'a-late-exec'`,
			execRes.ExecID, "late-container"); err != nil {
			t.Errorf("late exec_id write: %v", err)
		}
	}()

	req := httptest.NewRequest("POST", "/?hard=true", nil)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("identifier", "ENG-41")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Stop(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	if running, _, err := fp.ExecInspect(context.Background(), execRes.ExecID); err != nil || running {
		t.Errorf("exec that started mid-wait was not signalled: running=%v err=%v", running, err)
	}
	var hardResult string
	if err := h.db.QueryRow(`SELECT COALESCE(hard_stop_result,'') FROM assignments WHERE id='a-late-exec'`).Scan(&hardResult); err != nil {
		t.Fatalf("query a-late-exec: %v", err)
	}
	if hardResult != hardStopTerminatedTerm && hardResult != hardStopTerminatedKill {
		t.Errorf("hard_stop_result = %q, want TERMINATED_TERM or TERMINATED_KILL — an exec that starts mid-wait must still be caught", hardResult)
	}
}
