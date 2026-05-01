package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const (
	// maxIntentLength is the maximum allowed length for the intent field.
	// Prevents prompt injection via oversized payloads and DoS via huge strings.
	maxIntentLength = 4096

	// maxCommandLength is the maximum allowed length for the command field in /keeper/execute.
	maxCommandLength = 4096
)

// credentialIDPattern allows only safe characters in credential IDs.
// Rejects path traversal (../), SQL meta-characters, and other injection vectors.
var credentialIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`)

// containsDangerousShellChars checks if a command contains shell operators that
// could be used for credential exfiltration or command injection.
// Content inside single quotes is considered safe (shell does not interpret them).
func containsDangerousShellChars(cmd string) bool {
	if strings.ContainsAny(cmd, "\n\r") {
		return true
	}
	parts := strings.Split(cmd, "'")
	for i, part := range parts {
		if i%2 == 1 {
			continue // inside single quotes — safe
		}
		if strings.ContainsAny(part, ";|>`") {
			return true
		}
		if strings.Contains(part, "&&") || strings.Contains(part, "||") || strings.Contains(part, "$(") {
			return true
		}
	}
	return false
}

// keeperRequestBody is what the agent sends to /keeper/request.
//
// agent_slug is intentionally NOT honoured for identity resolution — the
// sidecar attributes every keeper call to s.ipc.AgentID (the canonical
// identity for this sidecar). The field stays in the struct so older
// clients don't get a 400 when they include it, but its value is logged
// at WARN if it disagrees with the canonical slug. Slug-based identity
// was the C2 spoofing primitive in the security audit: any agent in the
// crew container could pass agent_slug=<peer> and the sidecar would
// forward that as requesting_agent_id, breaking non-repudiation.
type keeperRequestBody struct {
	CredentialID   string `json:"credential_id"`
	CredentialName string `json:"credential_name"`
	Intent         string `json:"intent"`
	TaskID         string `json:"task_id,omitempty"`
	AgentSlug      string `json:"agent_slug,omitempty"` // ignored; see comment above
}

// keeperExecuteBody is what the agent sends to /keeper/execute.
// The sidecar sets container_id and requesting_agent_id from IPC config — agents
// cannot override these fields. Same agent_slug rule as keeperRequestBody.
type keeperExecuteBody struct {
	CredentialID   string `json:"credential_id"`
	CredentialName string `json:"credential_name"`
	Intent         string `json:"intent"`
	Command        string `json:"command"`
	EnvVar         string `json:"env_var,omitempty"`
	TaskID         string `json:"task_id,omitempty"`
	AgentSlug      string `json:"agent_slug,omitempty"` // ignored; see keeperRequestBody comment
}

// handleKeeperRequest handles POST /keeper/request from agents (UID 1001).
// It validates the request and forwards it to crewshipd via the IPC channel.
// The response is a keeper decision (ALLOW/DENY/ESCALATE) — never a raw credential.
func (s *Server) handleKeeperRequest(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{
			"error": "keeper IPC not configured",
		})
		return
	}

	var req keeperRequestBody
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}

	req.CredentialID = strings.TrimSpace(req.CredentialID)
	req.CredentialName = strings.TrimSpace(req.CredentialName)
	req.Intent = strings.TrimSpace(req.Intent)

	if req.CredentialID == "" && req.CredentialName == "" {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{
			"error": "credential_id or credential_name required",
		})
		return
	}
	if req.Intent == "" {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{
			"error": "intent required",
		})
		return
	}

	// Reject oversized intents to prevent DoS and prompt injection via huge payloads
	if len(req.Intent) > maxIntentLength {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{
			"error": "intent exceeds maximum allowed length",
		})
		return
	}

	// Reject null bytes in the intent field (can indicate binary injection attempts)
	if strings.ContainsRune(req.Intent, 0) {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{
			"error": "intent contains invalid characters",
		})
		return
	}

	// Validate credential ID format: only alphanumeric, hyphens, underscores
	// Rejects path traversal (../), SQL meta-characters, and other injection vectors
	if req.CredentialID != "" && !credentialIDPattern.MatchString(req.CredentialID) {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{
			"error": "credential_id contains invalid characters",
		})
		return
	}

	// Identity is the canonical sidecar AgentID — ignore any slug the
	// caller provided. See keeperRequestBody comment for the why.
	if slug := strings.TrimSpace(req.AgentSlug); slug != "" && slug != s.ipc.AgentSlug {
		s.logger.Warn("keeper bridge: ignoring agent_slug in request body",
			"received_slug", slug, "canonical_slug", s.ipc.AgentSlug)
	}
	agentID := s.ipc.AgentID

	// Build the internal keeper request payload
	ipcPayload := map[string]string{
		"requesting_agent_id": agentID,
		"requesting_crew_id":  s.ipc.CrewID,
		"workspace_id":        s.ipc.WorkspaceID,
		"intent":              req.Intent,
	}
	if req.CredentialID != "" {
		ipcPayload["credential_id"] = req.CredentialID
	}
	if req.CredentialName != "" {
		ipcPayload["credential_name"] = req.CredentialName
	}
	if req.TaskID != "" {
		ipcPayload["task_id"] = req.TaskID
	}

	bodyJSON, err := json.Marshal(ipcPayload)
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "failed to serialize request"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.ipc.BaseURL+"/api/v1/internal/keeper/request", bytes.NewReader(bodyJSON))
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "failed to create IPC request"})
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Internal-Token", s.ipc.Token)

	resp, err := ipcClient.Do(httpReq)
	if err != nil {
		s.logger.Error("keeper bridge: IPC request failed", "error", err)
		writeJSONResponse(w, http.StatusBadGateway, map[string]string{
			"error": "keeper request failed — credential access denied by default",
		})
		return
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		writeJSONResponse(w, http.StatusBadGateway, map[string]string{"error": "invalid response from keeper"})
		return
	}

	writeJSONResponse(w, resp.StatusCode, result)
}

// handleKeeperExecute handles POST /keeper/execute from agents (UID 1001).
// The agent requests that a shell command be executed with a credential injected
// as an environment variable. The sidecar validates the request and forwards it
// to crewshipd, which runs the command inside the container and returns scrubbed output.
// The container_id is always taken from the IPC config — agents cannot override it.
func (s *Server) handleKeeperExecute(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{
			"error": "keeper IPC not configured",
		})
		return
	}

	var req keeperExecuteBody
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}

	req.CredentialID = strings.TrimSpace(req.CredentialID)
	req.CredentialName = strings.TrimSpace(req.CredentialName)
	req.Intent = strings.TrimSpace(req.Intent)
	req.Command = strings.TrimSpace(req.Command)

	if (req.CredentialID == "" && req.CredentialName == "") || req.Intent == "" || req.Command == "" {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{
			"error": "credential_id or credential_name, intent, and command required",
		})
		return
	}

	// Reject oversized intents and commands
	if len(req.Intent) > maxIntentLength {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{
			"error": "intent exceeds maximum allowed length",
		})
		return
	}
	if len(req.Command) > maxCommandLength {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{
			"error": "command exceeds maximum allowed length",
		})
		return
	}

	// Reject null bytes in intent or command (binary injection attempts)
	if strings.ContainsRune(req.Intent, 0) || strings.ContainsRune(req.Command, 0) {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{
			"error": "fields contain invalid characters",
		})
		return
	}

	// Reject commands with shell metacharacters that enable command chaining
	// or credential exfiltration (defense-in-depth — crewshipd also checks this).
	if containsDangerousShellChars(req.Command) {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{
			"error": "command contains disallowed shell operators",
		})
		return
	}

	// Validate credential ID format
	if req.CredentialID != "" && !credentialIDPattern.MatchString(req.CredentialID) {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{
			"error": "credential_id contains invalid characters",
		})
		return
	}

	// Identity is the canonical sidecar AgentID — ignore any slug the
	// caller provided. See keeperRequestBody comment for the why.
	if slug := strings.TrimSpace(req.AgentSlug); slug != "" && slug != s.ipc.AgentSlug {
		s.logger.Warn("keeper bridge: ignoring agent_slug in execute body",
			"received_slug", slug, "canonical_slug", s.ipc.AgentSlug)
	}
	execAgentID := s.ipc.AgentID

	// Build the IPC payload. Critically:
	// - requesting_agent_id resolved from crew members or IPC default (not the request body)
	// - container_id comes from s.ipc.ContainerID (not the request body)
	// This prevents an agent from executing commands in another agent's container.
	ipcPayload := map[string]string{
		"requesting_agent_id": execAgentID,
		"requesting_crew_id":  s.ipc.CrewID,
		"workspace_id":        s.ipc.WorkspaceID,
		"intent":              req.Intent,
		"command":             req.Command,
		"container_id":        s.ipc.ContainerID,
	}
	if req.CredentialID != "" {
		ipcPayload["credential_id"] = req.CredentialID
	}
	if req.CredentialName != "" {
		ipcPayload["credential_name"] = req.CredentialName
	}
	if req.EnvVar != "" {
		ipcPayload["env_var"] = req.EnvVar
	}
	if req.TaskID != "" {
		ipcPayload["task_id"] = req.TaskID
	}

	bodyJSON, err := json.Marshal(ipcPayload)
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "failed to serialize request"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.ipc.BaseURL+"/api/v1/internal/keeper/execute", bytes.NewReader(bodyJSON))
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "failed to create IPC request"})
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Internal-Token", s.ipc.Token)

	resp, err := ipcClient.Do(httpReq)
	if err != nil {
		s.logger.Error("keeper execute bridge: IPC request failed", "error", err)
		writeJSONResponse(w, http.StatusBadGateway, map[string]string{
			"error": "keeper execute failed — command not run",
		})
		return
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		writeJSONResponse(w, http.StatusBadGateway, map[string]string{"error": "invalid response from keeper execute"})
		return
	}

	writeJSONResponse(w, resp.StatusCode, result)
}
