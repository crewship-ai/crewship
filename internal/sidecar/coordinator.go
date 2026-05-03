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
	"io"
	"net/http"
	"time"
)

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
func (s *Server) handleListCredentials(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}
	s.proxyToAPI(w, r, http.MethodGet, "/api/v1/internal/credentials?workspace_id="+s.ipc.WorkspaceID)
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
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	var bodyReader io.Reader
	if r.Body != nil && (method == http.MethodPost || method == http.MethodPatch || method == http.MethodPut) {
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
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

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(result)
}
