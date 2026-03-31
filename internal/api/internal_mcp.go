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
		if !coveredVars[envVar] {
			missing = append(missing, envVar)
		}
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

	var query string
	var queryArgs []interface{}
	if isClientID || isClientSecret {
		query = `
			SELECT id, encrypted_value, type,
				oauth_client_id, oauth_client_secret_enc, oauth_token_url,
				oauth_refresh_token_enc, oauth_token_expires_at
			FROM credentials
			WHERE workspace_id = ? AND deleted_at IS NULL AND status != 'REVOKED'
			  AND (name LIKE ? OR (type = 'OAUTH2' AND oauth_client_id != '' AND oauth_client_id IS NOT NULL))
			ORDER BY created_at DESC LIMIT 1`
		queryArgs = []interface{}{workspaceID, prefix + "%"}
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

	// Decrypt client secret and refresh token.
	clientSecret, err := encryption.Decrypt(clientSecretEnc)
	if err != nil {
		logger.Warn("decrypt client secret for token refresh", "credential_id", credID, "error", err)
		return currentEncValue
	}
	refreshToken, err := encryption.Decrypt(refreshTokenEnc)
	if err != nil {
		logger.Warn("decrypt refresh token", "credential_id", credID, "error", err)
		return currentEncValue
	}

	// Call the token endpoint.
	tokenResp, err := refreshOAuthToken(tokenURL, clientID, clientSecret, refreshToken)
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
		if err == nil {
			_, _ = db.ExecContext(ctx, `
				UPDATE credentials SET
					encrypted_value = ?, oauth_token_expires_at = ?,
					oauth_refresh_token_enc = ?, status = 'ACTIVE', updated_at = datetime('now')
				WHERE id = ?`, newEnc, newExpiry, newRefreshEnc, credID)
		}
	} else {
		_, _ = db.ExecContext(ctx, `
			UPDATE credentials SET
				encrypted_value = ?, oauth_token_expires_at = ?,
				status = 'ACTIVE', updated_at = datetime('now')
			WHERE id = ?`, newEnc, newExpiry, credID)
	}

	logger.Info("refreshed OAuth token before agent exec", "credential_id", credID, "new_expiry", newExpiry)
	return newEnc
}
