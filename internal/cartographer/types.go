// Package cartographer implements mission checkpointing and time-travel.
//
// A checkpoint captures a moment in a mission's life: the latest journal
// entry id (the "cursor"), plus a materialized snapshot of in-memory state
// we can't reconstruct from the append-only journal alone — hashes of each
// agent's memory files, the set of pending mission_tasks, the set of
// running assignments, and the crew container id if one is attached.
//
// The journal is the source of truth; a checkpoint is just a pointer into
// it. Restore is intentionally non-destructive: it returns the snapshot
// plus a list of journal entries that would be "abandoned" if the caller
// chose to rewind. The actual rewind (if any) is a UX/policy decision
// deferred to the handler that calls Restore.
//
// Fork clones the parent mission row into a new missions row, copies the
// mission_tasks that existed at the cursor, and stamps a new checkpoint
// whose fork_of points back to the parent. This gives the UI a "branch
// from here" affordance without ever mutating history.
package cartographer

import "time"

// StateSnapshot is the materialized in-memory state that sits alongside
// the journal cursor. Everything in here is derived data — if it were
// purely event-sourced we'd replay journal entries on restore. We snapshot
// it because the per-agent memory directories and container state aren't
// all written to the journal, so replaying would miss them.
//
// Meta is intentionally open-ended so future fields (keeper state, open
// approvals, watchlist counters) can land without a schema change. Keep
// it small — the whole struct round-trips through a TEXT JSON column.
type StateSnapshot struct {
	// AgentMemory maps agent slug/id → sha256 hex digest of the agent's
	// memory directory contents. Hash only, not the bytes, so the
	// snapshot stays compact; the raw memory files are preserved on
	// disk by whoever called Capture.
	AgentMemory map[string]string `json:"agent_memory"`

	// PendingTasks lists mission_tasks IDs with status != COMPLETED at
	// capture time. On restore the caller can compare against the
	// current set to see what moved.
	PendingTasks []string `json:"pending_tasks"`

	// OpenAssignments lists assignment IDs in RUNNING state.
	OpenAssignments []string `json:"open_assignments"`

	// CrewContainerID is the Docker container name/id the crew was
	// bound to when captured. Empty string when not applicable.
	CrewContainerID string `json:"crew_container_id,omitempty"`

	// Meta is a grab-bag for future extension.
	Meta map[string]any `json:"meta,omitempty"`
}

// Checkpoint mirrors the `checkpoints` row plus the decoded StateSnapshot.
// ForkOf is a pointer so the JSON omits it cleanly when the checkpoint is
// not a fork.
type Checkpoint struct {
	ID            string        `json:"id"`
	WorkspaceID   string        `json:"workspace_id"`
	CrewID        string        `json:"crew_id,omitempty"`
	MissionID     string        `json:"mission_id"`
	Label         string        `json:"label,omitempty"`
	JournalCursor string        `json:"journal_cursor"`
	State         StateSnapshot `json:"state"`
	ForkOf        string        `json:"fork_of,omitempty"`
	CreatedBy     string        `json:"created_by,omitempty"`
	CreatedAt     time.Time     `json:"created_at"`
}

// RestoreResult is what Restore hands back. It does NOT mutate anything;
// the caller inspects WarnDivergence and decides whether to actually
// rewind downstream state (abort running assignments, reset memory, etc).
type RestoreResult struct {
	Checkpoint     *Checkpoint `json:"checkpoint"`
	JournalCursor  string      `json:"journal_cursor"`
	WarnDivergence []string    `json:"warn_divergence"`
}
