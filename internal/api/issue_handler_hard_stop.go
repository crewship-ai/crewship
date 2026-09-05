package api

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
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

// hardStopGrace is how long, after each signal, hardStopOne waits for the
// exec to actually stop running before escalating. TERM then (if needed)
// KILL, each with its own send + grace: worst case
// 2×(hardStopSignalTimeout+hardStopGrace) ≈ 4s, inside the PRD's 5s bound
// with margin for the DB/journal writes around it.
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
		h.recordHardStopResult(ctx, wsID, t.ID, "", "", 0, hardStopPendingExec)
		return
	}
	h.hardStopOne(ctx, wsID, t.ID, containerID, execID)
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

// hardStopOne resolves execID's pid and signals it, escalating from TERM to
// KILL after a grace period, then records what happened on both the
// assignment row and the journal. Never touches the container itself
// (StopCrewRuntime/RemoveCrewRuntime) — only ever a new Exec running `kill`
// against one pid, so a sibling agent's exec in the same container is
// untouched by construction.
func (h *IssueHandler) hardStopOne(ctx context.Context, wsID, assignmentID, containerID, execID string) {
	if h.container == nil {
		h.recordHardStopResult(ctx, wsID, assignmentID, containerID, execID, 0, hardStopUnsupported)
		return
	}
	signaler, ok := h.container.(provider.ExecPIDProvider)
	if !ok {
		h.recordHardStopResult(ctx, wsID, assignmentID, containerID, execID, 0, hardStopUnsupported)
		return
	}

	pid, err := signaler.ExecPID(ctx, execID)
	switch {
	case err != nil:
		// Distinct from "still running but signalling failed" (hardStopError,
		// set by signalAndEscalate below): the provider could not even
		// resolve execID to a pid — the exec id is stale (the container was
		// recreated since it was recorded) or the daemon rejected the
		// lookup outright. Either way there is nothing here to signal.
		h.logger.Warn("hard stop: exec pid lookup failed", "error", err, "assignment_id", assignmentID)
		h.recordHardStopResult(ctx, wsID, assignmentID, containerID, execID, 0, hardStopNotFound)
		return
	case pid <= 0:
		// A resolvable but zero pid means the provider positively knows the
		// exec already finished (ExecPIDProvider's own contract) — the run
		// ended on its own between the Tier 1 stamp and this lookup.
		h.recordHardStopResult(ctx, wsID, assignmentID, containerID, execID, 0, hardStopAlreadyExited)
		return
	}

	result := h.signalAndEscalate(ctx, containerID, execID, pid)
	h.recordHardStopResult(ctx, wsID, assignmentID, containerID, execID, pid, result)
}

// signalAndEscalate is the TERM-then-KILL sequence itself, factored out of
// hardStopOne so it can be read (and tested) as one linear decision: send,
// wait, escalate, wait, give up.
func (h *IssueHandler) signalAndEscalate(ctx context.Context, containerID, execID string, pid int) string {
	if !h.sendSignal(ctx, containerID, pid, "TERM") {
		return hardStopError
	}
	if h.waitExited(ctx, execID, hardStopGrace) {
		return hardStopTerminatedTerm
	}
	if !h.sendSignal(ctx, containerID, pid, "KILL") {
		return hardStopError
	}
	if h.waitExited(ctx, execID, hardStopGrace) {
		return hardStopTerminatedKill
	}
	// Still running after SIGKILL from inside its own container should not
	// happen (SIGKILL cannot be caught or blocked), but a hung daemon exec
	// or a zombie in an unusual pid-namespace state means it CAN read this
	// way — report it honestly as ERROR rather than claim success.
	return hardStopError
}

// sendSignal execs `kill -SIGNAL pid` into containerID — a brand new exec,
// never a container-level operation — and reports whether the exec itself
// ran (not whether the target pid still exists; that answer comes from
// waitExited's ExecInspect poll, which is authoritative).
func (h *IssueHandler) sendSignal(ctx context.Context, containerID string, pid int, signal string) bool {
	sigCtx, cancel := context.WithTimeout(ctx, hardStopSignalTimeout)
	defer cancel()
	res, err := h.container.Exec(sigCtx, provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         provider.KillSignalCmd(signal, pid),
	})
	if err != nil {
		h.logger.Warn("hard stop: send signal failed", "error", err, "signal", signal, "pid", pid, "container_id", containerID)
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
func (h *IssueHandler) recordHardStopResult(ctx context.Context, wsID, assignmentID, containerID, execID string, pid int, result string) {
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
	if pid > 0 {
		payload["pid"] = pid
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
