package sidecar

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ipcClient is used for all IPC HTTP calls with a reasonable timeout
// to prevent indefinite blocking if crewshipd hangs.
var ipcClient = &http.Client{Timeout: 30 * time.Second}

type assignRequest struct {
	Target string `json:"target"`
	Task   string `json:"task"`
	// Crew names the target's crew by slug. Empty (the common case) means
	// this crew. A different crew is only dispatchable when the two are
	// linked — crewshipd checks that, and the workspace binding on the
	// crew id, on every request; the sidecar just stops pretending the
	// caller's own crew is the only possible destination.
	Crew string `json:"crew,omitempty"`
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
	if !decodeCappedJSON(w, r, &req) {
		return
	}
	if req.Target == "" || req.Task == "" {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "target and task required"})
		return
	}

	// Which crew is the target in? Unnamed — or named as our own — means the
	// local roster, which the sidecar can and must check itself: it holds the
	// member list, and a typo should fail here rather than after a round trip.
	targetCrewID := s.ipc.CrewID
	if req.Crew != "" {
		resolved, err := s.resolveCrewIDBySlug(r.Context(), req.Crew)
		if err != nil {
			writeJSONResponse(w, http.StatusBadGateway, map[string]string{"error": "failed to resolve target crew"})
			return
		}
		if resolved == "" {
			writeJSONResponse(w, http.StatusNotFound, map[string]string{
				"error": fmt.Sprintf("crew %q not found", req.Crew),
			})
			return
		}
		targetCrewID = resolved
	}

	// Membership is only ours to judge for our own crew. For another crew the
	// roster lives elsewhere, so crewshipd resolves the agent — and refuses
	// the whole dispatch unless the two crews are linked.
	if targetCrewID == s.ipc.CrewID {
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
	}

	body := map[string]string{
		"target_slug":  req.Target,
		"task":         req.Task,
		"crew_id":      targetCrewID,
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
	// Reject any character that could smuggle a query string or extra path
	// segment into the IPC URL (#1040) — otherwise the trusted ?workspace_id=
	// appended below could be overridden via the same %3F path-injection trick,
	// defeating the workspace scope. CUID assignment ids never contain these.
	if assignmentID == "" || strings.ContainsAny(assignmentID, "/?#&=%") || strings.Contains(assignmentID, "..") {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "assignment_id required"})
		return
	}

	// Scope the internal read to this sidecar's trusted workspace (#1040): the
	// internal AssignmentHandler.Get now requires workspace_id and filters on
	// it, closing the cross-workspace IDOR on the assignment row.
	q := url.Values{}
	q.Set("workspace_id", s.ipc.WorkspaceID)
	s.proxyIPCJSON(w, r, http.MethodGet, "/api/v1/internal/assignments/"+url.PathEscape(assignmentID)+"?"+q.Encode(), "results", nil)
}
