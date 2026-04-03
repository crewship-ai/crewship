package api

import (
	"context"
	"strings"
)

// autoBindCredentialToMCPServers finds MCP servers matching the credential's
// name prefix and binds the credential to their agent bindings.
// E.g., credential "linear-oauth-abc12" matches server "linear".
func (h *OAuthHandler) autoBindCredentialToMCPServers(ctx context.Context, credentialID, workspaceID string) {
	// Get credential name to derive server name prefix
	var credName string
	if err := h.db.QueryRowContext(ctx,
		"SELECT name FROM credentials WHERE id = ?", credentialID).Scan(&credName); err != nil {
		return
	}

	// Extract server name: "linear-oauth-abc12" → "linear"
	serverName := credName
	if idx := strings.Index(credName, "-oauth"); idx > 0 {
		serverName = credName[:idx]
	}

	// Find matching MCP servers
	rows, err := h.db.QueryContext(ctx, `
		SELECT cs.id, cs.crew_id FROM crew_mcp_servers cs
		JOIN crews c ON c.id = cs.crew_id AND c.workspace_id = ?
		WHERE cs.name = ? AND cs.deleted_at IS NULL`, workspaceID, serverName)
	if err != nil {
		h.logger.Warn("auto-bind: find MCP servers", "error", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var serverID, crewID string
		if rows.Scan(&serverID, &crewID) != nil {
			continue
		}

		// Update existing bindings that have no credential
		res, err := h.db.ExecContext(ctx, `
			UPDATE agent_mcp_bindings SET credential_id = ?, cred_type = 'bearer'
			WHERE mcp_server_id = ? AND (credential_id IS NULL OR credential_id = '')`,
			credentialID, serverID)
		if err != nil {
			h.logger.Warn("auto-bind: update existing bindings", "error", err)
			continue
		}
		if affected, _ := res.RowsAffected(); affected > 0 {
			h.logger.Info("auto-bound credential to existing bindings",
				"credential_id", credentialID, "server", serverName, "count", affected)
			continue
		}

		// No existing bindings — create bindings for ALL agents in the crew
		agentRows, err := h.db.QueryContext(ctx,
			"SELECT id FROM agents WHERE crew_id = ? AND deleted_at IS NULL", crewID)
		if err != nil {
			continue
		}
		var count int
		for agentRows.Next() {
			var agentID string
			if agentRows.Scan(&agentID) != nil {
				continue
			}
			if _, err := h.db.ExecContext(ctx, `
				INSERT OR IGNORE INTO agent_mcp_bindings (id, agent_id, mcp_server_id, mcp_server_scope, credential_id, cred_type, enabled, created_at)
				VALUES (?, ?, ?, 'crew', ?, 'bearer', 1, datetime('now'))`,
				generateCUID(), agentID, serverID, credentialID); err == nil {
				count++
			}
		}
		if err := agentRows.Err(); err != nil {
			h.logger.Warn("auto-bind: iterate agents", "error", err)
		}
		agentRows.Close()
		if count > 0 {
			h.logger.Info("auto-created bindings with credential",
				"credential_id", credentialID, "server", serverName, "agents", count)
		}
	}
	if err := rows.Err(); err != nil {
		h.logger.Warn("auto-bind: iterate MCP servers", "error", err)
	}
}

// matchKnownProvider checks if an MCP server URL matches a known OAuth provider.
func matchKnownProvider(mcpURL string) *OAuthProvider {
	urlPatterns := map[string]string{
		"linear.app":    "linear",
		"gitlab.com":    "gitlab",
		"cloudflare.com": "cloudflare",
		"stripe.com":    "stripe",
		"notion.com":    "notion",
		"github.com":    "github",
		"googleapis.com": "google",
	}
	lower := strings.ToLower(mcpURL)
	for domain, providerKey := range urlPatterns {
		if strings.Contains(lower, domain) {
			if p, ok := OAuthProviders[providerKey]; ok {
				return &p
			}
		}
	}
	return nil
}
