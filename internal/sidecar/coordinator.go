package sidecar

// File: coordinator.go — sidecar handlers used by the deprecated COORDINATOR
// agent role to reach workspace-wide crewshipd endpoints.
//
// DEPRECATED (2026-04-16): COORDINATOR role is no longer actively developed.
// The role has no autonomous behavior — it activates only when a user
// explicitly chats with a coordinator agent or includes it in a mission.
// Its workspace-level context has ~98% overlap with normal AGENT role.
// New cross-crew orchestration should use the scheduler pattern with AGENT
// role or rely on external MCP clients for workspace queries.
// See docs/guides/coordinator.mdx for migration notes.
// Handlers retained for backward compatibility.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

// handleListCrews proxies GET /crews to the crewshipd internal API.
// Used by COORDINATOR agents to discover all workspace crews.
// Deprecated: see file-level notice.
func (s *Server) handleListCrews(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}
	s.proxyToAPI(w, r, http.MethodGet, "/api/v1/internal/crews?workspace_id="+s.ipc.WorkspaceID)
}

// handleListCrewConnections proxies GET /crew-connections to the crewshipd API.
// Used by COORDINATOR agents to discover crew connection topology.
func (s *Server) handleListCrewConnections(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}
	s.proxyToAPI(w, r, http.MethodGet, "/api/v1/internal/crew-connections?workspace_id="+s.ipc.WorkspaceID)
}

// handleCreateProposal proxies POST /proposal to the crewshipd API.
// Used by COORDINATOR agents to submit mission proposals for human review.
func (s *Server) handleCreateProposal(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}
	s.proxyToAPI(w, r, http.MethodPost, "/api/v1/internal/mission-proposals?workspace_id="+s.ipc.WorkspaceID)
}

// handleListProposals proxies GET /proposals to the crewshipd API.
func (s *Server) handleListProposals(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}
	s.proxyToAPI(w, r, http.MethodGet, "/api/v1/internal/mission-proposals?workspace_id="+s.ipc.WorkspaceID)
}

// handleListAllMissions proxies GET /missions/all to the crewshipd API.
func (s *Server) handleListAllMissions(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}
	s.proxyToAPI(w, r, http.MethodGet, "/api/v1/missions?workspace_id="+s.ipc.WorkspaceID)
}

// handleAllMissionsSummary returns an aggregated status summary of all workspace missions.
func (s *Server) handleAllMissionsSummary(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	url := s.ipc.BaseURL + "/api/v1/missions?workspace_id=" + s.ipc.WorkspaceID + "&limit=100"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "failed to create request"})
		return
	}
	req.Header.Set("X-Internal-Token", s.ipc.Token)

	resp, err := ipcClient.Do(req)
	if err != nil {
		writeJSONResponse(w, http.StatusBadGateway, map[string]string{"error": "crewshipd request failed"})
		return
	}
	defer resp.Body.Close()

	var missions []struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&missions); err != nil {
		writeJSONResponse(w, http.StatusBadGateway, map[string]string{"error": "invalid response"})
		return
	}

	summary := map[string]int{
		"total": len(missions), "planning": 0, "in_progress": 0,
		"review": 0, "completed": 0, "failed": 0, "cancelled": 0,
	}
	for _, m := range missions {
		switch m.Status {
		case "PLANNING":
			summary["planning"]++
		case "IN_PROGRESS":
			summary["in_progress"]++
		case "REVIEW":
			summary["review"]++
		case "COMPLETED":
			summary["completed"]++
		case "FAILED":
			summary["failed"]++
		case "CANCELLED":
			summary["cancelled"]++
		}
	}
	writeJSONResponse(w, http.StatusOK, summary)
}

// handleListCredentials proxies GET /credentials to the crewshipd internal API.
// Used by COORDINATOR agents to discover available credentials for assignment.
func (s *Server) handleListCredentials(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}
	s.proxyToAPI(w, r, http.MethodGet, "/api/v1/internal/credentials?workspace_id="+s.ipc.WorkspaceID)
}

// handleAssignAgentCredential proxies POST /agent-credentials to the crewshipd internal API.
// Used by COORDINATOR agents to assign a credential to a newly created agent.
func (s *Server) handleAssignAgentCredential(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}
	s.proxyToAPI(w, r, http.MethodPost, "/api/v1/internal/agent-credentials?workspace_id="+s.ipc.WorkspaceID)
}

// handleCreateCrewConnection proxies POST /crew-connections to the crewshipd internal API.
// Used by COORDINATOR agents to connect a new crew to existing ones.
func (s *Server) handleCreateCrewConnection(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}
	s.proxyToAPI(w, r, http.MethodPost, "/api/v1/internal/crew-connections?workspace_id="+s.ipc.WorkspaceID)
}

// handleCreateCrew proxies POST /crew/create to the crewshipd internal API.
// Allows COORDINATOR agents to create new crews in the workspace.
func (s *Server) handleCreateCrew(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}
	s.proxyToAPI(w, r, http.MethodPost, "/api/v1/internal/crews?workspace_id="+s.ipc.WorkspaceID)
}

// handleCreateAgent proxies POST /agent/create to the crewshipd internal API.
// Allows COORDINATOR agents to create new agents within a crew.
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
