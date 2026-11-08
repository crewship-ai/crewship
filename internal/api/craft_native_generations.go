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
// revoke-then-restore cycles. A direct grant moves only the (Page, recipient)
// pair in mooring_page_grant_generations, never the user row: a grant on
// another Page must not retire this session (#5123). They are not an ACL: every
// check still evaluates the canonical Page policy, membership and original
// credential live.
type craftNativeGenerations struct {
	user, page, grant string
}

// pageGrantGeneration reads, and on a fresh admission (capture) first installs
// if absent, the (Page, principal) direct-grant generation. Never call capture
// from reconnect or revalidation. An absent or malformed row denies.
func pageGrantGeneration(ctx context.Context, tx *sql.Tx, workspace, page, kind, principal string, capture bool) (string, error) {
	if tx == nil || workspace == "" || page == "" || principal == "" || (kind != "user" && kind != "agent") {
		return "", errCraftNativeAccess
	}
	if capture {
		if _, err := tx.ExecContext(ctx, `INSERT INTO mooring_page_grant_generations(workspace_id,page_id,principal_kind,principal_id) SELECT ?,?,?,? WHERE NOT EXISTS(SELECT 1 FROM mooring_page_grant_generations WHERE workspace_id=? AND page_id=? AND principal_kind=? AND principal_id=?)`, workspace, page, kind, principal, workspace, page, kind, principal); err != nil {
			return "", err
		}
	}
	var revision string
	if err := tx.QueryRowContext(ctx, `SELECT CAST(revision AS TEXT) FROM mooring_page_grant_generations WHERE workspace_id=? AND page_id=? AND principal_kind=? AND principal_id=?`, workspace, page, kind, principal).Scan(&revision); err != nil {
		return "", errCraftNativeAccess
	}
	if !chataudience.ValidUserAuthorityRevision(revision) {
		return "", errCraftNativeAccess
	}
	return revision, nil
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
	if _, err := pageGrantGeneration(ctx, tx, scope.WorkspaceID, scope.ArtifactID, "user", user, true); err != nil {
		return craftNativeGenerations{}, err
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
	grant, err := pageGrantGeneration(ctx, tx, scope.WorkspaceID, scope.ArtifactID, "user", user, false)
	if err != nil {
		return craftNativeGenerations{}, errCraftNativeAccess
	}
	g.grant = grant
	return g, nil
}

// craftNativeGenerationsCurrent reports whether the session's captured
// generations are still the current ones.
func craftNativeGenerationsCurrent(ctx context.Context, tx *sql.Tx, entry *craftNativeSession) error {
	if entry == nil || entry.generations.user == "" || entry.generations.page == "" || entry.generations.grant == "" {
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
