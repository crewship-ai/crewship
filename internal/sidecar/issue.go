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
		CrewID      string `json:"crew_id"` // SECURITY: ignored — the trusted IPC crew identity is always used.
		Priority    string `json:"priority"`
		ProjectID   string `json:"project_id"`
		AssigneeID  string `json:"assignee_id"`
	}
	if !decodeCappedJSON(w, r, &req) {
		return
	}
	if req.Title == "" {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "title is required"})
		return
	}

	// SECURITY: always use the trusted IPC crew identity. A request-supplied
	// crew_id is ignored — honoring it would let a compromised agent create an
	// issue in another crew (cross-crew override). This matches the
	// keeper_bridge.go pattern of deriving crew from s.ipc.
	crewID := s.ipc.CrewID
	if crewID == "" {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "crew_id is required"})
		return
	}

	// #812: attribute authorship to the ACTING agent from its per-agent bearer
	// token, not the boot agent that started the shared sidecar. Forged token → 403.
	authorAgentID, ok := s.actingAgentID(r)
	if !ok {
		writeJSONResponse(w, http.StatusForbidden, map[string]string{"error": "unrecognized agent token"})
		return
	}

	body := map[string]interface{}{
		"workspace_id":    s.ipc.WorkspaceID,
		"crew_id":         crewID,
		"title":           req.Title,
		"assignee_type":   "agent",
		"author_agent_id": authorAgentID,
	}
	// Provenance (v108/v129): the chat this agent container is bound to.
	// A run id is not part of the IPC identity today, so only the chat is
	// forwarded; crewshipd stamps authored_via='agent_tool_call' itself.
	if s.ipc.ChatID != "" {
		body["author_chat_id"] = s.ipc.ChatID
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
