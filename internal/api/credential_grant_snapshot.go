package api

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
)

// loadEffectiveCredentialGrants derives permissions from the delivery query,
// including explicit-grant precedence and binding slot resolution. It never
// decrypts a value. Callers use one read transaction for the whole snapshot.
func loadEffectiveCredentialGrants(ctx context.Context, db sqlQuerier, agentID string) (map[string]map[string]string, error) {
	rows, err := db.QueryContext(ctx, crewMembersSQL, agentID)
	if err != nil {
		return nil, err
	}
	var peers []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		peers = append(peers, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(peers) == 0 {
		return nil, nil
	} // Legacy crew-less runtime has no crew snapshot endpoint.
	grants := map[string]map[string]string{}
	for _, peer := range peers {
		deliveries, err := db.QueryContext(ctx, `SELECT credential_id, lease_expires_at FROM (`+agentDeliveredCredentialsSQL+`)`, peer, peer, leaseComparisonNow())
		if err != nil {
			return nil, fmt.Errorf("resolve effective grants: %w", err)
		}
		for deliveries.Next() {
			var id, deadline string
			if err := deliveries.Scan(&id, &deadline); err != nil {
				deliveries.Close()
				return nil, err
			}
			if grants[id] == nil {
				grants[id] = map[string]string{}
			}
			// Multiple slots for the same credential must not depend on SQL row order.
			old, exists := grants[id][peer]
			if !exists || deadline == "" || (old != "" && deadline > old) {
				grants[id][peer] = deadline
			}
		}
		err = deliveries.Err()
		deliveries.Close()
		if err != nil {
			return nil, err
		}
	}
	return grants, nil
}

// CredentialGrants is metadata only. A crew-bound caller cannot ask for a
// sibling's grants; an unbound host caller still has to supply its workspace.
func (h *InternalHandler) CredentialGrants(w http.ResponseWriter, r *http.Request) {
	crewID := r.URL.Query().Get("crew_id")
	if bound := InternalTokenCrewFromContext(r.Context()); bound != "" {
		if crewID != "" && crewID != bound {
			replyError(w, 403, "crew scope mismatch")
			return
		}
		crewID = bound
	}
	wsID := r.URL.Query().Get("workspace_id")
	if bound := InternalTokenWorkspaceFromContext(r.Context()); bound != "" {
		if wsID != "" && wsID != bound {
			replyError(w, 403, "workspace scope mismatch")
			return
		}
		wsID = bound
	}
	if crewID == "" || wsID == "" {
		replyError(w, 400, "crew_id and workspace_id are required")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		internalError(w, r, h.logger, "credential grant snapshot", err)
		return
	}
	defer tx.Rollback()
	var exists bool
	if err = tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM crews WHERE id=? AND workspace_id=? AND deleted_at IS NULL)`, crewID, wsID).Scan(&exists); err != nil {
		internalError(w, r, h.logger, "credential grant crew", err)
		return
	}
	if !exists {
		replyError(w, 403, "crew scope mismatch")
		return
	}
	var agentID string
	err = tx.QueryRowContext(r.Context(), `SELECT id FROM agents WHERE crew_id=? AND workspace_id=? AND deleted_at IS NULL ORDER BY id LIMIT 1`, crewID, wsID).Scan(&agentID)
	grants := map[string]map[string]string{}
	if err != nil && err != sql.ErrNoRows {
		internalError(w, r, h.logger, "credential grant roster", err)
		return
	}
	if agentID != "" {
		grants, err = loadEffectiveCredentialGrants(r.Context(), tx, agentID)
		if err != nil {
			internalError(w, r, h.logger, "credential grants", err)
			return
		}
	}
	if err = tx.Commit(); err != nil {
		internalError(w, r, h.logger, "credential grant commit", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": 1, "grants": grants})
}
