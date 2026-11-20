package api

// Deleting an agent revokes its explicit credential grants (#5529).
//
// An agent is deleted by a tombstone (agents.deleted_at), not a DELETE, so the
// ON DELETE CASCADE on agent_credentials.agent_id never fires. Before this the
// grant rows outlived their agent: `credential list` kept counting them, and
// `credential unassign` could not remove them because every agent lookup
// (rightly) answers "agent not found" for a tombstoned agent. The onboarding
// setup agent is also revived in place (deleted_at = NULL), which would have
// handed the revived agent every grant it held before it was deleted.
//
// What is removed, and what never is:
//
//   - only agent_credentials rows, i.e. the grant linking one agent to one
//     credential. The credential row, its encrypted material, its bindings,
//     crew links and audit history are never touched here;
//   - only rows whose agent is ALREADY tombstoned in the caller's transaction
//     (a.deleted_at IS NOT NULL) and belongs to the caller's workspace. The
//     predicate is in the SELECT and again in each DELETE, so a caller passing
//     the wrong scope cannot strip a live agent's grants;
//   - every removed grant gets a REVOKE credential_audit row in the SAME
//     transaction. An audit failure rolls the whole delete back, so a grant can
//     never disappear without a trace on the credential's timeline.
//
// Grants left behind by deletes made before this fix are repaired once by the
// migration 20261119175019_revoke_deleted_agent_credential_grants.

import (
	"context"
	"database/sql"
	"fmt"
)

// agentGrantScope selects which tombstoned agents' grants are revoked. The
// predicate is a constant chosen here, never caller-supplied SQL; it binds
// exactly one argument.
type agentGrantScope string

const (
	agentGrantScopeAgent     agentGrantScope = "a.id = ?"
	agentGrantScopeCrew      agentGrantScope = "a.crew_id = ?"
	agentGrantScopeWorkspace agentGrantScope = "a.workspace_id = ?"
)

// Reasons recorded in the REVOKE audit metadata.
const (
	grantRevokeReasonAgentDeleted     = "agent_deleted"
	grantRevokeReasonCrewDeleted      = "crew_deleted"
	grantRevokeReasonWorkspaceDeleted = "workspace_deleted"
)

// revokedAgentGrant describes one removed grant. It carries names and ids
// only, never a credential value.
type revokedAgentGrant struct {
	AssignmentID string `json:"assignment_id"`
	AgentID      string `json:"agent_id"`
	CredentialID string `json:"credential_id"`
	EnvVarName   string `json:"env_var_name"`
	ExpiresAt    string `json:"expires_at,omitempty"`
	LeaseSource  string `json:"lease_source,omitempty"`
}

// revokeDeletedAgentCredentialGrantsTx removes the explicit credential grants
// of the tombstoned agents selected by scope/scopeID in workspaceID, and audits
// each one inside tx. Call it after the agents' deleted_at has been written on
// the same transaction. It is idempotent: a second call finds nothing.
func revokeDeletedAgentCredentialGrantsTx(
	ctx context.Context,
	tx *sql.Tx,
	workspaceID string,
	scope agentGrantScope,
	scopeID string,
	reason string,
	ip string,
) ([]revokedAgentGrant, error) {
	switch scope {
	case agentGrantScopeAgent, agentGrantScopeCrew, agentGrantScopeWorkspace:
	default:
		return nil, fmt.Errorf("revoke agent credential grants: unknown scope %q", scope)
	}
	if workspaceID == "" || scopeID == "" {
		return nil, fmt.Errorf("revoke agent credential grants: workspace and scope id are required")
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT ac.id, ac.agent_id, ac.credential_id, COALESCE(ac.env_var_name, ''),
		       COALESCE(ac.expires_at, ''), COALESCE(ac.lease_source, '')
		  FROM agent_credentials ac
		  JOIN agents a ON a.id = ac.agent_id
		 WHERE a.workspace_id = ? AND a.deleted_at IS NOT NULL AND `+string(scope)+`
		 ORDER BY ac.agent_id, ac.id`,
		workspaceID, scopeID)
	if err != nil {
		return nil, fmt.Errorf("list deleted agents' credential grants: %w", err)
	}
	var grants []revokedAgentGrant
	for rows.Next() {
		var g revokedAgentGrant
		if err := rows.Scan(&g.AssignmentID, &g.AgentID, &g.CredentialID, &g.EnvVarName, &g.ExpiresAt, &g.LeaseSource); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan deleted agent credential grant: %w", err)
		}
		grants = append(grants, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list deleted agents' credential grants: %w", err)
	}

	for _, g := range grants {
		res, err := tx.ExecContext(ctx, `
			DELETE FROM agent_credentials
			 WHERE id = ? AND agent_id = ?
			   AND agent_id IN (SELECT id FROM agents WHERE workspace_id = ? AND deleted_at IS NOT NULL)`,
			g.AssignmentID, g.AgentID, workspaceID)
		if err != nil {
			return nil, fmt.Errorf("revoke credential grant %s: %w", g.AssignmentID, err)
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			return nil, fmt.Errorf("revoke credential grant %s not acknowledged: affected=%d error=%v", g.AssignmentID, n, err)
		}
		meta := map[string]any{
			"reason":        reason,
			"grant_source":  "explicit",
			"assignment_id": g.AssignmentID,
			"env_var_name":  g.EnvVarName,
		}
		if g.ExpiresAt != "" {
			meta["expires_at"] = g.ExpiresAt
		}
		if g.LeaseSource != "" {
			meta["lease_source"] = g.LeaseSource
		}
		if err := RecordCredentialEventTx(ctx, tx, g.CredentialID, AuditEventRevoke, g.AgentID, ip, meta); err != nil {
			return nil, fmt.Errorf("audit credential grant revoke %s: %w", g.AssignmentID, err)
		}
	}
	return grants, nil
}
