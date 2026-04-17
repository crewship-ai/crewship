package cartographer

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// ErrNotFound is returned by Get when no checkpoint matches the
// (workspace_id, id) pair. Keeping it as a sentinel lets handlers
// distinguish "not found" from "DB down" without string matching.
var ErrNotFound = errors.New("cartographer: checkpoint not found")

// newCheckpointID returns a prefixed random hex identifier. 16 hex chars
// (64 bits) is enough headroom for checkpoints — they're created by
// humans, not by hot-path code.
func newCheckpointID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return "cp_" + hex.EncodeToString(b[:])
}

// Create persists cp, assigns it a new ID, serializes the StateSnapshot
// to JSON, and emits a checkpoint.created entry into the journal. The
// write and the emit are not transactionally linked — if the emit fails
// the checkpoint row is still correct (journal entries are advisory for
// the UI, not required for restore). A nil Emitter is tolerated so
// tests that don't care about the audit trail can skip it.
//
// Caller passes cp with JournalCursor, State, MissionID, WorkspaceID,
// CrewID, Label, CreatedBy, and optionally ForkOf. ID and CreatedAt are
// assigned here.
func Create(ctx context.Context, db *sql.DB, j journal.Emitter, cp Checkpoint) (string, error) {
	if cp.WorkspaceID == "" {
		return "", errors.New("cartographer: workspace_id required")
	}
	if cp.MissionID == "" {
		return "", errors.New("cartographer: mission_id required")
	}
	if cp.JournalCursor == "" {
		return "", errors.New("cartographer: journal_cursor required")
	}

	id := newCheckpointID()
	if cp.State.AgentMemory == nil {
		cp.State.AgentMemory = map[string]string{}
	}
	if cp.State.PendingTasks == nil {
		cp.State.PendingTasks = []string{}
	}
	if cp.State.OpenAssignments == nil {
		cp.State.OpenAssignments = []string{}
	}
	stateJSON, err := json.Marshal(cp.State)
	if err != nil {
		return "", fmt.Errorf("cartographer: marshal state: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)

	_, err = db.ExecContext(ctx, `INSERT INTO checkpoints
		(id, workspace_id, crew_id, mission_id, label, journal_cursor, state_snapshot, fork_of, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id,
		cp.WorkspaceID,
		nullable(cp.CrewID),
		cp.MissionID,
		nullable(cp.Label),
		cp.JournalCursor,
		string(stateJSON),
		nullable(cp.ForkOf),
		nullable(cp.CreatedBy),
		now,
	)
	if err != nil {
		return "", fmt.Errorf("cartographer: insert: %w", err)
	}

	if j != nil {
		_, _ = j.Emit(ctx, journal.Entry{
			WorkspaceID: cp.WorkspaceID,
			CrewID:      cp.CrewID,
			MissionID:   cp.MissionID,
			Type:        journal.EntryCheckpointCreated,
			Severity:    journal.SeverityNotice,
			ActorType:   journal.ActorUser,
			ActorID:     cp.CreatedBy,
			Summary:     fmt.Sprintf("checkpoint %q @ cursor %s", cp.Label, cp.JournalCursor),
			Refs: map[string]any{
				"checkpoint_id":  id,
				"journal_cursor": cp.JournalCursor,
				"mission_id":     cp.MissionID,
			},
		})
	}

	return id, nil
}

// Get fetches a single checkpoint scoped to a workspace. Returns
// ErrNotFound if no row matches — callers typically turn that into a 404.
func Get(ctx context.Context, db *sql.DB, workspaceID, id string) (*Checkpoint, error) {
	if workspaceID == "" || id == "" {
		return nil, errors.New("cartographer: workspace_id and id required")
	}
	row := db.QueryRowContext(ctx, `SELECT
		id, workspace_id, crew_id, mission_id, label, journal_cursor, state_snapshot, fork_of, created_by, created_at
		FROM checkpoints WHERE workspace_id = ? AND id = ?`, workspaceID, id)
	cp, err := scanCheckpoint(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return cp, nil
}

// List returns checkpoints for a mission, newest first. limit <= 0 picks
// a sane default (50); the handler caps it before this runs.
func List(ctx context.Context, db *sql.DB, missionID string, limit int) ([]Checkpoint, error) {
	if missionID == "" {
		return nil, errors.New("cartographer: mission_id required")
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := db.QueryContext(ctx, `SELECT
		id, workspace_id, crew_id, mission_id, label, journal_cursor, state_snapshot, fork_of, created_by, created_at
		FROM checkpoints WHERE mission_id = ?
		ORDER BY created_at DESC, id DESC
		LIMIT ?`, missionID, limit)
	if err != nil {
		return nil, fmt.Errorf("cartographer: list: %w", err)
	}
	defer rows.Close()

	out := make([]Checkpoint, 0, limit)
	for rows.Next() {
		cp, err := scanCheckpoint(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *cp)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Delete hard-deletes a checkpoint scoped to a workspace. Children
// checkpoints (forks) reference this one via fork_of with ON DELETE SET
// NULL — see migration 52 — so deleting a parent does NOT cascade-drop
// its forks, it just orphans them. That's deliberate: forks are
// independent missions after the branch, and we don't want deleting the
// origin checkpoint to nuke a whole forked mission's history.
func Delete(ctx context.Context, db *sql.DB, workspaceID, id string) error {
	if workspaceID == "" || id == "" {
		return errors.New("cartographer: workspace_id and id required")
	}
	res, err := db.ExecContext(ctx, `DELETE FROM checkpoints WHERE workspace_id = ? AND id = ?`, workspaceID, id)
	if err != nil {
		return fmt.Errorf("cartographer: delete: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// scanner is the minimal interface both *sql.Row and *sql.Rows satisfy so
// scanCheckpoint can serve Get and List without duplication.
type scanner interface {
	Scan(dest ...any) error
}

func scanCheckpoint(s scanner) (*Checkpoint, error) {
	var (
		cp                                Checkpoint
		crewID, label, forkOf, createdBy  sql.NullString
		stateJSON, createdAtStr           string
	)
	if err := s.Scan(
		&cp.ID,
		&cp.WorkspaceID,
		&crewID,
		&cp.MissionID,
		&label,
		&cp.JournalCursor,
		&stateJSON,
		&forkOf,
		&createdBy,
		&createdAtStr,
	); err != nil {
		return nil, err
	}
	cp.CrewID = crewID.String
	cp.Label = label.String
	cp.ForkOf = forkOf.String
	cp.CreatedBy = createdBy.String
	if stateJSON != "" {
		if err := json.Unmarshal([]byte(stateJSON), &cp.State); err != nil {
			return nil, fmt.Errorf("cartographer: unmarshal state: %w", err)
		}
	}
	if cp.State.AgentMemory == nil {
		cp.State.AgentMemory = map[string]string{}
	}
	if cp.State.PendingTasks == nil {
		cp.State.PendingTasks = []string{}
	}
	if cp.State.OpenAssignments == nil {
		cp.State.OpenAssignments = []string{}
	}
	if t, err := parseTS(createdAtStr); err == nil {
		cp.CreatedAt = t
	}
	return &cp, nil
}

func parseTS(s string) (time.Time, error) {
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("cartographer: unparseable timestamp %q", s)
}

// nullable turns "" into sql NULL so fork_of / crew_id / label / created_by
// stay NULL rather than empty string. Keeps indexed "IS NULL" filters cheap.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
