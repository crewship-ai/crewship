package access

import (
	"context"
	"database/sql"
	"errors"
)

// PublicationGrantBinding is host-only provenance for one selected right.
// It is neither a public policy projection nor an execution/admission handle.
// The compiler classifies authoring receipts separately from execution bounds.
// Mutable policy revisions and display names deliberately are not identities.
type PublicationGrantBinding struct {
	WorkspaceID           string `json:"-"`
	PublisherID           string `json:"-"`
	PublisherMembershipID string `json:"-"`
	Right                 Right  `json:"-"`
	GrantID               string `json:"-"`
	IssuerID              string `json:"-"`
	IssuerMembershipID    string `json:"-"`
}

// CapturePublicationGrantIn reads exact provenance in the caller's snapshot.
// The authenticated host selects the principal, workspace and required right;
// none of these arguments may originate in untrusted task authority claims.
// Unlike CheckIn, this requires an explicit restricted-member share even if
// the publisher later becomes trusted. Publication is not a role bypass.
func (s Store) CapturePublicationGrantIn(ctx context.Context, tx *sql.Tx, publisher, workspace string, right Right) (PublicationGrantBinding, error) {
	var binding PublicationGrantBinding
	if tx == nil || publisher == "" || workspace == "" {
		return binding, ErrDenied
	}
	if err := resourceExists(ctx, tx, workspace, right); err != nil {
		return binding, err
	}
	binding.WorkspaceID, binding.PublisherID, binding.Right = workspace, publisher, right
	err := tx.QueryRowContext(ctx, `SELECT recipient.id,g.id,g.created_by,issuer.id
 FROM workspace_members recipient
 JOIN users publisher ON publisher.id=recipient.user_id AND publisher.suspended_at IS NULL
 JOIN workspaces w ON w.id=recipient.workspace_id AND w.deleted_at IS NULL
 JOIN live_access_grants g ON g.member_id=recipient.id
 JOIN workspace_members issuer ON issuer.user_id=g.created_by AND issuer.workspace_id=recipient.workspace_id
 WHERE recipient.user_id=? AND recipient.workspace_id=? AND recipient.access_mode='restricted'
 AND g.resource_kind=? AND COALESCE(g.agent_id,g.project_id,g.routine_id)=? AND g.operation=?`,
		publisher, workspace, right.Kind, right.ID, right.Operation).
		Scan(&binding.PublisherMembershipID, &binding.GrantID, &binding.IssuerID, &binding.IssuerMembershipID)
	if errors.Is(err, sql.ErrNoRows) {
		return PublicationGrantBinding{}, ErrDenied
	}
	if err != nil {
		return PublicationGrantBinding{}, err
	}
	return binding, nil
}

// ResolvePublicationGrantIn checks original identities, never replacement
// shares. Same-identity issuer recovery can authorize a NEW invocation. This
// read-only check cannot revive an old attempt: its separate admission fence
// must still be resolved at execution boundaries.
func (s Store) ResolvePublicationGrantIn(ctx context.Context, tx *sql.Tx, binding PublicationGrantBinding) error {
	if binding.PublisherMembershipID == "" || binding.GrantID == "" || binding.IssuerID == "" || binding.IssuerMembershipID == "" {
		return ErrDenied
	}
	current, err := s.CapturePublicationGrantIn(ctx, tx, binding.PublisherID, binding.WorkspaceID, binding.Right)
	if err != nil {
		return err
	}
	if current != binding {
		return ErrDenied
	}
	return nil
}
