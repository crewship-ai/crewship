package cartographer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/crewship-ai/crewship/internal/journal"
)

// Restore loads a checkpoint, validates that its journal cursor still
// exists in the journal, and returns a RestoreResult containing the
// checkpoint plus the set of journal entries strictly newer than the
// cursor (scoped to the same mission). The UI surfaces WarnDivergence
// to the operator as "restoring will abandon these N events" — but
// Restore itself is non-destructive: no DB rows are mutated, no
// containers are torn down, no memory is rewound. That work is
// deferred to whatever handler owns the post-restore policy.
//
// An EntryCheckpointRestored is emitted into the journal so the
// restore attempt is itself auditable regardless of whether the
// caller follows through with a rewind.
//
// A nil Emitter is tolerated for tests.
func Restore(ctx context.Context, db *sql.DB, j journal.Emitter, workspaceID, checkpointID string) (*RestoreResult, error) {
	if workspaceID == "" || checkpointID == "" {
		return nil, errors.New("cartographer: workspace_id and checkpoint_id required")
	}
	cp, err := Get(ctx, db, workspaceID, checkpointID)
	if err != nil {
		return nil, err
	}

	// Validate the cursor still resolves. The journal is append-only so
	// disappearing entries would indicate manual DB tampering or a
	// broken migration, but it's cheap to check and saves an obscure
	// foreign-key error later.
	if cp.JournalCursor != "" {
		var exists int
		err := db.QueryRowContext(ctx,
			`SELECT 1 FROM journal_entries WHERE workspace_id = ? AND id = ? LIMIT 1`,
			cp.WorkspaceID, cp.JournalCursor).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("cartographer: journal cursor %q no longer exists", cp.JournalCursor)
		}
		if err != nil {
			return nil, fmt.Errorf("cartographer: validate cursor: %w", err)
		}
	}

	divergence, err := newerEntryIDs(ctx, db, cp.MissionID, cp.JournalCursor)
	if err != nil {
		return nil, err
	}

	if j != nil {
		_, _ = j.Emit(ctx, journal.Entry{
			WorkspaceID: cp.WorkspaceID,
			CrewID:      cp.CrewID,
			MissionID:   cp.MissionID,
			Type:        journal.EntryCheckpointRestored,
			Severity:    journal.SeverityNotice,
			ActorType:   journal.ActorUser,
			ActorID:     cp.CreatedBy,
			Summary:     fmt.Sprintf("restore preview for checkpoint %s (%d divergent entries)", cp.ID, len(divergence)),
			Refs: map[string]any{
				"checkpoint_id":    cp.ID,
				"journal_cursor":   cp.JournalCursor,
				"mission_id":       cp.MissionID,
				"divergence_count": len(divergence),
			},
		})
	}

	return &RestoreResult{
		Checkpoint:     cp,
		JournalCursor:  cp.JournalCursor,
		WarnDivergence: divergence,
	}, nil
}

// newerEntryIDs returns journal entry IDs in the mission strictly newer
// than cursor, ordered oldest-first so the UI can walk them in the
// same direction events happened. "Newer" is determined by (ts, id) —
// matching the journal's own ordering convention, where id breaks ties
// within a millisecond.
//
// When cursor is empty (e.g. mission had no entries at capture time),
// everything currently on the mission is divergence.
func newerEntryIDs(ctx context.Context, db *sql.DB, missionID, cursor string) ([]string, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if cursor == "" {
		rows, err = db.QueryContext(ctx,
			`SELECT id FROM journal_entries WHERE mission_id = ? ORDER BY ts ASC, id ASC`,
			missionID)
	} else {
		// Grab the cursor's timestamp, then select entries with (ts, id)
		// strictly greater. A single round-trip with a correlated subquery
		// keeps the semantics self-contained.
		rows, err = db.QueryContext(ctx, `SELECT id FROM journal_entries
			WHERE mission_id = ?
			AND (ts, id) > (
				SELECT ts, id FROM journal_entries WHERE id = ?
			)
			ORDER BY ts ASC, id ASC`,
			missionID, cursor)
	}
	if err != nil {
		return nil, fmt.Errorf("cartographer: divergence lookup: %w", err)
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
