package sidecar

import (
	"encoding/json"
	"net/http"
)

// handleIssueCreate handles POST /issue/create from agents.
// It forwards the request to crewshipd's internal API to create an issue.
func (s *Server) handleIssueCreate(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}

	var req struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		CrewID      string `json:"crew_id"`
		Priority    string `json:"priority"`
		ProjectID   string `json:"project_id"`
		AssigneeID  string `json:"assignee_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if req.Title == "" {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "title is required"})
		return
	}

	// Default crew_id to the sidecar's crew if not specified
	crewID := req.CrewID
	if crewID == "" {
		crewID = s.ipc.CrewID
	}
	if crewID == "" {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "crew_id is required"})
		return
	}

	body := map[string]interface{}{
		"workspace_id":  s.ipc.WorkspaceID,
		"crew_id":       crewID,
		"title":         req.Title,
		"assignee_type": "agent",
	}
	if req.Description != "" {
		body["description"] = req.Description
	}
	if req.Priority != "" {
		body["priority"] = req.Priority
	}
	if req.ProjectID != "" {
		body["project_id"] = req.ProjectID
	}
	if req.AssigneeID != "" {
		body["assignee_id"] = req.AssigneeID
	}

	bodyJSON, err := json.Marshal(body)
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "failed to serialize request"})
		return
	}

	s.proxyIPCJSON(w, r, http.MethodPost, "/api/v1/internal/issues", "issue create", bodyJSON)
}
