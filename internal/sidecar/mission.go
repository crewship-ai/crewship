package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/orchestrator"
)

type missionCreateRequest struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Plan        string `json:"plan,omitempty"`
	CrewID      string `json:"crew_id,omitempty"`      // Required for COORDINATOR (no default crew)
	Tasks       []struct {
		Title           string   `json:"title"`
		Description     string   `json:"description,omitempty"`
		AssignedTo      string   `json:"assigned_to"`       // Slug (same crew only)
		AssignedToID    string   `json:"assigned_to_id"`    // Agent ID (cross-crew)
		TaskOrder       int      `json:"task_order"`
		DependsOn       []string `json:"depends_on,omitempty"`
		MaxIterations   *int     `json:"max_iterations,omitempty"`
	} `json:"tasks,omitempty"`
}

// handleMissionCreate handles POST /mission/create from lead agents.
// Creates a mission with optional tasks, then optionally starts it.
func (s *Server) handleMissionCreate(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}

	var req missionCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if req.Title == "" {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "title is required"})
		return
	}

	// Resolve assigned_to slugs to agent IDs
	memberMap := make(map[string]CrewMember)
	for _, m := range s.crewMembers {
		memberMap[m.Slug] = m
	}

	type internalTask struct {
		Title           string   `json:"title"`
		Description     *string  `json:"description,omitempty"`
		AssignedAgentID *string  `json:"assigned_agent_id,omitempty"`
		TaskOrder       int      `json:"task_order"`
		DependsOn       []string `json:"depends_on,omitempty"`
		MaxIterations   *int     `json:"max_iterations,omitempty"`
	}

	var tasks []internalTask
	for _, t := range req.Tasks {
		it := internalTask{
			Title:     t.Title,
			TaskOrder: t.TaskOrder,
			DependsOn: t.DependsOn,
			MaxIterations: t.MaxIterations,
		}
		if t.Description != "" {
			it.Description = &t.Description
		}
		// Support both slug-based (same crew) and ID-based (cross-crew) assignment
		if t.AssignedToID != "" {
			it.AssignedAgentID = &t.AssignedToID
		} else if t.AssignedTo != "" {
			if m, ok := memberMap[t.AssignedTo]; ok {
				it.AssignedAgentID = &m.ID
			} else {
				writeJSONResponse(w, http.StatusBadRequest, map[string]string{
					"error": fmt.Sprintf("agent %q not found in crew", t.AssignedTo),
				})
				return
			}
		}
		tasks = append(tasks, it)
	}

	// Use explicit crew_id if provided (COORDINATOR), otherwise use sidecar's crew
	crewID := s.ipc.CrewID
	if req.CrewID != "" {
		crewID = req.CrewID
	}
	if crewID == "" {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{
			"error": "crew_id required (COORDINATOR agents must specify crew_id explicitly)",
		})
		return
	}

	body := map[string]interface{}{
		"title":         req.Title,
		"lead_agent_id": s.ipc.AgentID,
		"crew_id":       crewID,
		"workspace_id":  s.ipc.WorkspaceID,
		"tasks":         tasks,
	}
	if req.Description != "" {
		body["description"] = req.Description
	}
	if req.Plan != "" {
		body["plan"] = req.Plan
	}

	bodyJSON, err := json.Marshal(body)
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "failed to serialize request"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.ipc.BaseURL+"/api/v1/internal/missions", bytes.NewReader(bodyJSON))
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "failed to create request"})
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Internal-Token", s.ipc.Token)

	resp, err := ipcClient.Do(httpReq)
	if err != nil {
		writeJSONResponse(w, http.StatusBadGateway, map[string]string{
			"error": fmt.Sprintf("mission create request failed: %v", err),
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

// handleMissionStart handles POST /mission/{missionId}/start
// Transitions a PLANNING mission to IN_PROGRESS.
func (s *Server) handleMissionStart(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}

	missionID := strings.TrimPrefix(r.URL.Path, "/mission/")
	missionID = strings.TrimSuffix(missionID, "/start")
	if missionID == "" || strings.Contains(missionID, "/") {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "mission_id required"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.ipc.BaseURL+"/api/v1/internal/missions/"+missionID+"/start", nil)
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "failed to create request"})
		return
	}
	httpReq.Header.Set("X-Internal-Token", s.ipc.Token)

	resp, err := ipcClient.Do(httpReq)
	if err != nil {
		writeJSONResponse(w, http.StatusBadGateway, map[string]string{
			"error": fmt.Sprintf("mission start request failed: %v", err),
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

// handleMissionStatus handles GET /mission/{missionId}
// Returns mission details and task statuses.
func (s *Server) handleMissionStatus(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}

	missionID := strings.TrimPrefix(r.URL.Path, "/mission/")
	if missionID == "" || strings.Contains(missionID, "/") {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "mission_id required"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		s.ipc.BaseURL+"/api/v1/internal/missions/"+missionID, nil)
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "failed to create request"})
		return
	}
	httpReq.Header.Set("X-Internal-Token", s.ipc.Token)

	resp, err := ipcClient.Do(httpReq)
	if err != nil {
		writeJSONResponse(w, http.StatusBadGateway, map[string]string{
			"error": fmt.Sprintf("mission status request failed: %v", err),
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

// handleMissionTemplates handles GET /mission/templates
// Returns available workflow templates that lead agents can use.
func (s *Server) handleMissionTemplates(w http.ResponseWriter, _ *http.Request) {
	writeJSONResponse(w, http.StatusOK, orchestrator.ListTemplates())
}
