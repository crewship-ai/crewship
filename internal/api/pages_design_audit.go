package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

type designAdminAuditKey struct{}
type designAdminAuditScope struct{ id, created, operation string }

func newDesignAdminAuditRequest(r *http.Request, operation string) *http.Request {
	scope := designAdminAuditScope{id: generateCUID(), created: tsformat.Format(time.Now()), operation: operation}
	return r.WithContext(context.WithValue(r.Context(), designAdminAuditKey{}, scope))
}
func ensureDesignAdminAuditRequest(r *http.Request, write bool) *http.Request {
	if _, ok := r.Context().Value(designAdminAuditKey{}).(designAdminAuditScope); ok {
		return r
	}
	operation := "read"
	if write {
		operation = "write"
	}
	return newDesignAdminAuditRequest(r, operation)
}

// A request/connection admission has one audit ID. Permission probes and
// websocket heartbeats verify that record instead of producing new events.
// An incoming websocket edit gets a fresh ID and commits its audit with data.
func recordDesignAdminAccess(ctx context.Context, tx *sql.Tx, r *http.Request, k artifacts.VersionKey, actor, ownerUser, ownerCrew string) error {
	scope, ok := r.Context().Value(designAdminAuditKey{}).(designAdminAuditScope)
	if !ok {
		return errors.New("Design administrator audit scope unavailable")
	}
	human := UserFromContext(ctx)
	if human == nil {
		return errDesignAccess
	}
	raw, err := json.Marshal(map[string]string{"operation": scope.operation, "owner_user_id": ownerUser, "owner_crew_id": ownerCrew, "actor": actor})
	if err != nil {
		return err
	}
	const action = "design.workspace_admin_accessed"
	args := []any{scope.id, k.WorkspaceID, human.ID, action, k.PageID, string(raw), scope.created}
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_logs(id,workspace_id,user_id,action,entity_type,entity_id,metadata,created_at) VALUES(?,?,?,?,'page',?,?,?) ON CONFLICT(id) DO NOTHING`, args...); err != nil {
		return err
	}
	var acknowledged bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM audit_logs WHERE id COLLATE BINARY=? AND workspace_id COLLATE BINARY=? AND user_id COLLATE BINARY=? AND action COLLATE BINARY=? AND entity_type='page' AND entity_id COLLATE BINARY=? AND metadata COLLATE BINARY=? AND created_at COLLATE BINARY=? AND ip_address IS NULL AND user_agent IS NULL)`, args...).Scan(&acknowledged)
	if err != nil {
		return err
	}
	if !acknowledged {
		return errors.New("Design administrator audit was not durably acknowledged")
	}
	return nil
}

// Read-scoped manager credentials retain full Design read authority without
// becoming write credentials. Normal readers/commenters remain shared-only.
func (h *PageHandler) designManagerRead(ctx context.Context, tx *sql.Tx, k artifacts.VersionKey) (bool, error) {
	human := UserFromContext(ctx)
	if human == nil {
		return false, sql.ErrNoRows
	}
	ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
	role, err := h.liveContentHumanRole(ctx, k.WorkspaceID, human.ID)
	return canRole(role, "manage"), err
}
