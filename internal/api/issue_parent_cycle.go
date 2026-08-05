package api

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// validIssueDueDate reports whether s is a due date the readers can parse.
//
// Two forms are accepted, and they are exactly the two the product already
// produces: the date-only "2026-09-01" the issue panel's date picker writes,
// and the RFC 3339 timestamp the API hands back. time.Parse rejects impossible
// calendar dates ("2026-13-45", "2026-02-30") as well as prose ("tomorrow"),
// which is the whole point — the column is TEXT, so whatever is written is what
// every reader downstream has to make sense of.
func validIssueDueDate(s string) bool {
	if _, err := time.Parse("2006-01-02", s); err == nil {
		return true
	}
	_, err := time.Parse(time.RFC3339, s)
	return err == nil
}

// issue_parent_cycle.go — the sub-issue hierarchy must stay a forest.
//
// Both parent-setting paths used to reject only the trivial self-parent
// (A → A), with a comment promising that deeper cycles were "tracked
// separately". Two calls were enough to build A → B → A. Nothing walks the
// chain recursively today, so a cycle does not hang a query — it corrupts the
// *meaning* of the hierarchy: every issue in the loop is its own ancestor, so
// "show me this issue's parent chain" and any future roll-up (progress, cost,
// completion) has no base case to stop at.
//
// It stayed theoretical while the only way to set a parent was a human dragging
// one issue onto another in the UI. Adding the agent verb changes that: an agent
// decomposing a backlog sets parents in a loop, from a plan it wrote itself,
// with no one watching each call. The cheap check belongs in before that, and it
// belongs on BOTH paths — the public one and the internal one — or the two
// endpoints disagree about what the same graph is allowed to look like.

// maxParentChainDepth bounds the ancestor walk. It is a safety stop, not a
// product limit on nesting: it exists so a cycle that predates this check (or
// one written directly into the DB) cannot spin the walk forever. A real
// hierarchy deeper than this is pathological on its own, and the walk answers
// "cycle" for it — refusing to add another level to a 64-deep chain is the
// correct outcome either way.
const maxParentChainDepth = 64

// errParentCycle is returned when childID already sits on parentID's ancestor
// chain, so setting it would close a loop.
var errParentCycle = errors.New("parent_issue_id would create a cycle")

// wouldCycleParent reports whether setting childID.parent_issue_id = parentID
// would create a cycle, by walking parentID's own ancestors upward.
//
// The walk is workspace-scoped for the same reason every other lookup on these
// paths is: an ancestor row in another tenant must not be readable, and a chain
// that leaves the workspace is not a chain this caller can reason about — it
// terminates the walk rather than following the link.
//
// Returns errParentCycle on a cycle, a DB error on a real failure, and nil when
// the link is safe. Callers map errParentCycle to a 400.
func wouldCycleParent(ctx context.Context, q rowQuerier, childID, parentID, wsID string) error {
	if childID == parentID {
		return errParentCycle
	}
	seen := map[string]bool{parentID: true}
	current := parentID
	for i := 0; i < maxParentChainDepth; i++ {
		var next sql.NullString
		err := q.QueryRowContext(ctx,
			`SELECT parent_issue_id FROM missions WHERE id = ? AND workspace_id = ?`,
			current, wsID).Scan(&next)
		if errors.Is(err, sql.ErrNoRows) {
			// The chain left the workspace (or the row is gone). Nothing above
			// this point is reachable, so no cycle can be closed through it.
			return nil
		}
		if err != nil {
			return err
		}
		if !next.Valid || next.String == "" {
			return nil // reached a root
		}
		if next.String == childID {
			return errParentCycle
		}
		if seen[next.String] {
			// A pre-existing loop that does not include childID. Not this
			// caller's fault and not this caller's problem — but continuing
			// would spin, so stop here and allow the write.
			return nil
		}
		seen[next.String] = true
		current = next.String
	}
	// Depth bound hit: treat as a cycle rather than writing blind.
	return errParentCycle
}
