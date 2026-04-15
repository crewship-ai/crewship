package api

import (
	"context"
	"database/sql"
)

// Batch / junction-table loaders for CredentialHandler. Lifted out of
// credentials.go to keep the handler file focused on HTTP-facing
// methods. All functions are unexported methods on CredentialHandler
// so behaviour and package-private access stay identical.

// loadAgentNamesBatch fetches agent names for multiple credentials in one query.
func (h *CredentialHandler) loadAgentNamesBatch(ctx context.Context, credentialIDs []string) map[string][]string {
	result := make(map[string][]string, len(credentialIDs))
	if len(credentialIDs) == 0 {
		return result
	}
	ph := sqlPlaceholders(len(credentialIDs))
	args := make([]any, len(credentialIDs))
	for i, id := range credentialIDs {
		args[i] = id
	}
	rows, err := h.db.QueryContext(ctx,
		"SELECT ac.credential_id, a.name FROM agent_credentials ac JOIN agents a ON a.id = ac.agent_id AND a.deleted_at IS NULL WHERE ac.credential_id IN ("+ph+") ORDER BY a.name",
		args...)
	if err != nil {
		return result
	}
	defer rows.Close()
	for rows.Next() {
		var credID, agentName string
		if rows.Scan(&credID, &agentName) == nil {
			result[credID] = append(result[credID], agentName)
		}
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("iterate agent names batch", "error", err)
	}
	return result
}

// loadMCPUsedBatch returns the set of credential IDs that are referenced by MCP bindings.
func (h *CredentialHandler) loadMCPUsedBatch(ctx context.Context, credentialIDs []string) map[string]bool {
	result := make(map[string]bool, len(credentialIDs))
	if len(credentialIDs) == 0 {
		return result
	}
	ph := sqlPlaceholders(len(credentialIDs))
	args := make([]any, len(credentialIDs))
	for i, id := range credentialIDs {
		args[i] = id
	}
	rows, err := h.db.QueryContext(ctx,
		"SELECT DISTINCT credential_id FROM agent_mcp_bindings WHERE credential_id IN ("+ph+") AND credential_id IS NOT NULL",
		args...)
	if err != nil {
		return result
	}
	defer rows.Close()
	for rows.Next() {
		var credID string
		if rows.Scan(&credID) == nil {
			result[credID] = true
		}
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("iterate MCP used batch", "error", err)
	}
	return result
}

// loadCrewIDs fetches crew_ids from the junction table for a single credential.
func (h *CredentialHandler) loadCrewIDs(ctx context.Context, credentialID string) []string {
	rows, err := h.db.QueryContext(ctx,
		"SELECT crew_id FROM credential_crews WHERE credential_id = ? ORDER BY created_at", credentialID)
	if err != nil {
		return []string{}
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("iterate crew IDs", "error", err)
	}
	if ids == nil {
		ids = []string{}
	}
	return ids
}

// loadCrewIDsBatch fetches crew_ids for multiple credentials in one query.
func (h *CredentialHandler) loadCrewIDsBatch(ctx context.Context, credentialIDs []string) map[string][]string {
	result := make(map[string][]string, len(credentialIDs))
	if len(credentialIDs) == 0 {
		return result
	}
	ph := sqlPlaceholders(len(credentialIDs))
	args := make([]any, len(credentialIDs))
	for i, id := range credentialIDs {
		args[i] = id
	}
	rows, err := h.db.QueryContext(ctx,
		"SELECT credential_id, crew_id FROM credential_crews WHERE credential_id IN ("+ph+") ORDER BY created_at",
		args...)
	if err != nil {
		return result
	}
	defer rows.Close()
	for rows.Next() {
		var credID, crewID string
		if rows.Scan(&credID, &crewID) == nil {
			result[credID] = append(result[credID], crewID)
		}
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("iterate crew IDs batch", "error", err)
	}
	return result
}

// setCrewIDs replaces crew_ids for a credential in the junction table.
func (h *CredentialHandler) setCrewIDs(ctx context.Context, tx *sql.Tx, credentialID string, crewIDs []string) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM credential_crews WHERE credential_id = ?", credentialID); err != nil {
		return err
	}
	for _, crewID := range crewIDs {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO credential_crews (credential_id, crew_id) VALUES (?, ?)",
			credentialID, crewID); err != nil {
			return err
		}
	}
	return nil
}
