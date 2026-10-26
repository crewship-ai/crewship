package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/crewship-ai/crewship/internal/hooks"
	"github.com/crewship-ai/crewship/internal/journal"
)

// mutateHook serializes the observed kind, mutation and host-authority audit.
// Existing scope/instance checks remain in the handlers and store. A chain
// failure rolls back both approval and its legacy audit projection.
func (h *HooksHandler) mutateHook(r *http.Request, workspaceID, id, action string, mutate func(hooks.StoreDB) (string, error)) (string, bool, error) {
	ctx := r.Context()
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()
	var before *hooks.Hook
	if id != "" {
		before, err = hooks.Get(ctx, tx, workspaceID, id)
		if err != nil {
			return "", false, err
		}
	}
	id, err = mutate(tx)
	if err != nil {
		return "", false, err
	}
	after, err := hooks.Get(ctx, tx, workspaceID, id)
	if err != nil {
		return "", false, err
	}
	host := (before != nil && before.HandlerKind == hooks.HandlerKindShell) || (after != nil && after.HandlerKind == hooks.HandlerKindShell)
	notify := func() {}
	if host {
		metadata := map[string]any{}
		snapshot := after
		if snapshot == nil {
			snapshot = before
		}
		if snapshot != nil {
			metadata["event"] = string(snapshot.Event)
			metadata["handler_kind"] = string(snapshot.HandlerKind)
			metadata["crew_id"] = snapshot.CrewID
			metadata["blocking"] = snapshot.Blocking
			metadata["enabled"] = snapshot.Enabled
		}
		if before != nil {
			metadata["before_authorization"] = before.HostAuthorization
			metadata["before_enabled"] = before.Enabled
		}
		if after != nil {
			metadata["after_authorization"] = after.HostAuthorization
			metadata["after_enabled"] = after.Enabled
			metadata["handler_kind"] = string(after.HandlerKind)
		}
		encoded, err := json.Marshal(metadata)
		if err != nil {
			return "", false, err
		}
		actor := actorIDFromContext(r)
		var actorArg any
		actorType := journal.ActorSystem
		if actor != "" {
			actorArg = actor
			actorType = journal.ActorUser
		}
		inserted, err := tx.ExecContext(ctx, `INSERT INTO audit_logs(id,workspace_id,user_id,action,entity_type,entity_id,metadata,created_at)
   VALUES(lower(hex(randomblob(16))),?,?,?,'hook',?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, workspaceID, actorArg, action, id, string(encoded))
		if err != nil {
			return "", false, err
		}
		if n, err := inserted.RowsAffected(); err != nil || n != 1 {
			return "", false, errors.Join(err, errors.New("host hook audit projection not inserted"))
		}
		kind := journal.EntryAuditEntityUpdated
		if action == "hook.create" {
			kind = journal.EntryAuditEntityCreated
		}
		if action == "hook.delete" {
			kind = journal.EntryAuditEntityDeleted
		}
		notify, err = appendAuthorizationAudit(ctx, tx, h.journal, journal.Entry{WorkspaceID: workspaceID, Type: kind, Severity: journal.SeverityNotice, ActorType: actorType, ActorID: actor, Summary: fmt.Sprintf("Host lifecycle hook %s", action), Payload: map[string]any{"action": action, "entity_type": "hook", "entity_id": id, "metadata": metadata}, Refs: map[string]any{"entity_type": "hook", "entity_id": id}})
		if err != nil {
			return "", false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	hooks.InvalidateCache(workspaceID)
	notify()
	return id, host, nil
}
