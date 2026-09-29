// Package access is the server authority for resource-scoped human work.
// Callers supply authenticated identities, never identity claims from task JSON.
// Existing role, token-scope and Page/routine gates remain additional constraints.
package access

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

var ErrDenied = errors.New("resource authority denied")
var ErrConflict = errors.New("resource authority revision changed")

type Store struct{ DB *sql.DB }
type Right struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Operation string `json:"operation"`
}
type Membership struct {
	ID       string `json:"membership_id"`
	Mode     string `json:"mode"`
	Revision int64  `json:"revision"`
}
type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func randomID() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func validRight(r Right) bool {
	if r.ID == "" {
		return false
	}
	switch r.Kind {
	case "agent":
		switch r.Operation {
		case "discover", "chat", "run", "delegate":
			return true
		}
	case "project":
		switch r.Operation {
		case "list", "read", "write", "delete":
			return true
		}
	}
	return false
}

func member(ctx context.Context, q queryer, user, workspace string) (Membership, error) {
	var m Membership
	err := q.QueryRowContext(ctx, `SELECT wm.id,wm.access_mode,wm.access_revision
        FROM workspace_members wm JOIN workspaces w ON w.id=wm.workspace_id
        WHERE wm.user_id=? AND wm.workspace_id=? AND w.deleted_at IS NULL`, user, workspace).
		Scan(&m.ID, &m.Mode, &m.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrDenied
	}
	return m, err
}

func (s Store) Membership(ctx context.Context, user, workspace string) (Membership, error) {
	if s.DB == nil {
		return Membership{}, ErrDenied
	}
	return member(ctx, s.DB, user, workspace)
}

// HasRestrictedMembership is used at unclassified global entrypoints. A
// membership's restriction cannot be bypassed by omitting workspace_id.
func (s Store) HasRestrictedMembership(ctx context.Context, user string) (bool, error) {
	if s.DB == nil || user == "" {
		return true, ErrDenied
	}
	var yes bool
	err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workspace_members WHERE user_id=? AND access_mode='restricted')`, user).Scan(&yes)
	return yes, err
}

func resourceExists(ctx context.Context, q queryer, workspace string, r Right) error {
	if !validRight(r) {
		return ErrDenied
	}
	query := `SELECT 1 FROM projects WHERE id=? AND workspace_id=?`
	if r.Kind == "agent" {
		query = `SELECT 1 FROM agents WHERE id=? AND workspace_id=? AND deleted_at IS NULL`
	}
	var one int
	err := q.QueryRowContext(ctx, query, r.ID, workspace).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDenied
	}
	return err
}

func check(ctx context.Context, q queryer, user, workspace string, r Right) (Membership, error) {
	m, err := member(ctx, q, user, workspace)
	if err != nil {
		return m, err
	}
	if err = resourceExists(ctx, q, workspace, r); err != nil {
		return m, err
	}
	if m.Mode == "trusted" {
		return m, nil
	}
	if m.Mode != "restricted" {
		return m, ErrDenied
	}
	var one int
	err = q.QueryRowContext(ctx, `SELECT 1 FROM access_grants WHERE member_id=? AND resource_kind=? AND COALESCE(agent_id,project_id)=? AND operation=?`, m.ID, r.Kind, r.ID, r.Operation).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrDenied
	}
	return m, err
}

// Check is an additional resource ceiling, never a substitute for RBAC.
func (s Store) Check(ctx context.Context, user, workspace string, r Right) error {
	if s.DB == nil {
		return ErrDenied
	}
	_, err := check(ctx, s.DB, user, workspace, r)
	return err
}

func operator(ctx context.Context, q queryer, user, workspace string) error {
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM workspace_members wm JOIN workspaces w ON w.id=wm.workspace_id
        WHERE wm.user_id=? AND wm.workspace_id=? AND wm.access_mode='trusted'
        AND wm.role IN ('OWNER','ADMIN') AND w.deleted_at IS NULL`, user, workspace).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDenied
	}
	return err
}

// Replace atomically replaces an explicit member policy. Optimistic revisions
// prevent a stale Settings save from restoring a concurrently revoked grant.
// Restricting administrators is forbidden: access administration has one owner.
func (s Store) Replace(ctx context.Context, actor, user, workspace, mode string, expected Membership, rights []Right) (Membership, error) {
	if s.DB == nil {
		return Membership{}, ErrDenied
	}
	if mode != "trusted" && mode != "restricted" || len(rights) > 256 || (mode == "trusted" && len(rights) > 0) {
		return Membership{}, ErrDenied
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Membership{}, err
	}
	defer tx.Rollback()
	if err = operator(ctx, tx, actor, workspace); err != nil {
		return Membership{}, err
	}
	m, err := member(ctx, tx, user, workspace)
	if err != nil {
		return m, err
	}
	if expected.ID != m.ID || expected.Revision != m.Revision {
		return m, ErrConflict
	}
	var role string
	if err = tx.QueryRowContext(ctx, `SELECT role FROM workspace_members WHERE id=?`, m.ID).Scan(&role); err != nil {
		return m, err
	}
	if mode == "restricted" && (role == "OWNER" || role == "ADMIN") {
		return m, ErrDenied
	}
	seen := map[Right]bool{}
	for _, r := range rights {
		if seen[r] {
			return m, ErrDenied
		}
		seen[r] = true
		if err = resourceExists(ctx, tx, workspace, r); err != nil {
			return m, err
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM access_grants WHERE member_id=?`, m.ID); err != nil {
		return m, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE workspace_members SET access_mode=?,access_revision=access_revision+1 WHERE id=?`, mode, m.ID); err != nil {
		return m, err
	}
	for _, r := range rights {
		var agentID, projectID any
		if r.Kind == "agent" {
			agentID = r.ID
		} else {
			projectID = r.ID
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO access_grants(id,member_id,resource_kind,agent_id,project_id,operation,created_by,created_at) VALUES(?,?,?,?,?,?,?,?)`, randomID(), m.ID, r.Kind, agentID, projectID, r.Operation, actor, tsformat.Format(time.Now())); err != nil {
			return m, err
		}
	}
	m, err = member(ctx, tx, user, workspace)
	if err != nil {
		return m, err
	}
	return m, tx.Commit()
}
