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
	return s.policy(ctx, actor, user, workspace, false)
}

// OwnPolicy exposes only the authenticated caller's saved grants. It does not
// require workspace administration and never accepts another target identity.
func (s Store) OwnPolicy(ctx context.Context, user, workspace string) (Policy, error) {
	return s.policy(ctx, user, user, workspace, true)
}

func (s Store) policy(ctx context.Context, actor, user, workspace string, own bool) (Policy, error) {
	if s.DB == nil {
		return Policy{}, ErrDenied
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Policy{}, err
	}
	defer tx.Rollback()
	if own {
		var active bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=? AND suspended_at IS NULL)`, user).Scan(&active); err != nil {
			return Policy{}, err
		}
		if !active {
			return Policy{}, ErrDenied
		}
	} else {
		if err = operator(ctx, tx, actor, workspace); err != nil {
			return Policy{}, err
		}
	}
	m, err := member(ctx, tx, user, workspace)
	if err != nil {
		return Policy{}, err
	}
	p := Policy{Membership: m, Rights: []Right{}}
	rows, err := tx.QueryContext(ctx, `SELECT resource_kind,COALESCE(agent_id,project_id,routine_id),operation FROM access_grants WHERE member_id=? ORDER BY resource_kind,COALESCE(agent_id,project_id,routine_id),operation`, m.ID)
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
