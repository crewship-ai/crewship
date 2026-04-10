package sidecar

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ipcClient is used for all IPC HTTP calls with a reasonable timeout
// to prevent indefinite blocking if crewshipd hangs.
var ipcClient = &http.Client{Timeout: 30 * time.Second}

type assignRequest struct {
	Target string `json:"target"`
	Task   string `json:"task"`
}

// handleAssign handles POST /assign from lead agents.
// It validates the target slug, then forwards the assignment to crewshipd
// via the internal API so crewshipd can exec the sub-agent.
func (s *Server) handleAssign(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "assignment IPC not configured"})
		return
	}

	var req assignRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if req.Target == "" || req.Task == "" {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "target and task required"})
		return
	}

	// Validate that the target is a known crew member slug
	found := false
	for _, m := range s.crewMembers {
		if m.Slug == req.Target {
			found = true
			break
		}
	}
	if !found {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("target %q not found in crew", req.Target),
		})
		return
	}

	body := map[string]string{
		"target_slug":  req.Target,
		"task":         req.Task,
		"crew_id":      s.ipc.CrewID,
		"workspace_id": s.ipc.WorkspaceID,
		"chat_id":      s.ipc.ChatID,
	}
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "failed to serialize request"})
		return
	}

	s.proxyIPCJSON(w, r, http.MethodPost, "/api/v1/internal/assignments", "assignment", bodyJSON)
}

// handleResults handles GET /results/{assignment_id} from lead agents.
// It proxies the request to the crewshipd internal API to retrieve assignment status and output.
func (s *Server) handleResults(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "assignment IPC not configured"})
		return
	}

	assignmentID := strings.TrimPrefix(r.URL.Path, "/results/")
	if assignmentID == "" || strings.Contains(assignmentID, "/") || strings.Contains(assignmentID, "..") {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "assignment_id required"})
		return
	}

	s.proxyIPCJSON(w, r, http.MethodGet, "/api/v1/internal/assignments/"+assignmentID, "results", nil)
}
