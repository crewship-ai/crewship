package api

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/crewship-ai/crewship/internal/memory"
)

// freeDeletedAgentSlug releases a slug still held by a soft-deleted agent so
// a new or renamed agent can take it (the agents UNIQUE constraint covers
// tombstones). The deleted agent keeps its identity under
// "<slug>_deleted_<id>", and so do its memory revision anchors: the
// agent-scope ledger key is workspace-wide ("agent:<slug>/<file>") while the
// file lives under the agent's crew, so an anchor left behind would be
// inherited by the next agent with that slug, and every memory.write of that
// agent would fail the drift check against a file it never had (#4840).
// Its unconfirmed memory writes move too; confirmed history (memory_versions,
// confirmed memory_mutations) is append-only audit and keeps its key.
func freeDeletedAgentSlug(ctx context.Context, db *sql.DB, workspaceID, slug string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := freeDeletedAgentSlugTx(ctx, tx, workspaceID, slug); err != nil {
		return err
	}
	return tx.Commit()
}

// freeDeletedAgentSlugTx does the same inside the caller's transaction.
func freeDeletedAgentSlugTx(ctx context.Context, tx *sql.Tx, workspaceID, slug string) error {
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
	// Anchors and unconfirmed writes move with the tombstone; a write of the
	// deleted agent still in flight is fenced by the ledger (#4862).
	if err := memory.RekeyAgentScopeTx(ctx, tx, workspaceID, slug, renamed); err != nil {
		return fmt.Errorf("free deleted agent slug %q: %w", slug, err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agents SET slug = ? WHERE id = ? AND deleted_at IS NOT NULL`, renamed, ids[0]); err != nil {
		return err
	}
	return nil
}
