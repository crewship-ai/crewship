package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// Only the workspace/crew settings writers use this boundary. Authority is
// re-read on the writer transaction, including the account-wide Guest ceiling.
func settingsActorIn(r *http.Request, tx *sql.Tx) error {
	user := UserFromContext(r.Context())
	if user == nil {
		return sql.ErrNoRows
	}
	_, err := invitationIssuerRole(r.Context(), tx, user.ID, WorkspaceIDFromContext(r.Context()))
	return err
}

// The compliance view still consumes audit_logs. Both records are required,
// use the configured transaction emitter, and belong to the business commit.
func appendSettingsAudit(r *http.Request, tx *sql.Tx, emitter journal.Emitter, action, entity, id string, metadata map[string]any) (func(), error) {
	actor := UserFromContext(r.Context())
	if actor == nil {
		return nil, sql.ErrNoRows
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	if metadata == nil {
		raw = []byte("{}")
	}
	auditID, stamp := generateCUID(), time.Now().UTC().Format(time.RFC3339)
	result, err := tx.ExecContext(r.Context(), `INSERT INTO audit_logs(id,workspace_id,user_id,action,entity_type,entity_id,metadata,created_at) VALUES(?,?,?,?,?,?,?,?)`, auditID, WorkspaceIDFromContext(r.Context()), actor.ID, action, entity, id, string(raw), stamp)
	if err != nil {
		return nil, err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return nil, errors.Join(err, errors.New("settings compliance audit not inserted"))
	}
	payload := map[string]any{"action": action, "entity_type": entity, "entity_id": id}
	if metadata != nil {
		payload["metadata"] = metadata
	}
	notify, err := appendAuthorizationAudit(r.Context(), tx, emitter, journal.Entry{
		WorkspaceID: WorkspaceIDFromContext(r.Context()), Type: classifyAuditAction(action), Severity: journal.SeverityNotice,
		ActorType: journal.ActorUser, ActorID: actor.ID, Summary: fmt.Sprintf("%s %s %s", action, entity, shortenID(id, 12)),
		Payload: payload,
		Refs:    map[string]any{"entity_type": entity, "entity_id": id},
	})
	if err != nil {
		return nil, err
	}
	var intact bool
	err = tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM audit_logs WHERE id=? AND workspace_id IS ? AND user_id IS ? AND action IS ? AND entity_type IS ? AND entity_id IS ? AND metadata IS ? AND created_at IS ? AND ip_address IS NULL AND user_agent IS NULL)`, auditID, WorkspaceIDFromContext(r.Context()), actor.ID, action, entity, id, string(raw), stamp).Scan(&intact)
	if err != nil || !intact {
		return nil, errors.Join(err, errors.New("settings compliance audit changed"))
	}
	return notify, nil
}

// Check the actual builder's requested values, including SQL NULL, rather than
// treating RowsAffected as proof that a trigger retained the requested patch.
func settingsUpdatePersisted(ctx context.Context, tx *sql.Tx, table, id string, update *updateBuilder) error {
	if table != "crews" && table != "workspaces" {
		return errors.New("unsupported settings target")
	}
	conditions, args, index := []string{"id=?"}, []any{id}, 0
	for _, set := range update.sets {
		parts := strings.SplitN(set, " = ", 2)
		if len(parts) != 2 {
			return errors.New("unsupported settings assignment")
		}
		if parts[1] == "NULL" {
			conditions = append(conditions, parts[0]+" IS NULL")
		} else {
			conditions = append(conditions, parts[0]+" IS ?")
			args = append(args, update.args[index])
			index++
		}
	}
	var intact bool
	err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM "+table+" WHERE "+strings.Join(conditions, " AND ")+")", args...).Scan(&intact)
	if err != nil || !intact {
		return errors.Join(err, errors.New("settings update did not persist"))
	}
	return nil
}

// Keep all native columns in memory solely for final comparison. No config
// values enter the audit, response errors or logs through this snapshot.
func settingsNativeRow(ctx context.Context, tx *sql.Tx, table, id string) ([]any, error) {
	if table != "crews" && table != "workspaces" {
		return nil, errors.New("unsupported settings target")
	}
	rows, err := tx.QueryContext(ctx, "SELECT * FROM "+table+" WHERE id=?", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	if !rows.Next() {
		return nil, errors.Join(rows.Err(), sql.ErrNoRows)
	}
	values, pointers := make([]any, len(columns)), make([]any, len(columns))
	for i := range values {
		pointers[i] = &values[i]
	}
	if err := rows.Scan(pointers...); err != nil {
		return nil, err
	}
	if rows.Next() {
		return nil, errors.New("duplicate settings target")
	}
	return values, rows.Err()
}
func acknowledgeSettingsRow(ctx context.Context, tx *sql.Tx, table, id string, expected []any) error {
	actual, err := settingsNativeRow(ctx, tx, table, id)
	if err != nil || !reflect.DeepEqual(actual, expected) {
		return errors.Join(err, errors.New("settings target changed during audit"))
	}
	return nil
}

func checkWorkspaceSettingsActor(w http.ResponseWriter, r *http.Request, tx *sql.Tx, logger *slog.Logger, privileged *bool) bool {
	err := settingsActorIn(r, tx)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusForbidden, "Forbidden")
		return false
	}
	if err != nil {
		replyInternalError(w, logger, "read workspace settings authority", err)
		return false
	}
	if privileged != nil && *privileged {
		var email string
		var role sql.NullString
		err = tx.QueryRowContext(r.Context(), `SELECT email,instance_role FROM users WHERE id=? AND suspended_at IS NULL`, UserFromContext(r.Context()).ID).Scan(&email, &role)
		if err != nil {
			replyInternalError(w, logger, "read instance settings authority", err)
			return false
		}
		if instanceAdminSourceFor(email, role.String, false) == "" || !canScope(r.Context(), scopeInstanceAdmin) {
			replyError(w, http.StatusForbidden, "Only an instance administrator can enable privileged runtime access")
			return false
		}
	}
	return true
}
