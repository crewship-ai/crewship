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
// A write of that agent still in flight is fenced by the ledger, not by the
// file lock (the lock is per canonical file; the new agent's file is another
// one). A write that names its owner (MutateRequest.ScopeAgentID) records its
// intent only while that agent is live and owns the slug, and confirms only
// under a key the agent still owns, resolved from its mutation row, so a moved
// intent confirms under the moved key (#4862). The caller must rename the
// tombstone's slug in the same transaction: that rename is what the ownership
// check reads, and SQLite's single writer orders it against both checks.
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
