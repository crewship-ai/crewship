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
// waiting out a grace period.
const hardStopPollInterval = 100 * time.Millisecond

// hardStopTargets runs Tier 2 hard termination (§10.3, B7) against every
// RUNNING row Stop's Tier 1 stamp just reached, one goroutine per target so
// N simultaneous agents on one issue cost one grace period, not N. Joined
// (not fire-and-forget) before Stop responds, so nothing here outlives the
// request — no beginBackgroundWork registration needed. wsID scopes the
// journal entries; ident is for logging only.
func (h *IssueHandler) hardStopTargets(ctx context.Context, wsID, ident string, targets []cancelTarget) {
	var wg sync.WaitGroup
	for _, t := range targets {
		// A row that never reached RUNNING has no exec to signal — Tier 1
		// alone already keeps it from ever starting one. Signalling it
		// would just manufacture a NOT_FOUND entry for a run nobody
		// expected a hard stop to touch.
		if t.Status != "RUNNING" || !t.ExecID.Valid || t.ExecID.String == "" || !t.ExecContID.Valid || t.ExecContID.String == "" {
			continue
		}
		wg.Add(1)
		go func(t cancelTarget) {
			defer wg.Done()
			h.hardStopOne(ctx, wsID, t.ID, t.ExecContID.String, t.ExecID.String)
		}(t)
	}
	wg.Wait()
	h.logger.Info("issue stop: hard-stop pass complete", "identifier", ident, "targets", len(targets))
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
