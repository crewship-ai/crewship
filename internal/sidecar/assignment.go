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

// handleAssign handles POST /assign from an agent delegating work.
// It resolves the ACTING agent, validates the target slug, then forwards the
// assignment to crewshipd via the internal API so crewshipd can exec the
// sub-agent.
//
// Not "from lead agents" any more, and — the part worth stating plainly — it
// never was, in code. This handler used to check the target and the crew
// roster and nothing about the caller: no identity, not even a bearer token.
// "Only leads delegate" was true only because only a LEAD's system prompt
// carried the recipe (internal/orchestrator/lead.go), which is a fact about
// what agents are TOLD, not about what they may do (#1754).
//
// The identity resolved here is what crewshipd's delegation caps measure
// against — it finds the assignment the caller is executing and takes its depth
// — so getting it wrong is not an attribution nit: a sub-agent's dispatch filed
// under its lead reads as depth 1 on every hop, forever.
func (s *Server) handleAssign(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "assignment IPC not configured"})
		return
	}

	var req assignRequest
	if !decodeCappedJSON(w, r, &req) {
		return
	}

	// #812 identity contract, same shape as /report-confidence: a valid
	// per-agent token is authoritative, an unrecognized one is a forgery, and
	// an ABSENT one on a crew that issues tokens is a downgrade attempt — the
	// caller dropping the header to be taken for whoever booted this sidecar.
	// Only a fully token-less (legacy) deployment falls back to the boot agent.
	actorAgentID, ok := s.actingAgentID(r)
	if !ok {
		writeJSONResponse(w, http.StatusForbidden, map[string]string{
			"error": "unrecognized agent token — /assign requires the per-agent token in $CREWSHIP_AGENT_TOKEN",
		})
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
		// Who is dispatching, resolved above from the bearer token. chat_id
		// cannot answer that: the crew shares one sidecar and its IPC chat is
		// the boot agent's.
		"actor_agent_id": actorAgentID,
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
