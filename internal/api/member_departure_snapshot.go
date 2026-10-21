package api

import (
	"context"
	"database/sql"
	"errors"

	"github.com/crewship-ai/crewship/internal/access"
)

// Capture access before cascading deletes remove the evidence. This snapshot is
// canonical audit data; public feeds receive only the existing event fields.
func memberDepartureSnapshot(ctx context.Context, tx *sql.Tx, workspace, user string) (map[string]any, error) {
	var id, role, mode string
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT id,role,access_mode,access_revision FROM workspace_members WHERE workspace_id=? AND user_id=?`, workspace, user).Scan(&id, &role, &mode, &revision); err != nil {
		return nil, err
	}
	rights := []access.Right{}
	rows, err := tx.QueryContext(ctx, `SELECT resource_kind,COALESCE(agent_id,project_id,routine_id),operation FROM access_grants WHERE member_id=? ORDER BY resource_kind,COALESCE(agent_id,project_id,routine_id),operation`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var right access.Right
		if err := rows.Scan(&right.Kind, &right.ID, &right.Operation); err != nil {
			_ = rows.Close()
			return nil, err
		}
		rights = append(rights, right)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	crews := []map[string]string{}
	rows, err = tx.QueryContext(ctx, `SELECT cm.id,cm.crew_id,cm.role FROM crew_members cm JOIN crews c ON c.id=cm.crew_id WHERE c.workspace_id=? AND cm.user_id=? ORDER BY cm.crew_id,cm.id`, workspace, user)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, crew string
		var role sql.NullString
		if err := rows.Scan(&id, &crew, &role); err != nil {
			_ = rows.Close()
			return nil, err
		}
		value := map[string]string{"id": id, "crew_id": crew}
		if role.Valid {
			value["role"] = role.String
		}
		crews = append(crews, value)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	return map[string]any{"member_id": id, "role": role, "access_mode": mode, "access_revision": revision, "rights": rights, "crew_memberships": crews}, nil
}
