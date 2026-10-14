package access

import (
	"context"
	"database/sql"
)

// Policy is a complete, versioned replacement document. An empty Rights slice
// in restricted mode explicitly denies all resource operations.
type Policy struct {
	Membership
	Rights []Right `json:"rights"`
	// Read-only provenance, recomputed by the server; never trusted on writes.
	GrantDetails []GrantDetail `json:"grant_details,omitempty"`
	// Only these rights are deliberately reissued by the authenticated actor.
	Reissue []Right `json:"reissue,omitempty"`
}

type GrantDetail struct {
	Right
	IssuerID   string `json:"issuer_id"`
	IssuerName string `json:"issuer_name,omitempty"`
	CreatedAt  string `json:"created_at"`
	Live       bool   `json:"live"`
}

// Policy reads membership and grants in one snapshot, only for a current trusted
// administrator. A stale document cannot overwrite a later revocation.
func (s Store) Policy(ctx context.Context, actor, user, workspace string) (Policy, error) {
	if s.DB == nil {
		return Policy{}, ErrDenied
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Policy{}, err
	}
	defer tx.Rollback()
	p, err := s.PolicyIn(ctx, tx, actor, user, workspace)
	if err != nil {
		return Policy{}, err
	}
	return p, tx.Commit()
}

// PolicyIn reads the policy in the caller's transaction, for atomic audit and
// response snapshots alongside a replacement.
func (s Store) PolicyIn(ctx context.Context, tx *sql.Tx, actor, user, workspace string) (Policy, error) {
	if err := operator(ctx, tx, actor, workspace); err != nil {
		return Policy{}, err
	}
	m, err := member(ctx, tx, user, workspace)
	if err != nil {
		return Policy{}, err
	}
	p := Policy{Membership: m, Rights: []Right{}}
	rows, err := tx.QueryContext(ctx, `SELECT g.resource_kind,COALESCE(g.agent_id,g.project_id,g.routine_id),g.operation,g.created_by,COALESCE(issuer.full_name,''),g.created_at,EXISTS(SELECT 1 FROM live_access_grants live WHERE live.id=g.id) FROM access_grants g JOIN users issuer ON issuer.id=g.created_by WHERE g.member_id=? ORDER BY g.resource_kind,COALESCE(g.agent_id,g.project_id,g.routine_id),g.operation`, m.ID)
	if err != nil {
		return Policy{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var detail GrantDetail
		if err = rows.Scan(&detail.Kind, &detail.ID, &detail.Operation, &detail.IssuerID, &detail.IssuerName, &detail.CreatedAt, &detail.Live); err != nil {
			return Policy{}, err
		}
		p.Rights = append(p.Rights, detail.Right)
		p.GrantDetails = append(p.GrantDetails, detail)
	}
	if err = rows.Err(); err != nil {
		return Policy{}, err
	}
	if err = rows.Close(); err != nil {
		return Policy{}, err
	}
	return p, nil
}
