package api

import (
	"context"
	"database/sql"
	"fmt"
)

// credentialVisibilityFilter builds the SQL fragment + arguments that
// constrain which credentials the caller may see, based on their
// workspace role.
//
// MANAGER+ (canRole "update") sees every credential in the workspace —
// they own the credential lifecycle and need full visibility for
// rotations, revokes, and audit reviews.
//
// MEMBER and VIEWER are scoped: they see WORKSPACE-scoped credentials
// (which are intentionally workspace-wide, e.g. shared CI tokens) plus
// any CREW-scoped credential whose crew they belong to via
// crew_members. This mirrors how a teammate without admin rights only
// gets to see secrets attached to projects they actually work on.
//
// Returns ("", nil) for full-access roles so callers can append the
// fragment unconditionally.
func credentialVisibilityFilter(role string, user *AuthUser) (string, []any) {
	if canRole(role, "update") {
		return "", nil
	}
	// Defensive: an empty user shouldn't reach here (auth middleware
	// guarantees one), but if it does, scope-only filter blanks the
	// crew side so we never serve CREW-scoped rows under no identity.
	var userID string
	if user != nil {
		userID = user.ID
	}
	return ` AND (
		c.scope = 'WORKSPACE'
		OR EXISTS (
			SELECT 1 FROM credential_crews cc
			JOIN crew_members cm ON cm.crew_id = cc.crew_id
			WHERE cc.credential_id = c.id AND cm.user_id = ?
		)
	)`, []any{userID}
}

// Batch / junction-table loaders for CredentialHandler. Lifted out of
// credentials.go to keep the handler file focused on HTTP-facing
// methods. All functions are unexported methods on CredentialHandler
// so behaviour and package-private access stay identical.
//
// The loadXxx helpers intentionally return empty maps/slices on DB
// errors rather than failing the handler — list views show `—` for
// the affected credential column and the request otherwise succeeds.
// This matches the pre-refactor behaviour; the split kept it, but
// every swallowed error is now logged at Error so the silent drop is
// observable in ops. If you change the contract to return errors,
// callers in credentials.go must be updated to surface the failure.

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
		h.logger.Error("loadAgentNamesBatch: query failed", "error", err, "n", len(credentialIDs))
		return result
	}
	defer rows.Close()
	for rows.Next() {
		var credID, agentName string
		if err := rows.Scan(&credID, &agentName); err != nil {
			h.logger.Error("loadAgentNamesBatch: scan failed", "error", err)
			continue
		}
		result[credID] = append(result[credID], agentName)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("loadAgentNamesBatch: iterate", "error", err)
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
		h.logger.Error("loadMCPUsedBatch: query failed", "error", err, "n", len(credentialIDs))
		return result
	}
	defer rows.Close()
	for rows.Next() {
		var credID string
		if err := rows.Scan(&credID); err != nil {
			h.logger.Error("loadMCPUsedBatch: scan failed", "error", err)
			continue
		}
		result[credID] = true
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("loadMCPUsedBatch: iterate", "error", err)
	}
	return result
}

// loadCrewIDs fetches crew_ids from the junction table for a single credential.
func (h *CredentialHandler) loadCrewIDs(ctx context.Context, credentialID string) []string {
	rows, err := h.db.QueryContext(ctx,
		"SELECT crew_id FROM credential_crews WHERE credential_id = ? ORDER BY created_at", credentialID)
	if err != nil {
		h.logger.Error("loadCrewIDs: query failed", "error", err, "credential_id", credentialID)
		return []string{}
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			h.logger.Error("loadCrewIDs: scan failed", "error", err, "credential_id", credentialID)
			continue
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("loadCrewIDs: iterate", "error", err, "credential_id", credentialID)
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
		h.logger.Error("loadCrewIDsBatch: query failed", "error", err, "n", len(credentialIDs))
		return result
	}
	defer rows.Close()
	for rows.Next() {
		var credID, crewID string
		if err := rows.Scan(&credID, &crewID); err != nil {
			h.logger.Error("loadCrewIDsBatch: scan failed", "error", err)
			continue
		}
		result[credID] = append(result[credID], crewID)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("loadCrewIDsBatch: iterate", "error", err)
	}
	return result
}

// setCrewIDs replaces crew_ids for a credential in the junction table.
func (h *CredentialHandler) setCrewIDs(ctx context.Context, tx *sql.Tx, credentialID string, crewIDs []string) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM credential_crews WHERE credential_id = ?", credentialID); err != nil {
		return fmt.Errorf("setCrewIDs: delete existing crew bindings for credential %q: %w", credentialID, err)
	}
	for _, crewID := range crewIDs {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO credential_crews (credential_id, crew_id) VALUES (?, ?)",
			credentialID, crewID); err != nil {
			return fmt.Errorf("setCrewIDs: insert crew %q for credential %q: %w", crewID, credentialID, err)
		}
	}
	return nil
}
