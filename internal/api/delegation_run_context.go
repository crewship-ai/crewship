package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/crewship-ai/crewship/internal/runauthority"
)

var errDelegationExecutionChatUnavailable = fmt.Errorf("delegation execution chat unavailable: %w", runauthority.ErrUnavailable)

// A delegated run's execution chat can differ from its root human chat. Resolve
// first verifies identity/authority; only the durable host start names the chat
// where this execution's assignment side effects belong.
func delegationExecutionChat(ctx context.Context, q issueOperationQueryer, runID, agentID, workspaceID string) (string, error) {
	if _, err := runauthority.Resolve(ctx, q, runID, agentID, workspaceID); err != nil {
		return "", err
	}
	var raw string
	if err := q.QueryRowContext(ctx, `SELECT payload FROM journal_entries WHERE trace_id=? AND workspace_id=? AND agent_id=? AND entry_type='run.started'`, runID, workspaceID, agentID).Scan(&raw); err != nil {
		return "", err
	}
	var payload struct {
		ChatID string `json:"chat_id"`
	}
	if json.Unmarshal([]byte(raw), &payload) != nil || payload.ChatID == "" {
		return "", errDelegationExecutionChatUnavailable
	}
	var exists bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM chats WHERE id=? AND workspace_id=?)`, payload.ChatID, workspaceID).Scan(&exists); err != nil {
		return "", err
	}
	if !exists {
		return "", runauthority.ErrIdentityMismatch
	}
	return payload.ChatID, nil
}

func assertLiveDelegationMembers(w http.ResponseWriter, r *http.Request, tx *sql.Tx, logger *slog.Logger, workspace, actor, actorCrew, target, targetCrew string) bool {
	var live bool
	err := tx.QueryRowContext(r.Context(), `
		SELECT (?='' OR EXISTS(SELECT 1 FROM agents a JOIN crews c ON c.id=a.crew_id
			WHERE a.id=? AND a.workspace_id=? AND a.crew_id=? AND a.deleted_at IS NULL
			AND COALESCE(a.status,'')<>'PENDING_REVIEW' AND c.workspace_id=a.workspace_id AND c.deleted_at IS NULL))
		AND EXISTS(SELECT 1 FROM agents a JOIN crews c ON c.id=a.crew_id
			WHERE a.id=? AND a.workspace_id=? AND a.crew_id=? AND a.deleted_at IS NULL
			AND c.workspace_id=a.workspace_id AND c.deleted_at IS NULL)`,
		actor, actor, workspace, actorCrew, target, workspace, targetCrew).Scan(&live)
	if err != nil {
		replyInternalError(w, logger, "recheck delegation membership", err)
		return false
	}
	if !live {
		replyError(w, http.StatusForbidden, "delegation membership changed before admission")
		return false
	}
	return true
}
