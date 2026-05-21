package sidecar

// File: coordinator.go — sidecar handlers that proxy workspace-wide
// crewshipd endpoints (crews, crew-connections, credentials, agent
// creation, issue creation). Lead and AGENT agents reach these via the
// sidecar so they don't need direct network access to the host API.
//
// Historical note: the file is named after the now-removed COORDINATOR
// role, which originally drove these endpoints. The proposal +
// missions/all proxy handlers that were COORDINATOR-only have been
// removed. Rename pending — kept as-is in v0.1 to keep the diff focused.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

// maxProxyBodyBytes caps every body the sidecar will buffer when
// forwarding a workspace-scoped POST/PATCH/PUT upstream to crewshipd.
// The sidecar runs as UID 1002 inside the agent container — the agent
// process (UID 1001) is what writes to these endpoints, and a buggy or
// adversarial agent could otherwise OOM the sidecar by streaming a
// multi-GB body. 1 MiB matches the internal/api/readJSON cap on the
// destination handler so legitimate workspace-scoped payloads (agent
// records, credential metadata) always fit comfortably.
const maxProxyBodyBytes = 1 << 20

// handleListCrews proxies GET /crews to the crewshipd internal API.
// Used by AGENT and LEAD agents to discover all workspace crews.
func (s *Server) handleListCrews(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}
	s.proxyToAPI(w, r, http.MethodGet, "/api/v1/internal/crews?workspace_id="+s.ipc.WorkspaceID)
}

// handleListCrewConnections proxies GET /crew-connections to the crewshipd API.
// Used by AGENT and LEAD agents to discover crew connection topology.
func (s *Server) handleListCrewConnections(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}
	s.proxyToAPI(w, r, http.MethodGet, "/api/v1/internal/crew-connections?workspace_id="+s.ipc.WorkspaceID)
}

// COORDINATOR-only proposal + cross-mission proxy handlers were removed in
// v0.1 (handleCreateProposal, handleListProposals, handleListAllMissions,
// handleAllMissionsSummary). The matching public + internal API routes are
// also gone — see internal/api/proposals.go deletion. v0.2 will replace
// the proposal flow with a crew-to-crew handoff primitive; recover the
// reference implementation from git history at that point.

// handleListCredentials proxies GET /credentials to the crewshipd internal API.
// Used by AGENT and LEAD agents to discover available credentials for assignment.
//
// Post-Patch-A crewshipd already returns metadata only by default, but we
// also strip any access_token / refresh_token that survives a future
// crewshipd regression — defense in depth: an agent inside the container
// never sees plaintext via this path even if the upstream handler
// accidentally re-introduces the field. The sidecar's stdin boot payload
// (orchestrator/exec_sidecar.go) is the ONLY supply line for cleartext
// values, and that path goes straight to the credStore — never to the
// agent over HTTP.
func (s *Server) handleListCredentials(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}
	s.proxyToAPIFiltered(
		w, r,
		http.MethodGet,
		"/api/v1/internal/credentials?workspace_id="+s.ipc.WorkspaceID,
		stripCredentialValues,
	)
}

// stripCredentialValues removes plaintext token fields from a credentials
// list response before it goes back to the agent. Operates on a generic
// []map[string]any so a future crewshipd schema change that adds new
// secret-looking fields doesn't silently bypass the filter.
//
// Fields scrubbed (case-sensitive, JSON tag names): access_token,
// refresh_token, encrypted_value, encrypted_refresh_token, token,
// secret. Both top-level and nested under arbitrary maps.
func stripCredentialValues(raw json.RawMessage) json.RawMessage {
	// Try array shape first (the canonical /credentials response).
	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err == nil {
		for _, row := range arr {
			scrubSecretKeys(row)
		}
		out, _ := json.Marshal(arr)
		return out
	}
	// Fall through: try object shape (error envelopes etc.). If we can't
	// parse it at all, pass it through unchanged — wrapper errors carry
	// no credential data.
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err == nil {
		scrubSecretKeys(obj)
		out, _ := json.Marshal(obj)
		return out
	}
	return raw
}

// secretKeyDenylist is the closed set of JSON keys the sidecar will refuse
// to relay back to the agent. Conservative — agent-visible metadata
// (id, name, type, provider, status, created_at, etc.) is NOT in the list.
var secretKeyDenylist = map[string]struct{}{
	"access_token":            {},
	"refresh_token":           {},
	"encrypted_value":         {},
	"encrypted_refresh_token": {},
	"token":                   {},
	"secret":                  {},
	"plain_value":             {},
}

func scrubSecretKeys(m map[string]any) {
	for k, v := range m {
		if _, banned := secretKeyDenylist[k]; banned {
			delete(m, k)
			continue
		}
		switch nested := v.(type) {
		case map[string]any:
			scrubSecretKeys(nested)
		case []any:
			for _, item := range nested {
				if im, ok := item.(map[string]any); ok {
					scrubSecretKeys(im)
				}
			}
		}
	}
}

// handleAssignAgentCredential proxies POST /agent-credentials to the crewshipd internal API.
// Used by LEAD agents to assign a credential to a newly created agent.
func (s *Server) handleAssignAgentCredential(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}
	s.proxyToAPI(w, r, http.MethodPost, "/api/v1/internal/agent-credentials?workspace_id="+s.ipc.WorkspaceID)
}

// handleCreateCrewConnection proxies POST /crew-connections to the crewshipd internal API.
// Used by LEAD agents to connect a new crew to existing ones.
func (s *Server) handleCreateCrewConnection(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}
	s.proxyToAPI(w, r, http.MethodPost, "/api/v1/internal/crew-connections?workspace_id="+s.ipc.WorkspaceID)
}

// handleCreateCrew proxies POST /crew/create to the crewshipd internal API.
// Allows LEAD agents to create new crews in the workspace.
func (s *Server) handleCreateCrew(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}
	s.proxyToAPI(w, r, http.MethodPost, "/api/v1/internal/crews?workspace_id="+s.ipc.WorkspaceID)
}

// handleCreateAgent proxies POST /agent/create to the crewshipd internal API.
// Allows LEAD agents to create new agents within a crew.
func (s *Server) handleCreateAgent(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}
	s.proxyToAPI(w, r, http.MethodPost, "/api/v1/internal/agents?workspace_id="+s.ipc.WorkspaceID)
}

// proxyToAPI is a generic helper that proxies a request to the crewshipd internal API.
func (s *Server) proxyToAPI(w http.ResponseWriter, r *http.Request, method, path string) {
	s.proxyToAPIFiltered(w, r, method, path, nil)
}

// proxyToAPIFiltered is the same as proxyToAPI but applies a transform to
// the response body before relaying it back to the agent. Used by
// handleListCredentials to strip plaintext token fields even if a future
// crewshipd handler regresses and emits them.
func (s *Server) proxyToAPIFiltered(
	w http.ResponseWriter,
	r *http.Request,
	method, path string,
	transform func(json.RawMessage) json.RawMessage,
) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	var bodyReader io.Reader
	if r.Body != nil && (method == http.MethodPost || method == http.MethodPatch || method == http.MethodPut) {
		// MaxBytesReader closes the underlying body on overflow and
		// returns a sentinel *http.MaxBytesError on the next Read.
		limited := http.MaxBytesReader(w, r.Body, maxProxyBodyBytes)
		bodyBytes, err := io.ReadAll(limited)
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				writeJSONResponse(w, http.StatusRequestEntityTooLarge,
					map[string]string{"error": "request body exceeds sidecar limit"})
				return
			}
			writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "failed to read body"})
			return
		}
		bodyReader = bytes.NewReader(bodyBytes)
	}

	req, err := http.NewRequestWithContext(ctx, method, s.ipc.BaseURL+path, bodyReader)
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "failed to create request"})
		return
	}
	req.Header.Set("X-Internal-Token", s.ipc.Token)
	if bodyReader != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := ipcClient.Do(req)
	if err != nil {
		writeJSONResponse(w, http.StatusBadGateway, map[string]string{"error": "crewshipd request failed"})
		return
	}
	defer resp.Body.Close()

	var result json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		writeJSONResponse(w, http.StatusBadGateway, map[string]string{"error": "invalid response"})
		return
	}

	if transform != nil {
		result = transform(result)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(result)
}
