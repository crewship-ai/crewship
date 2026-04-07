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

	// Find matching MCP servers (both crew and workspace scoped)
	type mcpMatch struct {
		serverID string
		scope    string
		crewID   string // only set for crew scope
	}
	var matches []mcpMatch

	// Crew-scoped servers
	crewRows, err := h.db.QueryContext(ctx, `
		SELECT cs.id, cs.crew_id FROM crew_mcp_servers cs
		JOIN crews c ON c.id = cs.crew_id AND c.workspace_id = ?
		WHERE cs.name = ? AND cs.deleted_at IS NULL`, workspaceID, serverName)
	if err != nil {
		h.logger.Warn("auto-bind: find crew MCP servers", "error", err)
	} else {
		for crewRows.Next() {
			var m mcpMatch
			if crewRows.Scan(&m.serverID, &m.crewID) == nil {
				m.scope = "crew"
				matches = append(matches, m)
			}
		}
		if err := crewRows.Err(); err != nil {
			h.logger.Warn("auto-bind: iterate crew MCP servers", "error", err)
		}
		crewRows.Close()
	}

	// Workspace-scoped servers
	wsRows, err := h.db.QueryContext(ctx, `
		SELECT id FROM workspace_mcp_servers
		WHERE workspace_id = ? AND name = ? AND deleted_at IS NULL`, workspaceID, serverName)
	if err != nil {
		h.logger.Warn("auto-bind: find workspace MCP servers", "error", err)
	} else {
		for wsRows.Next() {
			var m mcpMatch
			if wsRows.Scan(&m.serverID) == nil {
				m.scope = "workspace"
				matches = append(matches, m)
			}
		}
		if err := wsRows.Err(); err != nil {
			h.logger.Warn("auto-bind: iterate workspace MCP servers", "error", err)
		}
		wsRows.Close()
	}

	for _, m := range matches {
		// Update existing bindings that have no credential
		res, err := h.db.ExecContext(ctx, `
			UPDATE agent_mcp_bindings SET credential_id = ?, cred_type = 'bearer'
			WHERE mcp_server_id = ? AND mcp_server_scope = ? AND (credential_id IS NULL OR credential_id = '')`,
			credentialID, m.serverID, m.scope)
		if err != nil {
			h.logger.Warn("auto-bind: update existing bindings", "error", err)
			continue
		}
		if affected, _ := res.RowsAffected(); affected > 0 {
			h.logger.Info("auto-bound credential to existing bindings",
				"credential_id", credentialID, "server", serverName, "scope", m.scope, "count", affected)
			continue
		}

		// No existing bindings — create bindings for agents in the crew (crew scope only)
		if m.scope != "crew" || m.crewID == "" {
			continue
		}
		agentRows, err := h.db.QueryContext(ctx,
			"SELECT id FROM agents WHERE crew_id = ? AND deleted_at IS NULL", m.crewID)
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
				VALUES (?, ?, ?, ?, ?, 'bearer', 1, datetime('now'))`,
				generateCUID(), agentID, m.serverID, m.scope, credentialID); err == nil {
				count++
			}
		}
		if err := agentRows.Err(); err != nil {
			h.logger.Warn("auto-bind: iterate agents", "error", err)
		}
		agentRows.Close()
		if count > 0 {
			h.logger.Info("auto-created bindings with credential",
				"credential_id", credentialID, "server", serverName, "scope", m.scope, "agents", count)
		}
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
