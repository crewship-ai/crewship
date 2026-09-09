package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

// Work keeps the sidecar's trusted actor and crew boundary, then enters the
// same revision-checked transaction as the human work surface.
func (h *InternalIssueHandler) Work(w http.ResponseWriter, r *http.Request) {
	var body map[string]json.RawMessage
	if err := readJSON(r, &body); err != nil {
		writeProblem(w, r, 400, "Invalid JSON body")
		return
	}
	var wsID, agentID string
	_ = json.Unmarshal(body["workspace_id"], &wsID)
	_ = json.Unmarshal(body["agent_id"], &agentID)
	if wsID == "" || agentID == "" {
		writeProblem(w, r, 400, "workspace_id and agent_id are required")
		return
	}
	if !assertInternalTokenWorkspace(w, r, wsID) || !h.assertAuthorAgentInWorkspace(w, r, wsID, agentID) {
		return
	}
	var crewID string
	if err := h.db.QueryRowContext(r.Context(), `SELECT crew_id FROM missions WHERE identifier=? AND workspace_id=?`, r.PathValue("identifier"), wsID).Scan(&crewID); err != nil {
		writeProblem(w, r, 404, "Issue not found")
		return
	}
	if !assertBoundCrewWorkspaceDB(w, r, h.db, h.logger, &crewID) {
		return
	}
	encoded, _ := json.Marshal(body)
	r.Body = io.NopCloser(bytes.NewReader(encoded))
	r.SetPathValue("crewId", crewID)
	issues := NewIssueHandler(h.db, h.hub, nil, h.logger)
	issues.workAs(w, r, "agent", agentID, wsID)
}
