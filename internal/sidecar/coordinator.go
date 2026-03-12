package sidecar

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// handleListCrews proxies GET /crews to the crewshipd internal API.
// Used by COORDINATOR agents to discover all workspace crews.
func (s *Server) handleListCrews(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	url := s.ipc.BaseURL + "/api/v1/internal/crews?workspace_id=" + s.ipc.WorkspaceID
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

	var result json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		writeJSONResponse(w, http.StatusBadGateway, map[string]string{"error": "invalid response"})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(result)
}

// handleListCrewConnections proxies GET /crew-connections to the crewshipd API.
// Used by COORDINATOR agents to discover crew connection topology.
func (s *Server) handleListCrewConnections(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	url := s.ipc.BaseURL + "/api/v1/internal/crew-connections?workspace_id=" + s.ipc.WorkspaceID
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

	var result json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		writeJSONResponse(w, http.StatusBadGateway, map[string]string{"error": "invalid response"})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(result)
}
