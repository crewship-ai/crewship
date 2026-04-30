package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// ---------------------------------------------------------------------------
// MCP credential auto-resolution
// ---------------------------------------------------------------------------

// reservedMCPEnvVarPrefixes are env var prefixes that MUST NOT be auto-resolved
// from MCP configs. An agent (or anyone with MCP-edit permission) could
// otherwise drop ${INTERNAL_TOKEN} or ${ENCRYPTION_KEY} into a config and the
// auto-resolver would happily look up a workspace credential of that name and
// hand the plaintext to the MCP server process, which may be a third-party
// binary we don't control. The defense is a deny-list at the resolver entry
// point — even if a credential with one of these names somehow exists, MCP
// configs cannot reach it.
//
// This is the H4 audit fix; pair it with operator hygiene (don't name regular
// credentials with these prefixes — they would be unusable from MCP anyway).
var reservedMCPEnvVarPrefixes = []string{
	"INTERNAL_",   // sidecar / X-Internal-Token
	"NEXTAUTH_",   // session JWT secret
	"ENCRYPTION_", // credential encryption key
	"CREWSHIP_",   // crewshipd-internal config
	"JWT_",        // any JWT signing key
	"DATABASE_",   // DB connection strings
	"OLLAMA_",     // model server (local LLM)
}

// isReservedMCPEnvVar reports whether the given env var name is in a
// reserved namespace that auto-resolve must refuse to look up.
func isReservedMCPEnvVar(name string) bool {
	upper := strings.ToUpper(name)
	for _, p := range reservedMCPEnvVarPrefixes {
		if strings.HasPrefix(upper, p) {
			return true
		}
	}
	return false
}

// autoResolveMCPCredentials parses MCP config JSONs for ${VAR} env references
// and finds workspace credentials whose name matches the derived prefix.
// This bridges the gap between the MCP config editor (which stores env var
// references like ${GOOGLE_ACCESS_TOKEN}) and the credential system (which
// stores credentials with names like "google-access-token-oauth-abc123").
//
// For OAUTH2 credentials, it also maps:
//   - *_CLIENT_ID env vars → oauth_client_id field (plaintext)
//   - *_CLIENT_SECRET env vars → oauth_client_secret_enc field (decrypted)
//   - *_ACCESS_TOKEN env vars → encrypted_value (with auto-refresh)
//
// References whose name matches reservedMCPEnvVarPrefixes are rejected with
// a WARN log — those namespaces are owned by crewshipd-internal config and
// must not be exfiltrated through MCP server env injection.
func autoResolveMCPCredentials(
	ctx context.Context,
	db *sql.DB,
	logger *slog.Logger,
	workspaceID string,
	existing []mcpCredEntry,
	configs ...string,
) []mcpCredEntry {
	// Collect ${VAR} references from MCP configs.
	refs := collectMCPEnvVarRefs(configs...)
	if len(refs) == 0 {
		return existing
	}

	// Remove refs already covered by explicit agent_credentials.
	coveredVars := make(map[string]bool)
	for _, c := range existing {
		coveredVars[c.EnvVar] = true
	}
	var missing []string
	for envVar := range refs {
		if coveredVars[envVar] {
			continue
		}
		if isReservedMCPEnvVar(envVar) {
			logger.Warn("auto-resolve MCP credential refused: reserved namespace",
				"env_var", envVar, "workspace_id", workspaceID)
			continue
		}
		missing = append(missing, envVar)
	}
	if len(missing) == 0 {
		return existing
	}

	for _, envVar := range missing {
		entry, ok := resolveOneEnvVar(ctx, db, logger, workspaceID, envVar)
		if ok {
			existing = append(existing, entry)
		}
	}

	return existing
}

// resolveOneEnvVar finds a credential for a single ${VAR} reference.
func resolveOneEnvVar(
	ctx context.Context,
	db *sql.DB,
	logger *slog.Logger,
	workspaceID, envVar string,
) (mcpCredEntry, bool) {
	prefix := strings.ToLower(strings.ReplaceAll(envVar, "_", "-"))

	// For CLIENT_ID/CLIENT_SECRET env vars, also search OAUTH2 credentials
	// that have the matching oauth_client_id field (not just by name prefix).
	isClientID := strings.HasSuffix(envVar, "_CLIENT_ID")
	isClientSecret := strings.HasSuffix(envVar, "_CLIENT_SECRET")

	// Derive the provider prefix from the env var name.
	// E.g. "GOOGLE_CLIENT_ID" → "google", "SLACK_CLIENT_SECRET" → "slack"
	oauthNamePrefix := ""
	if isClientID {
		oauthNamePrefix = strings.ToLower(strings.TrimSuffix(envVar, "_CLIENT_ID"))
	} else if isClientSecret {
		oauthNamePrefix = strings.ToLower(strings.TrimSuffix(envVar, "_CLIENT_SECRET"))
	}
	oauthNamePrefix = strings.ReplaceAll(oauthNamePrefix, "_", "-")

	var query string
	var queryArgs []interface{}
	if isClientID || isClientSecret {
		// Scope the OAUTH2 fallback to credentials whose name matches the provider prefix,
		// not any arbitrary OAUTH2 credential in the workspace.
		query = `
			SELECT id, encrypted_value, type,
				oauth_client_id, oauth_client_secret_enc, oauth_token_url,
				oauth_refresh_token_enc, oauth_token_expires_at
			FROM credentials
			WHERE workspace_id = ? AND deleted_at IS NULL AND status != 'REVOKED'
			  AND (name LIKE ? OR (type = 'OAUTH2' AND name LIKE ? AND oauth_client_id != '' AND oauth_client_id IS NOT NULL))
			ORDER BY created_at DESC LIMIT 1`
		queryArgs = []interface{}{workspaceID, prefix + "%", oauthNamePrefix + "%"}
	} else {
		query = `
			SELECT id, encrypted_value, type,
				oauth_client_id, oauth_client_secret_enc, oauth_token_url,
				oauth_refresh_token_enc, oauth_token_expires_at
			FROM credentials
			WHERE workspace_id = ? AND name LIKE ? AND deleted_at IS NULL
			  AND status != 'REVOKED'
			ORDER BY created_at DESC LIMIT 1`
		queryArgs = []interface{}{workspaceID, prefix + "%"}
	}

	row := db.QueryRowContext(ctx, query, queryArgs...)

	var id, encValue, credType string
	var oaClientID, oaSecretEnc, oaTokenURL, oaRefreshEnc, oaExpiresAt sql.NullString
	if err := row.Scan(&id, &encValue, &credType,
		&oaClientID, &oaSecretEnc, &oaTokenURL, &oaRefreshEnc, &oaExpiresAt); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			logger.Warn("auto-resolve MCP credential", "env_var", envVar, "error", err)
		}
		return mcpCredEntry{}, false
	}

	// For OAUTH2 credentials, map env var names to the correct fields.
	if credType == "OAUTH2" {
		if isClientID && oaClientID.Valid && oaClientID.String != "" {
			logger.Debug("auto-resolved MCP credential (oauth_client_id)", "env_var", envVar, "credential_id", id)
			return mcpCredEntry{ID: id, EnvVar: envVar, Value: oaClientID.String, Type: credType}, true
		}
		if isClientSecret && oaSecretEnc.Valid && oaSecretEnc.String != "" {
			dec, err := encryption.Decrypt(oaSecretEnc.String)
			if err != nil {
				logger.Warn("decrypt oauth_client_secret", "id", id, "error", err)
				return mcpCredEntry{}, false
			}
			logger.Debug("auto-resolved MCP credential (oauth_client_secret)", "env_var", envVar, "credential_id", id)
			return mcpCredEntry{ID: id, EnvVar: envVar, Value: dec, Type: credType}, true
		}
		// For access token env vars, refresh if needed.
		if oaRefreshEnc.Valid && oaRefreshEnc.String != "" && oaTokenURL.Valid {
			encValue = ensureFreshOAuthToken(ctx, db, logger, id, encValue,
				oaClientID.String, oaSecretEnc.String, oaTokenURL.String,
				oaRefreshEnc.String, oaExpiresAt.String)
		}
	}

	dec, err := encryption.Decrypt(encValue)
	if err != nil {
		logger.Warn("decrypt auto-resolved MCP credential", "id", id, "error", err)
		return mcpCredEntry{}, false
	}

	logger.Debug("auto-resolved MCP credential", "env_var", envVar, "credential_id", id)
	return mcpCredEntry{ID: id, EnvVar: envVar, Value: dec, Type: credType}, true
}

// collectMCPEnvVarRefs extracts ${VAR} references from the "env" blocks of
// MCP config JSON strings.
func collectMCPEnvVarRefs(configs ...string) map[string]bool {
	refs := make(map[string]bool)
	for _, cfg := range configs {
		if cfg == "" {
			continue
		}
		var wrapper struct {
			MCPServers map[string]struct {
				Env map[string]string `json:"env"`
			} `json:"mcpServers"`
		}
		if err := json.Unmarshal([]byte(cfg), &wrapper); err != nil {
			continue
		}
		for _, srv := range wrapper.MCPServers {
			for _, val := range srv.Env {
				if strings.HasPrefix(val, "${") && strings.HasSuffix(val, "}") {
					varName := val[2 : len(val)-1]
					if varName != "" {
						refs[varName] = true
					}
				}
			}
		}
	}
	return refs
}

// ensureFreshOAuthToken checks whether an OAUTH2 credential's access token is
// about to expire and refreshes it if needed.  Returns the (possibly updated)
// encrypted access token value.
func ensureFreshOAuthToken(
	ctx context.Context,
	db *sql.DB,
	logger *slog.Logger,
	credID string,
	currentEncValue string,
	clientID, clientSecretEnc, tokenURL, refreshTokenEnc, expiresAt string,
) string {
	// Check if token expires within the next 5 minutes.
	if expiresAt != "" {
		expiry, err := time.Parse(time.RFC3339, expiresAt)
		if err == nil && time.Until(expiry) > 5*time.Minute {
			return currentEncValue // Still fresh.
		}
	}

	// Decrypt client secret (may be empty for public/PKCE OAuth clients).
	clientSecret := ""
	if clientSecretEnc != "" {
		var err error
		clientSecret, err = encryption.Decrypt(clientSecretEnc)
		if err != nil {
			logger.Warn("decrypt client secret for token refresh", "credential_id", credID, "error", err)
			return currentEncValue
		}
	}
	refreshToken, err := encryption.Decrypt(refreshTokenEnc)
	if err != nil {
		logger.Warn("decrypt refresh token", "credential_id", credID, "error", err)
		return currentEncValue
	}

	// Call the token endpoint.
	tokenResp, err := refreshOAuthToken(ctx, tokenURL, clientID, clientSecret, refreshToken)
	if err != nil {
		logger.Warn("refresh OAuth token before exec", "credential_id", credID, "error", err)
		return currentEncValue
	}

	// Encrypt and persist the new access token.
	newEnc, err := encryption.Encrypt(tokenResp.AccessToken)
	if err != nil {
		logger.Warn("encrypt refreshed token", "credential_id", credID, "error", err)
		return currentEncValue
	}

	newExpiry := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)

	// Update refresh token only if a new one was issued.
	if tokenResp.RefreshToken != "" && tokenResp.RefreshToken != refreshToken {
		newRefreshEnc, err := encryption.Encrypt(tokenResp.RefreshToken)
		if err != nil {
			logger.Warn("encrypt refreshed refresh token", "credential_id", credID, "error", err)
			return currentEncValue
		}
		if _, err := db.ExecContext(ctx, `
			UPDATE credentials SET
				encrypted_value = ?, oauth_token_expires_at = ?,
				oauth_refresh_token_enc = ?, status = 'ACTIVE', updated_at = datetime('now')
			WHERE id = ?`, newEnc, newExpiry, newRefreshEnc, credID); err != nil {
			logger.Error("persist refreshed OAuth tokens", "credential_id", credID, "error", err)
			return currentEncValue
		}
	} else {
		if _, err := db.ExecContext(ctx, `
			UPDATE credentials SET
				encrypted_value = ?, oauth_token_expires_at = ?,
				status = 'ACTIVE', updated_at = datetime('now')
			WHERE id = ?`, newEnc, newExpiry, credID); err != nil {
			logger.Error("persist refreshed OAuth token", "credential_id", credID, "error", err)
			return currentEncValue
		}
	}

	logger.Info("refreshed OAuth token before agent exec", "credential_id", credID, "new_expiry", newExpiry)
	return newEnc
}
