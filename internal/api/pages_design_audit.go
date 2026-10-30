package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

type designAdminAuditKey struct{}
type designAdminAuditScope struct {
	id, created, operation string
	mu                     sync.Mutex
	chain                  []any
	notify                 func()
}

func newDesignAdminAuditRequest(r *http.Request, operation string) *http.Request {
	scope := &designAdminAuditScope{id: generateCUID(), created: tsformat.Format(time.Now()), operation: operation}
	return r.WithContext(context.WithValue(r.Context(), designAdminAuditKey{}, scope))
}
func ensureDesignAdminAuditRequest(r *http.Request, write bool) *http.Request {
	if _, ok := r.Context().Value(designAdminAuditKey{}).(*designAdminAuditScope); ok {
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
func (h *PageHandler) recordDesignAdminAccess(ctx context.Context, tx *sql.Tx, r *http.Request, k artifacts.VersionKey, actor, ownerUser, ownerCrew string) error {
	scope, ok := r.Context().Value(designAdminAuditKey{}).(*designAdminAuditScope)
	if !ok {
		return errors.New("Design administrator audit scope unavailable")
	}
	human := UserFromContext(ctx)
	if human == nil {
		return errDesignAccess
	}
	scope.mu.Lock()
	defer scope.mu.Unlock()
	metadata := map[string]string{"operation": scope.operation, "owner_user_id": ownerUser, "owner_crew_id": ownerCrew, "actor": actor}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	const action = "design.workspace_admin_accessed"
	args := []any{scope.id, k.WorkspaceID, human.ID, action, k.PageID, string(raw), scope.created}
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_logs(id,workspace_id,user_id,action,entity_type,entity_id,metadata,created_at) VALUES(?,?,?,?,'page',?,?,?) ON CONFLICT(id) DO NOTHING`, args...); err != nil {
		return err
	}
	if scope.chain == nil {
		stamp, err := time.Parse(time.RFC3339Nano, scope.created)
		if err != nil {
			return err
		}
		notify, err := appendAuthorizationAudit(ctx, tx, h.journal, journal.Entry{
			ID: scope.id, TS: stamp, WorkspaceID: k.WorkspaceID,
			Type: classifyAuditAction(action), Severity: journal.SeverityNotice,
			ActorType: journal.ActorUser, ActorID: human.ID,
			Summary: "Workspace administrator accessed a Design",
			Payload: map[string]any{"action": action, "entity_type": "page", "entity_id": k.PageID, "metadata": metadata},
			Refs:    map[string]any{"entity_type": "page", "entity_id": k.PageID},
		})
		if err != nil {
			return err
		}
		// AppendTx acknowledges the exact keyed row. Keep its native frame so
		// later permission probes cannot bless a changed or missing receipt.
		scope.chain, err = pageGrantEventFrame(ctx, tx, scope.id)
		if err != nil {
			return err
		}
		scope.notify = notify
	}
	frame, err := pageGrantEventFrame(ctx, tx, scope.id)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(frame, scope.chain) {
		return errors.New("Design administrator chained audit changed")
	}
	// Check the compliance row after the chained append too: a journal
	// trigger may otherwise change a previously acknowledged legacy row.
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

// Call only after the transaction owning this request's receipt committed.
// Repeated websocket permission probes publish the admission receipt once.
func notifyDesignAdminAudit(r *http.Request) {
	scope, ok := r.Context().Value(designAdminAuditKey{}).(*designAdminAuditScope)
	if !ok {
		return
	}
	scope.mu.Lock()
	notify := scope.notify
	scope.notify = nil
	scope.mu.Unlock()
	if notify != nil {
		notify()
	}
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
