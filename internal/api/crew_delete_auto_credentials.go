package api

// Crew delete → the crew's auto-managed service credentials (#2771).
//
// `crewship apply` mints one AUTO_MANAGED credential per catalogued datastore
// a crew declares under `services:` (REDIS_PASSWORD, POSTGRES_PASSWORD, ...).
// The row is a workspace credential, so it outlived the crew: nothing used it
// any more, and the next manifest declaring the same service was refused with
// "clashes with an existing workspace credential" until someone deleted it by
// hand. With the same slug it was worse — the stale row was taken as the new
// crew's own, so the agents got the old password and the fresh sidecar a new
// one.
//
// The cleanup deletes a credential only when the stored row PROVES it was
// minted for this crew, and nothing outside this crew still uses it:
//
//   - workspace_id is the crew's workspace;
//   - provider = 'AUTO_MANAGED' AND created_by_actor_type = 'system' AND
//     provisioned_for_service = '<crew-slug>/<service>'. The create handler
//     refuses provisioned_for_service on any other provider/actor pair, so
//     the triple is a server-enforced marker, not a name a user can collide
//     with. A user's REDIS_PASSWORD never matches: it carries no tag;
//   - the slug in the tag is the deleted crew's. Live crew slugs are unique
//     per workspace, so no other live crew can hold that tag;
//   - no live consumer outside the deleted crew references the row (see
//     autoCredentialConsumers). A credential another crew has been wired to
//     is shared, and it stays.
//
// What this does NOT catch, by design: rows minted before a crew's slug was
// changed (tagged with the old slug), and orphans left by crews deleted
// before this fix. Neither is provably this crew's, so both stay for an
// operator to remove with `crewship credential delete`.

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"
)

// autoCredentialConsumers is the SQL predicate (over the credentials row being
// deleted, with the deleted crew's id bound as every `?`) that is TRUE when anything
// still alive outside the deleted crew references the credential. The crew's
// own agents are already tombstoned by the time this runs, so their
// agent_credentials rows do not count.
//
// Every table holding a foreign key to credentials(id) must be classified as
// a consumer (checked here) or as the credential's own history/data;
// TestAutoCredentialConsumers_ClassifyEveryCredentialReference fails on a new
// one, because an unclassified reference is a consumer this cleanup would not
// see.
const autoCredentialConsumers = `(
	EXISTS (SELECT 1 FROM agent_credentials ac JOIN agents a ON a.id = ac.agent_id
	        WHERE ac.credential_id = credentials.id AND a.deleted_at IS NULL)
 OR EXISTS (SELECT 1 FROM agent_mcp_bindings mb JOIN agents a ON a.id = mb.agent_id
	        WHERE mb.credential_id = credentials.id AND a.deleted_at IS NULL)
 OR EXISTS (SELECT 1 FROM credential_crews cc JOIN crews cr ON cr.id = cc.crew_id
	        WHERE cc.credential_id = credentials.id AND cc.crew_id != ? AND cr.deleted_at IS NULL)
 OR EXISTS (SELECT 1 FROM credential_bindings cb
	        LEFT JOIN crews cr ON cr.id = cb.crew_id
	        LEFT JOIN agents a ON a.id = cb.agent_id
	        WHERE cb.credential_id = credentials.id AND (
	              cb.scope = 'WORKSPACE'
	           OR (cb.scope = 'CREW' AND cb.crew_id != ? AND cr.deleted_at IS NULL)
	           OR (cb.scope = 'AGENT' AND a.deleted_at IS NULL)))
 OR EXISTS (SELECT 1 FROM keeper_governance_settings WHERE gov_model_credential_id = credentials.id)
 OR EXISTS (SELECT 1 FROM keeper_aux_settings WHERE credential_id = credentials.id)
 OR EXISTS (SELECT 1 FROM mission_code_links WHERE credential_id = credentials.id)
 OR EXISTS (SELECT 1 FROM provider_login_pool_members WHERE credential_id = credentials.id)
 OR EXISTS (SELECT 1 FROM restricted_workflow_provider_policies WHERE credential_id = credentials.id)
)`

// removeCrewAutoCredentials soft-deletes the deleted crew's own auto-managed
// service credentials and returns their names. It runs after the crew and its
// agents are tombstoned. Each row is re-checked and removed in one guarded
// UPDATE inside its own transaction, so a binding created between the select
// and the delete keeps the row.
func removeCrewAutoCredentials(ctx context.Context, db *sql.DB, logger *slog.Logger, workspaceID, crewID, crewSlug, ip string) ([]string, error) {
	if crewSlug == "" {
		return nil, nil
	}
	prefix := crewSlug + "/"
	rows, err := db.QueryContext(ctx, `
		SELECT id, name FROM credentials
		 WHERE workspace_id = ? AND deleted_at IS NULL
		   AND provider = 'AUTO_MANAGED' AND created_by_actor_type = 'system'
		   AND provisioned_for_service IS NOT NULL
		   AND substr(provisioned_for_service, 1, ?) = ?
		 ORDER BY name`,
		workspaceID, len(prefix), prefix)
	if err != nil {
		return nil, fmt.Errorf("list auto-managed credentials: %w", err)
	}
	type candidate struct{ id, name string }
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.name); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan auto-managed credential: %w", err)
		}
		candidates = append(candidates, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list auto-managed credentials: %w", err)
	}

	removed := []string{}
	var firstErr error
	for _, c := range candidates {
		ok, err := removeOneAutoCredential(ctx, db, workspaceID, crewID, prefix, c.id)
		if err != nil {
			if logger != nil {
				logger.Warn("remove crew auto-managed credential", "crew_id", crewID, "credential_id", c.id, "error", err)
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if !ok {
			continue // shared, or changed under us — it stays
		}
		removed = append(removed, c.name)
		recordCredentialEventBestEffort(ctx, db, logger, c.id, AuditEventRevoke, "", ip,
			map[string]any{"soft_delete": true, "reason": "crew_deleted", "crew_id": crewID})
	}
	return removed, firstErr
}

func removeOneAutoCredential(ctx context.Context, db *sql.DB, workspaceID, crewID, prefix, credID string) (bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	now := time.Now().UTC().Format(time.RFC3339)
	res, err := tx.ExecContext(ctx, `
		UPDATE credentials SET deleted_at = ?
		 WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL
		   AND provider = 'AUTO_MANAGED' AND created_by_actor_type = 'system'
		   AND provisioned_for_service IS NOT NULL
		   AND substr(provisioned_for_service, 1, ?) = ?
		   AND NOT `+autoCredentialConsumers,
		now, credID, workspaceID, len(prefix), prefix, crewID, crewID)
	if err != nil {
		return false, fmt.Errorf("soft-delete: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	// The same join rows credential delete clears (credentials.go Delete):
	// the deleted crew's own assignments and claims on slots. Every other
	// reference was just proved absent.
	for _, q := range []string{
		"DELETE FROM agent_credentials WHERE credential_id = ?",
		"DELETE FROM credential_bindings WHERE credential_id = ?",
		"DELETE FROM credential_crews WHERE credential_id = ?",
		"UPDATE agent_mcp_bindings SET credential_id = NULL WHERE credential_id = ?",
	} {
		if _, err := tx.ExecContext(ctx, q, credID); err != nil {
			return false, fmt.Errorf("clear references: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
