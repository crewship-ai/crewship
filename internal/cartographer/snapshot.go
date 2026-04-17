package cartographer

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// Capture builds a StateSnapshot for a mission plus the current journal
// cursor. Both are returned so the caller can hand them to Create in one
// step — keeping them adjacent in a single function call is the closest
// we get to "atomic capture" across two different concerns.
//
// Capture is intentionally read-only. It never writes to the journal or
// to any domain tables; nothing about "taking a snapshot" should mutate
// the system it snapshots.
//
// journalCursor = latest journal_entries.id scoped to the mission,
// ordered by (ts, id). When the mission has no journal entries yet we
// still return a valid snapshot but cursor is empty — callers that need
// a non-empty cursor should check and bail.
func Capture(ctx context.Context, db *sql.DB, missionID string) (StateSnapshot, string, error) {
	var snap StateSnapshot
	if missionID == "" {
		return snap, "", errors.New("cartographer: mission_id required")
	}

	cursor, err := latestJournalCursor(ctx, db, missionID)
	if err != nil {
		return snap, "", err
	}

	pending, err := pendingTasks(ctx, db, missionID)
	if err != nil {
		return snap, "", err
	}
	snap.PendingTasks = pending

	open, err := openAssignments(ctx, db, missionID)
	if err != nil {
		return snap, "", err
	}
	snap.OpenAssignments = open

	snap.AgentMemory = map[string]string{}
	return snap, cursor, nil
}

// CaptureMemoryDir hashes the contents of an agent's memory directory
// and stores the digest under agentKey in the snapshot. Exposed so
// callers that have a filesystem layout (crew container host mount,
// per-agent home dir) can wire it up without the cartographer package
// taking a dependency on any particular memory helper.
//
// Digest algorithm: walk the directory lexically, for each regular
// file feed "path\n" followed by the file bytes followed by "\n" into
// a single sha256. Hidden files and subdirectories are included. If
// the directory doesn't exist we record an empty string — the absence
// is not an error (a fresh agent legitimately has no memory yet).
func CaptureMemoryDir(snap *StateSnapshot, agentKey, dir string) error {
	if snap == nil {
		return errors.New("cartographer: nil snapshot")
	}
	if agentKey == "" {
		return errors.New("cartographer: agent_key required")
	}
	if snap.AgentMemory == nil {
		snap.AgentMemory = map[string]string{}
	}
	digest, err := hashDir(dir)
	if err != nil {
		return err
	}
	snap.AgentMemory[agentKey] = digest
	return nil
}

// hashDir walks dir in lexical order and returns the sha256 hex of
// (relPath, fileBytes) pairs. Non-existent dir → "".
func hashDir(dir string) (string, error) {
	info, err := os.Stat(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("cartographer: stat %s: %w", dir, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("cartographer: %s is not a directory", dir)
	}

	var files []string
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("cartographer: walk %s: %w", dir, err)
	}
	sort.Strings(files)

	h := sha256.New()
	for _, rel := range files {
		abs := filepath.Join(dir, rel)
		b, err := os.ReadFile(abs)
		if err != nil {
			// Skip files we can't read (race with eviction, permission
			// blips). Record the path but not bytes so two snapshots
			// with the same unreadable file still match.
			_, _ = h.Write([]byte(rel))
			_, _ = h.Write([]byte{'\n'})
			continue
		}
		_, _ = h.Write([]byte(rel))
		_, _ = h.Write([]byte{'\n'})
		_, _ = h.Write(b)
		_, _ = h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// latestJournalCursor returns the ID of the most recent journal entry
// scoped to mission_id. Empty string when there are no entries (valid:
// a mission may be snapshotted at the moment of creation before anything
// has been logged).
func latestJournalCursor(ctx context.Context, db *sql.DB, missionID string) (string, error) {
	var id sql.NullString
	err := db.QueryRowContext(ctx,
		`SELECT id FROM journal_entries WHERE mission_id = ? ORDER BY ts DESC, id DESC LIMIT 1`,
		missionID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("cartographer: cursor lookup: %w", err)
	}
	return id.String, nil
}

// pendingTasks returns mission_tasks IDs where status != 'COMPLETED'.
// We also exclude CANCELLED because those are terminal from the
// workflow's perspective — no restore action would resurrect them.
func pendingTasks(ctx context.Context, db *sql.DB, missionID string) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id FROM mission_tasks WHERE mission_id = ? AND status NOT IN ('COMPLETED','CANCELLED') ORDER BY id`,
		missionID)
	if err != nil {
		return nil, fmt.Errorf("cartographer: pending tasks: %w", err)
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

// openAssignments returns assignment IDs in RUNNING state for this
// mission. assignments has no mission_id column at migration-52 time,
// so we join through mission_tasks.assignment_id. Any RUNNING
// assignment attached to any task of this mission counts.
func openAssignments(ctx context.Context, db *sql.DB, missionID string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT a.id
		FROM assignments a
		INNER JOIN mission_tasks t ON t.assignment_id = a.id
		WHERE t.mission_id = ? AND a.status = 'RUNNING'
		ORDER BY a.id`, missionID)
	if err != nil {
		return nil, fmt.Errorf("cartographer: open assignments: %w", err)
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
