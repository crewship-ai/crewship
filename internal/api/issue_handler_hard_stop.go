package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
)

// assignmentMatch is the same heuristic Stop has always used to find the
// live assignment(s) for an issue: mission_id (#2256) is the direct answer,
// with chat_id/group_id as a fallback for a delegated sub-task whose
// dispatcher never threaded mission_id through. See Stop's own comment
// (issue_handler_workflow.go) for the full rationale — hoisted to package
// scope so stampCancelRequested (used by both of Stop's branches, and by
// the Tier 2 hard-stop path added in B7) shares exactly one copy of it.
const assignmentMatch = `(mission_id = ? OR chat_id = ? OR group_id = ?) AND status NOT IN ('COMPLETED', 'FAILED', 'CANCELLED')`

// cancelTarget is one row Stop's Tier 1 stamp reached — enough for the
// Tier 2 hard-stop path to decide, without a second query, whether there is
// a live exec worth signalling.
type cancelTarget struct {
	ID         string
	Status     string
	ExecID     sql.NullString
	ExecContID sql.NullString
}

// txQueryExecer is satisfied by both *sql.DB and *sql.Tx, so
// stampCancelRequested runs identically whether Stop is inside a
// transaction (the IN_PROGRESS/REVIEW branch) or not (the mention-dispatched
// branch).
type txQueryExecer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// stampCancelRequested is Tier 1 (§10.3, A1) — writing cancel_requested_at
// is what actually stops a run; everything Tier 2 does below is strictly
// additional. Deliberately called and committed BEFORE any hard-stop signal
// goes out (both of Stop's call sites do this), so a run that finishes in
// the gap between the stamp and the signal — or that a signal simply misses
// — still lands CANCELLED (outcome CANCELLED, B6) via the ordinary
// finishAssignment path, rather than resurrecting as COMPLETED.
//
// Uses UPDATE ... RETURNING (SQLite 3.35+, already relied on elsewhere —
// lockout.go, issue_create_core.go) so the exact rows this stamp reached are
// known without a second, TOCTOU-prone SELECT.
func stampCancelRequested(ctx context.Context, q txQueryExecer, now, missionID string) ([]cancelTarget, error) {
	rows, err := q.QueryContext(ctx, `
		UPDATE assignments SET cancel_requested_at = ?, cancel_reason = 'issue stopped'
		 WHERE `+assignmentMatch+`
		RETURNING id, status, exec_id, exec_container_id`,
		now, missionID, missionID, missionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []cancelTarget
	for rows.Next() {
		var t cancelTarget
		if err := rows.Scan(&t.ID, &t.Status, &t.ExecID, &t.ExecContID); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Result vocabulary for assignments.hard_stop_result — must match the CHECK
// constraint in 20260905030812_assignments_hard_stop.sql exactly.
const (
	hardStopTerminatedTerm = "TERMINATED_TERM"
	hardStopTerminatedKill = "TERMINATED_KILL"
	hardStopAlreadyExited  = "ALREADY_EXITED"
	hardStopUnsupported    = "UNSUPPORTED"
	hardStopNotFound       = "NOT_FOUND"
	hardStopError          = "ERROR"
	// hardStopPendingExec covers the race window assignments_run.go's
	// RUNNING stamp opens: that stamp lands BEFORE EnsureProvisioned /
	// buildCrewRuntimeConfig / GetOrCreateContainerCfg run (which can take
	// real wall-clock time on a cold container) and well before the heavy
	// exec that OnExecStarted reports. A hard stop landing in that window
	// sees the row as RUNNING with no exec_id yet. awaitExecID re-reads the
	// row for up to hardStopExecAppearTimeout to catch the common case (the
	// exec starts within that window); this is what gets recorded when it
	// still hasn't by the deadline — the row is left explicitly explained
	// rather than silently unactioned. Tier 1's cancel_requested_at is
	// unaffected either way: the run still ends CANCELLED once it reports
	// back (finishAssignment).
	hardStopPendingExec = "PENDING_EXEC"
)

// hardStopSignalTimeout bounds one "send a signal" exec — it should return
// in milliseconds; a container daemon that cannot answer this fast is not
// going to answer at all inside the 5s budget.
const hardStopSignalTimeout = 1 * time.Second

// hardStopProbeTimeout bounds one tmuxPanePIDs (list-panes) attempt — a
// pure read, tried up to twice (one retry on a transient failure) BEFORE
// the TERM/KILL sequence below, so it is deliberately shorter than
// hardStopSignalTimeout: a full-length timeout on a read attempted twice
// would eat into the 5s budget the signal sequence itself needs.
const hardStopProbeTimeout = 300 * time.Millisecond

// hardStopGrace is how long, after each signal, hardStopOne waits for the
// exec to actually stop running before escalating. TERM then (if needed)
// KILL, each with its own send + grace: worst case
// 2×hardStopProbeTimeout (list-panes, plus one retry) +
// 2×(hardStopSignalTimeout+hardStopGrace) (kill-session/TERM-wait,
// kill-KILL/wait) ≈ 0.6s + 4s = 4.6s, inside the PRD's 5s bound — tighter
// than before tmuxPanePIDs existed, but still with margin for the
// DB/journal writes around it.
const hardStopGrace = 1 * time.Second

// hardStopPollInterval is how often hardStopOne re-checks ExecInspect while
// waiting out a grace period, and how often awaitExecID re-reads a
// not-yet-started target's row.
const hardStopPollInterval = 100 * time.Millisecond

// hardStopExecAppearTimeout bounds awaitExecID: how long a hard stop
// against a RUNNING-but-not-yet-executing target waits for exec_id to
// appear before giving up and recording hardStopPendingExec. Separate from
// (and on top of) the TERM/KILL budget above — this covers a different
// window, before any exec exists to signal at all.
const hardStopExecAppearTimeout = 2 * time.Second

// hardStopTargets runs Tier 2 hard termination (§10.3, B7) against every
// RUNNING row Stop's Tier 1 stamp just reached, one goroutine per target so
// N simultaneous agents on one issue cost one grace period, not N. Joined
// (not fire-and-forget) before Stop responds, so nothing here outlives the
// request — no beginBackgroundWork registration needed. wsID scopes the
// journal entries; ident is for logging only.
func (h *IssueHandler) hardStopTargets(ctx context.Context, wsID, ident string, targets []cancelTarget) {
	var wg sync.WaitGroup
	for _, t := range targets {
		// A row that never reached RUNNING has no exec to signal, ever —
		// Tier 1 alone already keeps it from starting one. A RUNNING row
		// WITHOUT an exec_id yet is different: it may simply not have
		// reached its exec yet (see hardStopPendingExec's doc), so it still
		// goes through hardStopRunningTarget rather than being skipped
		// here.
		if t.Status != "RUNNING" {
			continue
		}
		wg.Add(1)
		go func(t cancelTarget) {
			defer wg.Done()
			h.hardStopRunningTarget(ctx, wsID, t)
		}(t)
	}
	wg.Wait()
	h.logger.Info("issue stop: hard-stop pass complete", "identifier", ident, "targets", len(targets))
}

// hardStopRunningTarget resolves a RUNNING target's exec (waiting briefly
// if it hasn't started yet — see awaitExecID) and, once it has one, hands
// off to hardStopOne. A target whose exec never appears in time is recorded
// explicitly (hardStopPendingExec) rather than silently skipped.
func (h *IssueHandler) hardStopRunningTarget(ctx context.Context, wsID string, t cancelTarget) {
	containerID, execID, ok := h.awaitExecID(ctx, t)
	if !ok {
		h.recordHardStopResult(ctx, wsID, t.ID, "", "", "", hardStopPendingExec)
		return
	}
	agentSlug, err := h.assignmentAgentSlug(ctx, t.ID)
	if err != nil {
		// A real DB error here (busy/locked, connection dropped) is not
		// "the assignee isn't an agent" — record it honestly as ERROR
		// rather than UNSUPPORTED, which would misattribute a transient
		// failure to a configuration gap.
		h.logger.Warn("hard stop: resolve agent slug", "error", err, "assignment_id", t.ID)
		h.recordHardStopResult(ctx, wsID, t.ID, containerID, execID, "", hardStopError)
		return
	}
	h.hardStopOne(ctx, wsID, t.ID, containerID, execID, agentSlug)
}

// awaitExecID returns t's exec_id/exec_container_id, re-reading the row
// (every hardStopPollInterval, up to hardStopExecAppearTimeout) when the
// RETURNING snapshot Stop's Tier 1 stamp captured did not have them yet —
// the assignments_run.go RUNNING stamp lands before provisioning, and
// OnExecStarted (which persists these) fires only once the heavy exec
// actually starts. ok=false means nothing appeared before the deadline.
func (h *IssueHandler) awaitExecID(ctx context.Context, t cancelTarget) (containerID, execID string, ok bool) {
	if t.ExecID.Valid && t.ExecID.String != "" && t.ExecContID.Valid && t.ExecContID.String != "" {
		return t.ExecContID.String, t.ExecID.String, true
	}
	deadline := time.Now().Add(hardStopExecAppearTimeout)
	for {
		var execIDNS, contIDNS sql.NullString
		if err := h.db.QueryRowContext(ctx,
			`SELECT exec_id, exec_container_id FROM assignments WHERE id = ?`, t.ID,
		).Scan(&execIDNS, &contIDNS); err != nil {
			h.logger.Warn("hard stop: re-read assignment for exec id", "error", err, "assignment_id", t.ID)
		} else if execIDNS.Valid && execIDNS.String != "" && contIDNS.Valid && contIDNS.String != "" {
			return contIDNS.String, execIDNS.String, true
		}
		if !time.Now().Before(deadline) {
			return "", "", false
		}
		select {
		case <-ctx.Done():
			return "", "", false
		case <-time.After(hardStopPollInterval):
		}
	}
}

// assignmentAgentSlug resolves the slug of the agent assigned to
// assignmentID — the container-visible identity Tier 2 needs to build the
// run's tmux session name (orchestrator.TmuxSessionName). Every agent run
// dispatches through setupTmuxExec (internal/orchestrator/
// orchestrator_exec_env.go) under a session named exactly that, so the slug
// is all a hard stop needs to find it — no pid, host or container, involved.
// A miss (the assignee is not an agent, or the agent row is gone) returns
// "", nil: the caller treats an empty slug as "no session name to build",
// never as an error worth surfacing on its own.
func (h *IssueHandler) assignmentAgentSlug(ctx context.Context, assignmentID string) (string, error) {
	var slug string
	err := h.db.QueryRowContext(ctx, `
		SELECT COALESCE(ag.slug, '') FROM assignments a
		JOIN agents ag ON ag.id = a.assigned_to_id
		WHERE a.id = ?`, assignmentID).Scan(&slug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return slug, nil
}

// hardStopOne ends the run behind execID by killing the tmux session it
// runs inside — a container-visible identity — rather than resolving
// execID to a pid and signalling that (#2365: ExecPIDProvider's pid is in
// the HOST pid namespace; a `kill <that pid>` run as a new exec INSIDE the
// container — the only kind Tier 2 may ever issue — finds no such process
// there and silently signals nothing, which is exactly what dev1 hit).
// Escalates from tmux's own kill-session signal to a direct process-group
// KILL on the session's own pane pids if the exec is still alive after a
// grace period. Never touches the container itself
// (StopCrewRuntime/RemoveCrewRuntime), and never a session other than this
// run's own — a sibling agent's session in the same crew container has a
// different name and is never named by this call.
func (h *IssueHandler) hardStopOne(ctx context.Context, wsID, assignmentID, containerID, execID, agentSlug string) {
	if h.container == nil {
		h.recordHardStopResult(ctx, wsID, assignmentID, containerID, execID, "", hardStopUnsupported)
		return
	}

	running, _, err := h.container.ExecInspect(ctx, execID)
	switch {
	case err != nil:
		// The provider could not even resolve execID — a stale exec id (the
		// container was recreated since it was recorded) or the daemon
		// rejected the lookup outright. Either way there is nothing here to
		// signal.
		h.logger.Warn("hard stop: exec inspect failed", "error", err, "assignment_id", assignmentID)
		h.recordHardStopResult(ctx, wsID, assignmentID, containerID, execID, "", hardStopNotFound)
		return
	case !running:
		// The run ended on its own between the Tier 1 stamp and this check.
		h.recordHardStopResult(ctx, wsID, assignmentID, containerID, execID, "", hardStopAlreadyExited)
		return
	}

	if agentSlug == "" {
		// No container-visible identity to build a session name from (the
		// assignee is not, or is no longer, an agent) — nothing safe left
		// to signal by.
		h.logger.Warn("hard stop: no agent slug for assignment, cannot build a tmux session name", "assignment_id", assignmentID)
		h.recordHardStopResult(ctx, wsID, assignmentID, containerID, execID, "", hardStopUnsupported)
		return
	}

	session := orchestrator.TmuxSessionName(agentSlug)
	result := h.killTmuxSession(ctx, containerID, execID, session)
	h.recordHardStopResult(ctx, wsID, assignmentID, containerID, execID, session, result)
}

// killTmuxSession is the container-visible-identity TERM-then-KILL
// sequence, factored out of hardStopOne so it can be read (and tested) as
// one linear decision: confirm the session exists, end it, wait, escalate,
// wait, give up.
//
// Pane pids are captured BEFORE kill-session runs: tmux discards a
// session's own bookkeeping — list-panes included — the moment
// kill-session succeeds, so escalation would have nothing left to query if
// it waited until after. That same pre-check doubles as proof the session
// exists at all: not every RUNNING exec has one (setupTmuxExec's
// stdin-delivery bypass for oversized prompts, or a container without tmux
// installed, both fall back to a bare exec — internal/orchestrator/
// orchestrator_run.go's buildExecCommand) — sending kill-session and
// burning a full grace period against a session that provably does not
// exist would waste most of the 5s budget for a foregone ERROR.
func (h *IssueHandler) killTmuxSession(ctx context.Context, containerID, execID, session string) string {
	panePIDs, sessionFound, err := h.tmuxPanePIDs(ctx, containerID, session)
	if err != nil {
		// One retry: a single list-panes exec timing out under daemon load
		// should not be treated the same as a confirmed-absent session, nor
		// permanently disable escalation for this run.
		panePIDs, sessionFound, err = h.tmuxPanePIDs(ctx, containerID, session)
	}
	if err == nil && !sessionFound {
		// Confirmed absent, not merely unresolved — this run was never
		// tmux-wrapped, or its session ended between the Tier 1 stamp and
		// here. Recheck the exec directly to report which.
		if running, _, inspectErr := h.container.ExecInspect(ctx, execID); inspectErr == nil && !running {
			return hardStopAlreadyExited
		}
		h.logger.Warn("hard stop: no tmux session for a running exec, cannot signal by session name", "session", session)
		return hardStopNotFound
	}
	if err != nil {
		// Still unresolved after the retry — proceed on a best-effort basis
		// (kill-session may still reach it) rather than giving up outright,
		// but this is exactly the "unknown" case sessionFound's doc warns
		// about: not a confirmed absence.
		h.logger.Warn("hard stop: could not confirm tmux session before kill-session", "error", err, "session", session)
	}

	if !h.runShortExec(ctx, containerID, provider.TmuxKillSessionCmd(session)) {
		return hardStopError
	}
	if h.waitExited(ctx, execID, hardStopGrace) {
		return hardStopTerminatedTerm
	}
	if len(panePIDs) == 0 {
		// kill-session's own signal was the only shot this run got — there
		// is nothing captured to escalate against.
		return hardStopError
	}
	if !h.runShortExec(ctx, containerID, provider.KillProcessGroupCmd("KILL", panePIDs)) {
		return hardStopError
	}
	if h.waitExited(ctx, execID, hardStopGrace) {
		return hardStopTerminatedKill
	}
	// Still running after a process-group SIGKILL should not happen, but a
	// hung daemon exec or a zombie in an unusual state means it CAN read
	// this way — report it honestly as ERROR rather than claim success.
	return hardStopError
}

// tmuxPanePIDs lists session's pane pids by running
// provider.TmuxListPanePIDsCmd as a new exec into containerID — container-
// local pids, read from INSIDE the container the run lives in, never a
// host pid.
//
// Three-way return distinguishes "confirmed absent" from "unknown", which
// killTmuxSession's callers must not conflate: err != nil means the exec
// itself could not be run or timed out (hardStopProbeTimeout is
// deliberately short — see its doc) — the session's existence is UNKNOWN,
// not disproven. err == nil, sessionFound == false means the exec ran to
// completion and tmux reported a non-zero exit (list-panes' own
// "can't find session" error, in practice) — the session is CONFIRMED
// absent. err == nil, sessionFound == true means pids holds every pane pid
// tmux reported for a session that positively exists.
func (h *IssueHandler) tmuxPanePIDs(ctx context.Context, containerID, session string) (pids []int, sessionFound bool, err error) {
	// One shared deadline for the whole attempt (create + drain + wait for
	// exit), not one hardStopProbeTimeout per call — otherwise a single
	// attempt could cost up to 2×hardStopProbeTimeout and the retry above
	// would blow past the budget hardStopGrace's doc computes.
	probeCtx, cancel := context.WithTimeout(ctx, hardStopProbeTimeout)
	defer cancel()
	res, execErr := h.container.Exec(probeCtx, provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         provider.TmuxListPanePIDsCmd(session),
	})
	if execErr != nil {
		return nil, false, fmt.Errorf("list tmux panes: %w", execErr)
	}
	out, _ := io.ReadAll(res.Reader)
	_ = res.Reader.Close()
	code, waitErr := provider.WaitExecExit(probeCtx, h.container, res.ExecID, hardStopProbeTimeout)
	if waitErr != nil {
		return nil, false, fmt.Errorf("list tmux panes: %w", waitErr)
	}
	if code != 0 {
		return nil, false, nil
	}
	for _, field := range strings.Fields(string(out)) {
		if pid, err := strconv.Atoi(field); err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids, true, nil
}

// runShortExec runs cmd as a brand-new exec into containerID — never a
// container-level operation — and reports whether the exec itself ran, not
// its exit status: waitExited's ExecInspect poll of the ORIGINAL exec is
// what actually decides whether the target stopped, so a signal command's
// own exit code (tmux reporting "no such session" because the run already
// exited, say) is not a failure worth distinguishing here.
func (h *IssueHandler) runShortExec(ctx context.Context, containerID string, cmd []string) bool {
	sigCtx, cancel := context.WithTimeout(ctx, hardStopSignalTimeout)
	defer cancel()
	res, err := h.container.Exec(sigCtx, provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         cmd,
	})
	if err != nil {
		h.logger.Warn("hard stop: signal exec failed", "error", err, "cmd", cmd, "container_id", containerID)
		return false
	}
	// Drain and close: the signal-delivery exec is a short-lived helper
	// whose own output nobody consumes, but its reader must still be
	// closed so the provider's underlying connection doesn't leak.
	_, _ = io.Copy(io.Discard, res.Reader)
	_ = res.Reader.Close()
	return true
}

// waitExited polls ExecInspect(execID) until it reports not-running or
// timeout elapses. An ExecInspect error is treated as "still running" —
// fail closed, matching execExitStatus's doctrine elsewhere in this
// codebase (memory.go): an unknown state must never read as success.
func (h *IssueHandler) waitExited(ctx context.Context, execID string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		running, _, err := h.container.ExecInspect(ctx, execID)
		if err == nil && !running {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(hardStopPollInterval):
		}
	}
}

// recordHardStopResult writes what a Tier 2 attempt did onto both the
// assignment row (so `crewship issue runs` / the issue detail page can
// answer "was this hard-stopped, and did it work" without reading the
// journal) and the journal (so it survives independent of the row's later
// terminal write, and shows up in the issue's own timeline).
func (h *IssueHandler) recordHardStopResult(ctx context.Context, wsID, assignmentID, containerID, execID, session, result string) {
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := h.db.ExecContext(ctx,
		`UPDATE assignments SET hard_stop_at = ?, hard_stop_result = ? WHERE id = ?`,
		now, result, assignmentID); err != nil {
		h.logger.Warn("hard stop: record result failed", "error", err, "assignment_id", assignmentID)
	}

	severity := journal.SeverityInfo
	if result == hardStopError || result == hardStopUnsupported {
		severity = journal.SeverityWarn
	}
	payload := map[string]any{"result": result}
	if containerID != "" {
		payload["container_id"] = containerID
	}
	if execID != "" {
		payload["exec_id"] = execID
	}
	if session != "" {
		payload["session"] = session
	}
	if _, err := h.journal.Emit(ctx, journal.Entry{
		WorkspaceID: wsID,
		Type:        journal.EntryAssignmentHardStop,
		Severity:    severity,
		ActorType:   journal.ActorOrchestrator,
		Summary:     fmt.Sprintf("hard stop: assignment %s -> %s", shortRunID(assignmentID), strings.ToLower(result)),
		Payload:     payload,
		Refs:        map[string]any{"assignment_id": assignmentID},
	}); err != nil {
		h.logger.Warn("hard stop: journal emit failed", "error", err, "assignment_id", assignmentID)
	}
}
