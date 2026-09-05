package api

// issue_terminal_children.go — §10.4's terminal-children rule (fixes F10,
// work package B11, #2368): an issue with a non-terminal sub-issue
// (parent_issue_id) or a non-terminal mission_task may not transition to
// DONE or REVIEW without `?force=true`.
//
// Before this file, `sub_issues_count` was display-only (issue_handler.go's
// own comment on it says so) — nothing anywhere read parent_issue_id before
// writing `status`. A parent could close over a child mid-flight and the
// board would show it as done while an agent kept working underneath it.
// Golden scenario 8 (§18) is exactly this: mark a parent DONE with an open
// child, expect a 409, then force it and expect a receipt.
//
// "Receipt" here is not a new table (F42: the journal is already
// hash-chained per workspace, and rev 3 explicitly dropped decision_receipts
// in favour of that chain) — it is the SAME mission_activity/journal row
// issueEvents.record already writes for a status change, carrying who
// forced it (ActorType/ActorID, already on every issueEvent) and which
// children were open (issueEvent.ForcedPast, added alongside this file).

import (
	"context"
	"database/sql"
	"fmt"
)

// terminalIssueStatuses are the issue-tracker statuses §10.4 treats as
// "no longer live" for a SUB-ISSUE. Deliberately narrower than "closed" in
// the colloquial sense: FAILED is excluded on purpose — a failed child is
// an active problem, not a finished one, and a parent should not be able
// to force past it without at least seeing it named in the blocker list
// (force still works; it just also has to say "FAILED" out loud).
var terminalIssueStatuses = map[string]bool{
	"DONE":      true,
	"CANCELLED": true,
	"DUPLICATE": true,
}

// terminalTaskStatuses mirrors I3's "a terminal state is terminal" set for
// mission_tasks (§10.3: CANCELLED/COMPLETED/FAILED), plus SKIPPED — a
// skipped task is also no longer live work a parent needs to wait for.
var terminalTaskStatuses = map[string]bool{
	"COMPLETED": true,
	"FAILED":    true,
	"CANCELLED": true,
	"SKIPPED":   true,
}

// childQueryer is the subset of *sql.DB / *sql.Tx openChildBlockers needs.
// Accepting the interface (rather than *sql.DB) is what lets the Update
// handler re-run this SAME check inside a transaction right before its
// write, closing the check-then-act race a plain *sql.DB parameter would
// leave open: two concurrent requests could otherwise both read "no open
// children" and both proceed, one of them racing a THIRD request that
// opens or reopens a child in between (caught in review on #2377).
type childQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// openChildBlockers returns a human-readable line per non-terminal
// sub-issue and non-terminal mission_task blocking missionID from reaching
// DONE/REVIEW. An empty, nil-error result means nothing is blocking.
//
// Callers that intend to act on a clean result (proceed with the
// transition) MUST re-run this check inside the SAME transaction as the
// write that follows — see issue_handler_update.go's Update for the
// pattern. A caller that only wants a cheap, advisory pre-check (to fail
// fast on the common case before opening a transaction at all) may pass
// h.db directly; that read is not itself the enforcement point.
func openChildBlockers(ctx context.Context, db childQueryer, missionID string) ([]string, error) {
	var blockers []string

	subRows, err := db.QueryContext(ctx, `
		SELECT COALESCE(identifier, id), status
		  FROM missions
		 WHERE parent_issue_id = ?`, missionID)
	if err != nil {
		return nil, fmt.Errorf("query sub-issues: %w", err)
	}
	for subRows.Next() {
		var ident, status string
		if err := subRows.Scan(&ident, &status); err != nil {
			subRows.Close()
			return nil, fmt.Errorf("scan sub-issue: %w", err)
		}
		if !terminalIssueStatuses[status] {
			blockers = append(blockers, fmt.Sprintf("issue %s (%s)", ident, status))
		}
	}
	if err := subRows.Err(); err != nil {
		subRows.Close()
		return nil, fmt.Errorf("iterate sub-issues: %w", err)
	}
	subRows.Close()

	taskRows, err := db.QueryContext(ctx, `
		SELECT title, status FROM mission_tasks WHERE mission_id = ?`, missionID)
	if err != nil {
		return nil, fmt.Errorf("query mission tasks: %w", err)
	}
	for taskRows.Next() {
		var title, status string
		if err := taskRows.Scan(&title, &status); err != nil {
			taskRows.Close()
			return nil, fmt.Errorf("scan mission task: %w", err)
		}
		if !terminalTaskStatuses[status] {
			blockers = append(blockers, fmt.Sprintf("task %q (%s)", title, status))
		}
	}
	if err := taskRows.Err(); err != nil {
		taskRows.Close()
		return nil, fmt.Errorf("iterate mission tasks: %w", err)
	}
	taskRows.Close()

	return blockers, nil
}

// isTerminalIssueTarget reports whether newStatus is one of the two
// statuses §10.4 gates (DONE, REVIEW).
func isTerminalIssueTarget(newStatus string) bool {
	return newStatus == "DONE" || newStatus == "REVIEW"
}

// blockersMessage renders openChildBlockers' result into the 409 body a
// human (or a CLI caller) reads to understand what force would bypass.
func blockersMessage(blockers []string) string {
	msg := "Cannot move to DONE/REVIEW: "
	if len(blockers) == 1 {
		msg += "1 open child is still live: " + blockers[0]
	} else {
		msg += fmt.Sprintf("%d open children are still live: ", len(blockers))
		for i, b := range blockers {
			if i > 0 {
				msg += ", "
			}
			msg += b
		}
	}
	return msg + ". Retry with ?force=true to override (a receipt will name who forced it)."
}
