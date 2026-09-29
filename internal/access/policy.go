package access

import "context"

// Policy is a complete, versioned replacement document. An empty Rights slice
// in restricted mode explicitly denies all resource operations.
type Policy struct {
	Membership
	Rights []Right `json:"rights"`
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
	if err = operator(ctx, tx, actor, workspace); err != nil {
		return Policy{}, err
	}
	m, err := member(ctx, tx, user, workspace)
	if err != nil {
		return Policy{}, err
	}
	p := Policy{Membership: m, Rights: []Right{}}
	rows, err := tx.QueryContext(ctx, `SELECT resource_kind,COALESCE(agent_id,project_id),operation FROM access_grants WHERE member_id=? ORDER BY resource_kind,COALESCE(agent_id,project_id),operation`, m.ID)
	if err != nil {
		return Policy{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var right Right
		if err = rows.Scan(&right.Kind, &right.ID, &right.Operation); err != nil {
			return Policy{}, err
		}
		p.Rights = append(p.Rights, right)
	}
	if err = rows.Err(); err != nil {
		return Policy{}, err
	}
	if err = rows.Close(); err != nil {
		return Policy{}, err
	}
	return p, tx.Commit()
}
