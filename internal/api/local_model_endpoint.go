package api

import (
	"context"
	"database/sql"
	"log/slog"
)

// resolveLocalModelEndpoint returns the OpenAI-compatible base URL a coding
// agent should point a local ("ollama/…") model at, configured the same way
// as an API key (#955): as an ENDPOINT_URL credential in the vault rather than
// a server-global env var.
//
// Precedence (first hit wins):
//  1. a per-agent ENDPOINT_URL credential — already present in `assigned`
//     (the agent's resolved credential list) because it was assigned to this
//     agent, giving a per-agent override;
//  2. the workspace's ENDPOINT_URL credential (any ACTIVE ENDPOINT_URL row in
//     the workspace, not bound to a specific agent) — the workspace default.
//
// Returns "" when neither exists; the orchestrator then applies the deprecated
// server-env fallback (CREWSHIP_LOCAL_MODEL_BASE_URL). The returned URL is
// re-validated through the same gate as create so a value that was somehow
// stored malformed can't reach OpenCode's config.
func resolveLocalModelEndpoint(ctx context.Context, db *sql.DB, logger *slog.Logger, workspaceID string, assigned []mcpCredEntry) string {
	// 1. Per-agent override: an assigned ENDPOINT_URL credential. `assigned`
	//    is already ACTIVE-filtered and sentinel-filtered by
	//    resolveAgentCredentials, and ordered by priority ASC — take the
	//    first valid URL.
	for _, c := range assigned {
		if c.Type != CredTypeEndpointURL {
			continue
		}
		if validateEndpointURL(c.Value) == "" {
			return c.Value
		}
	}

	// 2. Workspace default: the newest ACTIVE ENDPOINT_URL credential in the
	//    workspace that isn't scoped to a crew. Unassigned rows never appear
	//    in `assigned`, so this direct query is what makes a single
	//    workspace-wide endpoint apply to every agent.
	var encValue string
	err := db.QueryRowContext(ctx, `
		SELECT encrypted_value FROM credentials
		WHERE workspace_id = ? AND type = ? AND status = 'ACTIVE' AND deleted_at IS NULL
		ORDER BY created_at DESC, id ASC
		LIMIT 1
	`, workspaceID, CredTypeEndpointURL).Scan(&encValue)
	if err != nil {
		if err != sql.ErrNoRows && logger != nil {
			logger.Warn("resolve workspace local-model endpoint", "error", err)
		}
		return ""
	}
	dec, err := decryptCredential(encValue)
	if err != nil {
		if logger != nil {
			logger.Warn("decrypt workspace local-model endpoint", "error", err)
		}
		return ""
	}
	if isPendingSentinel(dec) || validateEndpointURL(dec) != "" {
		return ""
	}
	return dec
}
