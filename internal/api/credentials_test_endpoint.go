package api

// The Test endpoint — exercises a stored credential against its
// provider (or stubs the call for offline providers) so the user
// can confirm the key works without leaving the UI. Long enough
// on its own to deserve a file. Extracted from credentials.go.
//
// Two flavours share the provider-specific probe logic:
//   - Test       — POST /api/v1/credentials/test, value supplied in body
//   - TestStored — POST /api/v1/credentials/{id}/test, value loaded from DB
//
// TestStored additionally records an AuditEventTest event so the
// detail-sheet "Test now" button feeds the timeline.

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// testResult is the JSON shape both Test and TestStored return.
type testResult struct {
	Valid  bool   `json:"valid"`
	Status int    `json:"status"`
	Error  string `json:"error,omitempty"`
}

// probeProvider runs the provider-specific HTTP check and returns a
// structured result. Transport errors fold into result.Error so the
// HTTP layer can always emit 200.
func probeProvider(ctx context.Context, provider, ctype, value string) testResult {
	switch provider {
	case "ANTHROPIC":
		// OAuth setup tokens (sk-ant-oat*) cannot be validated via standard API.
		// They only work inside Claude Code's authenticated tunnel.
		if ctype == "AI_CLI_TOKEN" || isAnthropicOAuthToken(value) {
			return testResult{Valid: true, Error: "OAuth token accepted (cannot validate via API, will be verified at runtime)"}
		}
		req, err := http.NewRequestWithContext(ctx, "GET", "https://api.anthropic.com/v1/models", nil)
		if err != nil {
			return testResult{Error: "Failed to create request"}
		}
		req.Header.Set("x-api-key", value)
		req.Header.Set("anthropic-version", "2023-06-01")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return testResult{Error: "Connection failed: " + err.Error()}
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		switch resp.StatusCode {
		case http.StatusOK:
			return testResult{Valid: true, Status: resp.StatusCode}
		case http.StatusUnauthorized:
			return testResult{Status: resp.StatusCode, Error: "Invalid API key"}
		case http.StatusForbidden:
			return testResult{Status: resp.StatusCode, Error: "Access revoked"}
		case http.StatusTooManyRequests:
			return testResult{Valid: true, Status: resp.StatusCode, Error: "Rate limited (key is valid but temporarily throttled)"}
		default:
			return testResult{Status: resp.StatusCode, Error: fmt.Sprintf("Unexpected response: %d", resp.StatusCode)}
		}

	case "OPENAI":
		req, err := http.NewRequestWithContext(ctx, "GET", "https://api.openai.com/v1/models", nil)
		if err != nil {
			return testResult{Error: "Failed to create request"}
		}
		req.Header.Set("Authorization", "Bearer "+value)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return testResult{Error: "Connection failed: " + err.Error()}
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		switch resp.StatusCode {
		case http.StatusOK:
			return testResult{Valid: true, Status: resp.StatusCode}
		case http.StatusUnauthorized:
			return testResult{Status: resp.StatusCode, Error: "Invalid API key"}
		default:
			return testResult{Status: resp.StatusCode, Error: fmt.Sprintf("Unexpected response: %d", resp.StatusCode)}
		}

	case "GOOGLE":
		req, err := http.NewRequestWithContext(ctx, "GET", "https://generativelanguage.googleapis.com/v1/models?key="+value, nil)
		if err != nil {
			return testResult{Error: "Failed to create request"}
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return testResult{Error: "Connection failed: " + err.Error()}
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		if resp.StatusCode == http.StatusOK {
			return testResult{Valid: true, Status: resp.StatusCode}
		}
		return testResult{Status: resp.StatusCode, Error: fmt.Sprintf("Unexpected response: %d", resp.StatusCode)}

	case "CURSOR":
		// Cursor token validation: ping the auth endpoint with the token.
		// 401 → invalid; 200 → valid; 403 → API access not enabled on the
		// subscription (a Cursor-specific gotcha worth surfacing distinctly).
		req, err := http.NewRequestWithContext(ctx, "GET", "https://api2.cursor.sh/v0/me", nil)
		if err != nil {
			return testResult{Error: "Failed to create request"}
		}
		req.Header.Set("Authorization", "Bearer "+value)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return testResult{Error: "Connection failed: " + err.Error()}
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		switch resp.StatusCode {
		case http.StatusOK:
			return testResult{Valid: true, Status: resp.StatusCode}
		case http.StatusUnauthorized:
			return testResult{Status: resp.StatusCode, Error: "Invalid Cursor API key"}
		case http.StatusForbidden:
			return testResult{Status: resp.StatusCode, Error: "Cursor subscription does not have API access enabled — visit cursor.com/account"}
		default:
			return testResult{Status: resp.StatusCode, Error: fmt.Sprintf("Unexpected response: %d", resp.StatusCode)}
		}

	case "FACTORY":
		// Factory Droid token validation: ping app.factory.ai with the API key.
		req, err := http.NewRequestWithContext(ctx, "GET", "https://app.factory.ai/api/cli/auth/whoami", nil)
		if err != nil {
			return testResult{Error: "Failed to create request"}
		}
		req.Header.Set("Authorization", "Bearer "+value)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return testResult{Error: "Connection failed: " + err.Error()}
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		switch resp.StatusCode {
		case http.StatusOK:
			return testResult{Valid: true, Status: resp.StatusCode}
		case http.StatusUnauthorized:
			return testResult{Status: resp.StatusCode, Error: "Invalid Factory API key"}
		default:
			return testResult{Status: resp.StatusCode, Error: fmt.Sprintf("Unexpected response: %d", resp.StatusCode)}
		}

	case "GITHUB":
		req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user", nil)
		if err != nil {
			return testResult{Error: "Failed to create request"}
		}
		req.Header.Set("Authorization", "Bearer "+value)
		req.Header.Set("User-Agent", "Crewship/1.0")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return testResult{Error: "Connection failed: " + err.Error()}
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		switch resp.StatusCode {
		case http.StatusOK:
			return testResult{Valid: true, Status: resp.StatusCode}
		case http.StatusUnauthorized:
			return testResult{Status: resp.StatusCode, Error: "Invalid token"}
		case http.StatusForbidden:
			return testResult{Status: resp.StatusCode, Error: "Token lacks required scopes"}
		default:
			return testResult{Status: resp.StatusCode, Error: fmt.Sprintf("Unexpected response: %d", resp.StatusCode)}
		}

	case "GITLAB":
		req, err := http.NewRequestWithContext(ctx, "GET", "https://gitlab.com/api/v4/user", nil)
		if err != nil {
			return testResult{Error: "Failed to create request"}
		}
		req.Header.Set("PRIVATE-TOKEN", value)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return testResult{Error: "Connection failed: " + err.Error()}
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		switch resp.StatusCode {
		case http.StatusOK:
			return testResult{Valid: true, Status: resp.StatusCode}
		case http.StatusUnauthorized:
			return testResult{Status: resp.StatusCode, Error: "Invalid token"}
		case http.StatusForbidden:
			return testResult{Status: resp.StatusCode, Error: "Token lacks required scopes"}
		default:
			return testResult{Status: resp.StatusCode, Error: fmt.Sprintf("Unexpected response: %d", resp.StatusCode)}
		}

	case "VERCEL":
		req, err := http.NewRequestWithContext(ctx, "GET", "https://api.vercel.com/v2/user", nil)
		if err != nil {
			return testResult{Error: "Failed to create request"}
		}
		req.Header.Set("Authorization", "Bearer "+value)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return testResult{Error: "Connection failed: " + err.Error()}
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		switch resp.StatusCode {
		case http.StatusOK:
			return testResult{Valid: true, Status: resp.StatusCode}
		case http.StatusUnauthorized, http.StatusForbidden:
			return testResult{Status: resp.StatusCode, Error: "Invalid token"}
		default:
			return testResult{Status: resp.StatusCode, Error: fmt.Sprintf("Unexpected response: %d", resp.StatusCode)}
		}

	default:
		return testResult{Valid: true, Error: "No validation available for this provider"}
	}
}

// Test validates a value supplied in the request body — no DB I/O.
// Used by the Add Credential wizard's inline "Test value" button.
func (h *CredentialHandler) Test(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Provider string `json:"provider"`
		Type     string `json:"type"`
		Value    string `json:"value"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}
	if body.Value == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Value is required"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, probeProvider(ctx, body.Provider, body.Type, body.Value))
}

// TestStored validates an existing credential by ID. Looks up + decrypts
// the stored value, runs the same probe, and records an AuditEventTest
// event so the detail-sheet timeline reflects the manual check.
//
// POST /api/v1/credentials/{credentialId}/test
func (h *CredentialHandler) TestStored(w http.ResponseWriter, r *http.Request) {
	credID := r.PathValue("credentialId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	user := UserFromContext(r.Context())

	// Crew-scoped visibility: a MEMBER/VIEWER must be allowed to see
	// the credential before they're allowed to test it. Without this
	// the FE could leak credential existence by trial-and-error.
	visFilter, visArgs := credentialVisibilityFilter(role, user)
	args := append([]any{credID, workspaceID}, visArgs...)

	var (
		provider, ctype, encValue string
	)
	err := h.db.QueryRowContext(r.Context(), `
		SELECT provider, type, encrypted_value
		FROM credentials
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL `+visFilter+`
	`, args...).Scan(&provider, &ctype, &encValue)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Credential not found"})
		return
	}
	if err != nil {
		h.logger.Error("test stored: lookup", "error", err, "credential_id", credID)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	value, err := encryption.Decrypt(encValue)
	if err != nil {
		h.logger.Error("test stored: decrypt", "error", err, "credential_id", credID)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to decrypt credential"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	res := probeProvider(ctx, provider, ctype, value)

	// Audit goes outside the request path failure mode — log warn but
	// don't fail the test if the audit insert hiccups.
	meta := map[string]any{"valid": res.Valid}
	if res.Error != "" {
		meta["error"] = res.Error
	}
	if recErr := RecordCredentialEvent(r.Context(), h.db, h.logger, credID, AuditEventTest, "", clientIP(r), meta); recErr != nil {
		h.logger.Warn("record TEST audit event", "error", recErr, "credential_id", credID)
	}

	writeJSON(w, http.StatusOK, res)
}

// DefaultEnvVar returns the conventional env var name for a CLI tool provider.
// GET /api/v1/credentials/default-env-var?provider=GITHUB
