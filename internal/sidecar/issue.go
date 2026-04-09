package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
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

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.ipc.BaseURL+"/api/v1/internal/issues", bytes.NewReader(bodyJSON))
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "failed to create request"})
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Internal-Token", s.ipc.Token)

	resp, err := ipcClient.Do(httpReq)
	if err != nil {
		writeJSONResponse(w, http.StatusBadGateway, map[string]string{
			"error": fmt.Sprintf("issue create request failed: %v", err),
		})
		return
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		writeJSONResponse(w, http.StatusBadGateway, map[string]string{"error": "invalid response from crewshipd"})
		return
	}

	writeJSONResponse(w, resp.StatusCode, result)
}
