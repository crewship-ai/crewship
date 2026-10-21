package restricteddispatch

import (
	"context"
	"database/sql"
	"errors"

	"github.com/crewship-ai/crewship/internal/access"
)

// PublicationProviderIn is a read-only host seam. The authenticated compiler
// supplies exact workspace/crew/agent selection and separately captures the
// publisher's explicit Run/Delegate grants; this is not a trusted-role bypass.
func (a Authority) PublicationProviderIn(ctx context.Context, tx *sql.Tx, workspace, crew, agent string) (access.PublicationProviderSnapshot, error) {
	var declaration access.PublicationProviderSnapshot
	if tx == nil || workspace == "" || crew == "" || agent == "" {
		return declaration, access.ErrDenied
	}
	var selectedCrew string
	err := tx.QueryRowContext(ctx, `SELECT a.crew_id FROM agents a
 JOIN crews c ON c.id=a.crew_id AND c.workspace_id=a.workspace_id
 JOIN workspaces w ON w.id=a.workspace_id
 WHERE a.id=? AND a.workspace_id=? AND a.crew_id=? AND a.deleted_at IS NULL
 AND c.deleted_at IS NULL AND w.deleted_at IS NULL`, agent, workspace, crew).Scan(&selectedCrew)
	if errors.Is(err, sql.ErrNoRows) {
		return declaration, access.ErrDenied
	}
	if err != nil {
		return declaration, err
	}
	snapshot, err := providerCurrent(ctx, tx, access.Attempt{Workspace: workspace, Agent: agent}, "")
	if err != nil {
		return declaration, err
	}
	if !delegatedProfile(snapshot.Profile) {
		return declaration, access.ErrDenied
	}
	return access.PublicationProviderSnapshot{
		WorkspaceID: workspace, CrewID: selectedCrew, AgentID: agent, Profile: snapshot.Profile,
		PolicyHash: delegationPolicyHash(snapshot), CredentialID: snapshot.Credential,
		GrantID: snapshot.Grant, CredentialRevision: snapshot.revision(), Model: snapshot.Model,
		GrantExpiry: snapshot.GrantExpiry.String, KeyExpiry: snapshot.KeyExpiry.String,
	}, nil
}
