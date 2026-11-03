package api

import (
	"context"
	"database/sql"
	"fmt"
	"unicode/utf8"
)

// freeDeletedAgentSlug releases a slug still held by a soft-deleted agent so
// a new or renamed agent can take it (the agents UNIQUE constraint covers
// tombstones). The deleted agent keeps its identity under
// "<slug>_deleted_<id>", and so do its memory revision anchors: the
// agent-scope ledger key is workspace-wide ("agent:<slug>/<file>") while the
// file lives under the agent's crew, so an anchor left behind would be
// inherited by the next agent with that slug, and every memory.write of that
// agent would fail the drift check against a file it never had (#4840).
// History (memory_versions, memory_mutations) is append-only audit and is not
// rewritten.
func freeDeletedAgentSlug(ctx context.Context, db *sql.DB, workspaceID, slug string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT id FROM agents WHERE workspace_id = ? AND slug = ? AND deleted_at IS NOT NULL`, workspaceID, slug)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	if len(ids) > 1 {
		// UNIQUE (workspace_id, slug) makes this unreachable; refuse rather
		// than guess which tombstone owns the anchors.
		return fmt.Errorf("free deleted agent slug %q: %d tombstones hold it", slug, len(ids))
	}
	renamed := slug + "_deleted_" + ids[0]
	oldPrefix, newPrefix := "agent:"+slug+"/", "agent:"+renamed+"/"
	n := utf8.RuneCountInString(oldPrefix) // SQLite substr counts characters
	// Exact prefix comparison, not LIKE: '_' in a slug is a LIKE wildcard.
	if _, err := tx.ExecContext(ctx, `
		UPDATE memory_revisions
		   SET path = ? || substr(path, ? + 1), scope = ?
		 WHERE workspace_id = ? AND substr(path, 1, ?) = ?`,
		newPrefix, n, "agent:"+renamed, workspaceID, n, oldPrefix); err != nil {
		return fmt.Errorf("move deleted agent memory anchors: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agents SET slug = ? WHERE id = ? AND deleted_at IS NOT NULL`, renamed, ids[0]); err != nil {
		return err
	}
	return tx.Commit()
}
