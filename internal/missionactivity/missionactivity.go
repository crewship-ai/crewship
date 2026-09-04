// Package missionactivity is the one write path into mission_activity —
// the issue event log PRD-ISSUES-AND-ROUTINES-2026 §9.1 widens
// mission_activity into (work package B1).
//
// Before this package there were three call sites that INSERT INTO
// mission_activity directly: internal/api/issue_events.go's shared emitter
// (issueEvents.log), and two writers that bypass it entirely
// (internal/api/assignments_run.go and
// internal/orchestrator/mission_tasks_completion.go) — named in
// issue_events.go's own header comment as the debt #1768 left behind. §9.1
// requires every one of them to allocate `seq` through a single path, because
// "a row without seq is invisible to every cursor" and UNIQUE(mission_id,
// seq) only holds if nothing computes a seq value except this function.
//
// internal/orchestrator cannot import internal/api (internal/api already
// imports internal/orchestrator, via AssignmentHandler.orch), so the shared
// path has to live below both. issue_events.go's issueEvents.log calls Emit
// for the row write and then does its own journal/hub fan-out with the
// (seq, workspace_id) it gets back; internal/orchestrator's completion path
// calls Emit directly and does not — see mission_tasks_completion.go's call
// site for why that split is a deliberate, documented choice rather than a
// gap.
package missionactivity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Entry is one write to mission_activity. ID is supplied by the caller
// (every existing call site already has its own CUID/ID generator; this
// package does not add a fourth) rather than generated here.
type Entry struct {
	ID          string
	MissionID   string
	ActorType   string
	ActorID     string
	Action      string
	Details     string
	PayloadJSON string
	SourceKind  string
	SourceID    string
}

// Written is what Emit hands back: the row's allocated seq and the
// mission's workspace/crew, so a caller that also needs to emit a journal
// entry (issueEvents.log) does not have to re-query missions for it.
type Written struct {
	Seq         int
	WorkspaceID string
	CrewID      string
}

// Emit resolves the mission's workspace, allocates the next per-mission
// `seq`, and inserts the row — all inside one write transaction.
//
// Race-free allocation. database.Open sets `_txlock=immediate` on every
// connection (internal/database/database.go), so db.BeginTx here acquires
// SQLite's single write lock at BEGIN, before the SELECT below runs — not at
// the first write statement, which is the default DEFERRED behaviour and
// would leave a read-then-write gap two concurrent transactions could both
// observe the same MAX(seq). With an immediate lock, a second concurrent
// Emit call blocks (up to busy_timeout) until this transaction commits, then
// reads the row this one just wrote — the same "SQLite serializes writers"
// property §9.1 invokes for the nextIssueIdentifierTx pattern this mirrors,
// re-keyed on mission_id instead of (workspace_id, prefix). The
// UNIQUE(mission_id, seq) index (20260904095700_mission_activity_widen.sql)
// is the belt to this suspenders: if the locking assumption above is ever
// wrong on some future driver/pragma combination, a collision fails loudly
// at INSERT instead of silently duplicating a seq.
func Emit(ctx context.Context, db *sql.DB, e Entry) (Written, error) {
	if e.ID == "" || e.MissionID == "" || e.ActorType == "" || e.Action == "" {
		return Written{}, fmt.Errorf("missionactivity: id, mission_id, actor_type and action are required")
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Written{}, fmt.Errorf("missionactivity: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Some legacy callers pass a chat id as the mission id (issue_events.go's
	// own comment on the same lookup) — ErrNoRows there is not this
	// function's error to raise, it just means workspace/crew stay empty and
	// the row is written without them, exactly as issueEvents.log has always
	// tolerated.
	var workspaceID, crewID sql.NullString
	if err := tx.QueryRowContext(ctx,
		`SELECT workspace_id, crew_id FROM missions WHERE id = ?`, e.MissionID,
	).Scan(&workspaceID, &crewID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Written{}, fmt.Errorf("missionactivity: lookup mission: %w", err)
	}

	var seq int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0) + 1 FROM mission_activity WHERE mission_id = ?`, e.MissionID,
	).Scan(&seq); err != nil {
		return Written{}, fmt.Errorf("missionactivity: allocate seq: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	wsVal := nullableString(workspaceID.String)
	payloadVal := nullableString(e.PayloadJSON)
	sourceKindVal := nullableString(e.SourceKind)
	sourceIDVal := nullableString(e.SourceID)

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO mission_activity
		    (id, mission_id, actor_type, actor_id, action, details, created_at,
		     workspace_id, seq, payload_json, source_kind, source_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.MissionID, e.ActorType, e.ActorID, e.Action, e.Details, now,
		wsVal, seq, payloadVal, sourceKindVal, sourceIDVal,
	); err != nil {
		return Written{}, fmt.Errorf("missionactivity: insert: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return Written{}, fmt.Errorf("missionactivity: commit: %w", err)
	}

	return Written{Seq: seq, WorkspaceID: workspaceID.String, CrewID: crewID.String}, nil
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
