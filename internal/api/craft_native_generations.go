package api

import (
	"context"
	"database/sql"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// craftNativeGenerations are the author-relevant, non-ABA generations of the
// admitted human and the admitted Page (mooring_authority_generations). They
// replace the broad workspace delivery epoch as the native session's
// continuity fence: workspaces.authority_revision also moves when anybody
// creates another Page, changes a grant on another Page or adds a member,
// which retired every live editor in the workspace (dev3 F1). These rows move
// only for changes whose recipients include this user or this Page, including
// revoke-then-restore cycles. They are not an ACL: every check still evaluates
// the canonical Page policy, membership and original credential live.
type craftNativeGenerations struct {
	user, page string
}

// captureCraftNativeGenerations runs only for a fresh authenticated admission,
// after canonical allow and inside the admission transaction. It installs the
// baseline rows if absent (never replacing or refreshing an existing row) and
// reads them back. Reconnect and revalidation use readCraftNativeGenerations.
func captureCraftNativeGenerations(ctx context.Context, tx *sql.Tx, scope mooring.EditLeaseScope, user string) (craftNativeGenerations, error) {
	if tx == nil || scope.WorkspaceID == "" || scope.ArtifactID == "" || user == "" {
		return craftNativeGenerations{}, errCraftNativeAccess
	}
	for _, k := range [][2]string{{"user", user}, {"page", scope.ArtifactID}} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO mooring_authority_generations(workspace_id,scope_kind,scope_id) SELECT ?,?,? WHERE NOT EXISTS(SELECT 1 FROM mooring_authority_generations WHERE workspace_id=? AND scope_kind=? AND scope_id=?)`, scope.WorkspaceID, k[0], k[1], scope.WorkspaceID, k[0], k[1]); err != nil {
			return craftNativeGenerations{}, err
		}
	}
	return readCraftNativeGenerations(ctx, tx, scope, user)
}

// SELECT-only. An absent or malformed row is uncertainty and denies access.
func readCraftNativeGenerations(ctx context.Context, tx *sql.Tx, scope mooring.EditLeaseScope, user string) (craftNativeGenerations, error) {
	var g craftNativeGenerations
	if tx == nil {
		return g, errCraftNativeAccess
	}
	for _, k := range []struct {
		kind, id string
		into     *string
	}{{"user", user, &g.user}, {"page", scope.ArtifactID, &g.page}} {
		if err := tx.QueryRowContext(ctx, `SELECT CAST(revision AS TEXT) FROM mooring_authority_generations WHERE workspace_id=? AND scope_kind=? AND scope_id=?`, scope.WorkspaceID, k.kind, k.id).Scan(k.into); err != nil {
			return craftNativeGenerations{}, errCraftNativeAccess
		}
		if !chataudience.ValidUserAuthorityRevision(*k.into) {
			return craftNativeGenerations{}, errCraftNativeAccess
		}
	}
	return g, nil
}

// craftNativeGenerationsCurrent reports whether the session's captured
// generations are still the current ones.
func craftNativeGenerationsCurrent(ctx context.Context, tx *sql.Tx, entry *craftNativeSession) error {
	if entry == nil || entry.generations.user == "" || entry.generations.page == "" {
		return errCraftNativeAccess
	}
	current, err := readCraftNativeGenerations(ctx, tx, entry.scope, entry.source.UserID)
	if err != nil {
		return err
	}
	if current != entry.generations {
		return errCraftNativeAccess
	}
	return nil
}
