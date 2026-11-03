package memory

import (
	"context"
	"database/sql"
	"fmt"
	"unicode/utf8"
)

// RekeyAgentScopeTx moves an agent-scope ledger key "agent:<from>/..." to
// "agent:<to>/..." inside the caller's transaction: the revision anchors and
// the unconfirmed (intent/renamed) mutations. Confirmed and conflicted rows
// are append-only audit history and keep their key.
//
// The API uses it when a soft-deleted agent's slug is freed for reuse (#4840).
// A write of that agent still in flight is fenced by the ledger itself, not by
// the file lock (the lock is per canonical file and the new agent's file is a
// different one): confirmMutation resolves the key from its mutation row, so
// a moved intent confirms under the moved key, and the anchor compare-and-swap
// in writeIntent/confirmMutation refuses an intent whose anchor moved away
// after it was read (#4862).
func RekeyAgentScopeTx(ctx context.Context, tx *sql.Tx, workspaceID, from, to string) error {
	oldPrefix, newPrefix := "agent:"+from+"/", "agent:"+to+"/"
	n := utf8.RuneCountInString(oldPrefix) // SQLite substr counts characters
	// Exact prefix comparison, not LIKE: '_' in a slug is a LIKE wildcard.
	if _, err := tx.ExecContext(ctx, `
		UPDATE memory_revisions
		   SET path = ? || substr(path, ? + 1), scope = ?
		 WHERE workspace_id = ? AND substr(path, 1, ?) = ?`,
		newPrefix, n, "agent:"+to, workspaceID, n, oldPrefix); err != nil {
		return fmt.Errorf("move agent memory anchors: %w", err)
	}
	// Unconfirmed writes are recovered per audit key against the CALLER's
	// file before every write (recoverFileLocked). Left under the old key, the
	// next agent with that slug would have the deleted agent's queued content
	// replayed into its own memory. Boot recovery still finds them by
	// canonical_path, which stays as recorded.
	if _, err := tx.ExecContext(ctx, `
		UPDATE memory_mutations
		   SET path = ? || substr(path, ? + 1), scope = ?
		 WHERE workspace_id = ? AND substr(path, 1, ?) = ? AND state IN (?, ?)`,
		newPrefix, n, "agent:"+to, workspaceID, n, oldPrefix,
		mutationStateIntent, mutationStateRenamed); err != nil {
		return fmt.Errorf("move agent unconfirmed memory writes: %w", err)
	}
	return nil
}
