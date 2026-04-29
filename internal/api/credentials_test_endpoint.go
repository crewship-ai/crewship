package api

// The Test endpoint — exercises a stored credential against its
// provider (or stubs the call for offline providers) so the user
// can confirm the key works without leaving the UI. Long enough
// on its own to deserve a file. Extracted from credentials.go.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

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

	type testResult struct {
		Valid  bool   `json:"valid"`
		Status int    `json:"status"`
		Error  string `json:"error,omitempty"`
	}

	switch body.Provider {
	case "ANTHROPIC":
		// OAuth setup tokens (sk-ant-oat*) cannot be validated via standard API.
		// They only work inside Claude Code's authenticated tunnel.
		if body.Type == "AI_CLI_TOKEN" || isAnthropicOAuthToken(body.Value) {
			writeJSON(w, http.StatusOK, testResult{Valid: true, Error: "OAuth token accepted (cannot validate via API, will be verified at runtime)"})
			return
		}

		req, err := http.NewRequestWithContext(ctx, "GET", "https://api.anthropic.com/v1/models", nil)
		if err != nil {
			writeJSON(w, http.StatusOK, testResult{Error: "Failed to create request"})
			return
		}
		req.Header.Set("x-api-key", body.Value)
		req.Header.Set("anthropic-version", "2023-06-01")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			writeJSON(w, http.StatusOK, testResult{Error: "Connection failed: " + err.Error()})
			return
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)

		switch resp.StatusCode {
		case http.StatusOK:
			writeJSON(w, http.StatusOK, testResult{Valid: true, Status: resp.StatusCode})
		case http.StatusUnauthorized:
			writeJSON(w, http.StatusOK, testResult{Status: resp.StatusCode, Error: "Invalid API key"})
		case http.StatusForbidden:
			writeJSON(w, http.StatusOK, testResult{Status: resp.StatusCode, Error: "Access revoked"})
		case http.StatusTooManyRequests:
			writeJSON(w, http.StatusOK, testResult{Valid: true, Status: resp.StatusCode, Error: "Rate limited (key is valid but temporarily throttled)"})
		default:
			writeJSON(w, http.StatusOK, testResult{Status: resp.StatusCode, Error: fmt.Sprintf("Unexpected response: %d", resp.StatusCode)})
		}

	case "OPENAI":
		req, err := http.NewRequestWithContext(ctx, "GET", "https://api.openai.com/v1/models", nil)
		if err != nil {
			writeJSON(w, http.StatusOK, testResult{Error: "Failed to create request"})
			return
		}
		req.Header.Set("Authorization", "Bearer "+body.Value)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			writeJSON(w, http.StatusOK, testResult{Error: "Connection failed: " + err.Error()})
			return
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)

		switch resp.StatusCode {
		case http.StatusOK:
			writeJSON(w, http.StatusOK, testResult{Valid: true, Status: resp.StatusCode})
		case http.StatusUnauthorized:
			writeJSON(w, http.StatusOK, testResult{Status: resp.StatusCode, Error: "Invalid API key"})
		default:
			writeJSON(w, http.StatusOK, testResult{Status: resp.StatusCode, Error: fmt.Sprintf("Unexpected response: %d", resp.StatusCode)})
		}

	case "GOOGLE":
		req, err := http.NewRequestWithContext(ctx, "GET", "https://generativelanguage.googleapis.com/v1/models?key="+body.Value, nil)
		if err != nil {
			writeJSON(w, http.StatusOK, testResult{Error: "Failed to create request"})
			return
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			writeJSON(w, http.StatusOK, testResult{Error: "Connection failed: " + err.Error()})
			return
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)

		if resp.StatusCode == http.StatusOK {
			writeJSON(w, http.StatusOK, testResult{Valid: true, Status: resp.StatusCode})
		} else {
			writeJSON(w, http.StatusOK, testResult{Status: resp.StatusCode, Error: fmt.Sprintf("Unexpected response: %d", resp.StatusCode)})
		}

	case "GITHUB":
		req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user", nil)
		if err != nil {
			writeJSON(w, http.StatusOK, testResult{Error: "Failed to create request"})
			return
		}
		req.Header.Set("Authorization", "Bearer "+body.Value)
		req.Header.Set("User-Agent", "Crewship/1.0")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			writeJSON(w, http.StatusOK, testResult{Error: "Connection failed: " + err.Error()})
			return
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)

		switch resp.StatusCode {
		case http.StatusOK:
			writeJSON(w, http.StatusOK, testResult{Valid: true, Status: resp.StatusCode})
		case http.StatusUnauthorized:
			writeJSON(w, http.StatusOK, testResult{Status: resp.StatusCode, Error: "Invalid token"})
		case http.StatusForbidden:
			writeJSON(w, http.StatusOK, testResult{Status: resp.StatusCode, Error: "Token lacks required scopes"})
		default:
			writeJSON(w, http.StatusOK, testResult{Status: resp.StatusCode, Error: fmt.Sprintf("Unexpected response: %d", resp.StatusCode)})
		}

	case "GITLAB":
		req, err := http.NewRequestWithContext(ctx, "GET", "https://gitlab.com/api/v4/user", nil)
		if err != nil {
			writeJSON(w, http.StatusOK, testResult{Error: "Failed to create request"})
			return
		}
		req.Header.Set("PRIVATE-TOKEN", body.Value)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			writeJSON(w, http.StatusOK, testResult{Error: "Connection failed: " + err.Error()})
			return
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)

		switch resp.StatusCode {
		case http.StatusOK:
			writeJSON(w, http.StatusOK, testResult{Valid: true, Status: resp.StatusCode})
		case http.StatusUnauthorized:
			writeJSON(w, http.StatusOK, testResult{Status: resp.StatusCode, Error: "Invalid token"})
		case http.StatusForbidden:
			writeJSON(w, http.StatusOK, testResult{Status: resp.StatusCode, Error: "Token lacks required scopes"})
		default:
			writeJSON(w, http.StatusOK, testResult{Status: resp.StatusCode, Error: fmt.Sprintf("Unexpected response: %d", resp.StatusCode)})
		}

	case "VERCEL":
		req, err := http.NewRequestWithContext(ctx, "GET", "https://api.vercel.com/v2/user", nil)
		if err != nil {
			writeJSON(w, http.StatusOK, testResult{Error: "Failed to create request"})
			return
		}
		req.Header.Set("Authorization", "Bearer "+body.Value)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			writeJSON(w, http.StatusOK, testResult{Error: "Connection failed: " + err.Error()})
			return
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)

		switch resp.StatusCode {
		case http.StatusOK:
			writeJSON(w, http.StatusOK, testResult{Valid: true, Status: resp.StatusCode})
		case http.StatusUnauthorized, http.StatusForbidden:
			writeJSON(w, http.StatusOK, testResult{Status: resp.StatusCode, Error: "Invalid token"})
		default:
			writeJSON(w, http.StatusOK, testResult{Status: resp.StatusCode, Error: fmt.Sprintf("Unexpected response: %d", resp.StatusCode)})
		}

	default:
		writeJSON(w, http.StatusOK, testResult{Valid: true, Error: "No validation available for this provider"})
	}
}

// DefaultEnvVar returns the conventional env var name for a CLI tool provider.
// GET /api/v1/credentials/default-env-var?provider=GITHUB
