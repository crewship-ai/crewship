package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	neturl "net/url"
	"os"
	"strconv"
	"time"
)

// queryClient uses a 130s timeout (10s buffer over the 120s context timeout in handleQuery).
// The context deadline fires first for clean cancellation; the client timeout is a safety net.
var queryClient = &http.Client{Timeout: 130 * time.Second}

// escalateWaitClient uses a 310s timeout (10s buffer over the 300s context timeout in handleEscalate wait).
// The agent blocks while waiting for human response to the escalation.
// Shares transport with ipcClient for consistency (both talk to crewshipd over HTTP).
var escalateWaitClient = &http.Client{
	Transport: ipcClient.Transport,
	Timeout:   310 * time.Second,
}

type queryRequest struct {
	Target   string `json:"target"`
	Question string `json:"question"`
	From     string `json:"from"`
	Depth    int    `json:"depth"`
}

type escalateRequest struct {
	From         string `json:"from"`
	Reason       string `json:"reason"`
	Context      string `json:"context"`
	Type         string `json:"type"`
	Metadata     string `json:"metadata"`
	EvidencePack string `json:"evidence_pack"`
}

// isCrewMember reports whether slug names an agent in this crew. It is the
// membership check both peer-query and escalation use to validate a
// caller-supplied `from`/target slug (mirrors memory_mcp.go's CRE-137 check) —
// the shared per-crew sidecar can't cryptographically bind identity, so it at
// least refuses attribution to a slug outside the crew.
func (s *Server) isCrewMember(slug string) bool {
	for _, m := range s.crewMembers {
		if m.Slug == slug {
			return true
		}
	}
	return false
}

// handleQuery handles POST /query from agents wanting to ask a peer a question.
// The query is synchronous — the caller blocks until the target agent responds.
func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}

	var req queryRequest
	if !decodeCappedJSON(w, r, &req) {
		return
	}
	if req.Target == "" || req.Question == "" {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "target and question required"})
		return
	}

	// Determine current depth from request or env var
	depth := req.Depth
	if depth == 0 {
		if envDepth := os.Getenv("CREWSHIP_QUERY_DEPTH"); envDepth != "" {
			if d, err := strconv.Atoi(envDepth); err == nil {
				depth = d
			}
		}
	}

	// Anti-loop: reject if depth >= 2
	if depth >= 2 {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{
			"error": "query depth limit reached (max 2), cannot query further",
		})
		return
	}

	// Validate that the target is a known crew member slug
	if !s.isCrewMember(req.Target) {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("target %q not found in crew", req.Target),
		})
		return
	}

	// #812: attribute the query to the ACTING agent derived from its per-agent
	// bearer token. A valid token overrides any `from` in the body (closing
	// intra-crew impersonation); a token matching no member is refused. With no
	// token we fall back to the #796 membership-validated `from`.
	tokenAuthed := false
	if actorID, actorSlug, present, ok := s.actingIdentity(r); present {
		if !ok {
			writeJSONResponse(w, http.StatusForbidden, map[string]string{"error": "unrecognized agent token"})
			return
		}
		if req.From != "" && req.From != actorSlug {
			s.logger.Warn("query: ignoring spoofed from-slug, attributing to authenticated agent",
				"claimed_from", req.From, "acting_slug", actorSlug, "acting_agent_id", actorID)
		}
		req.From = actorSlug
		tokenAuthed = true
	}
	if !tokenAuthed {
		if s.tokensProvisioned() {
			// Per-agent tokens are in force for this crew; a token-less request
			// is a downgrade/impersonation attempt (a sibling omitting the
			// Authorization header to fall through to the spoofable membership
			// check). Refuse — do NOT accept a caller-supplied `from`.
			writeJSONResponse(w, http.StatusForbidden, map[string]string{
				"error": "per-agent token required",
			})
			return
		}
		if !s.isCrewMember(req.From) {
			writeJSONResponse(w, http.StatusBadRequest, map[string]string{
				"error": fmt.Sprintf("from %q is not a member of this crew", req.From),
			})
			return
		}
	}
	// Build IPC request body
	body := map[string]interface{}{
		"target_slug":  req.Target,
		"question":     req.Question,
		"from_slug":    req.From,
		"crew_id":      s.ipc.CrewID,
		"workspace_id": s.ipc.WorkspaceID,
		"chat_id":      s.ipc.ChatID,
		"depth":        depth,
	}
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "failed to serialize request"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.ipc.BaseURL+"/api/v1/internal/queries", bytes.NewReader(bodyJSON))
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "failed to create request"})
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Internal-Token", s.ipc.Token)

	resp, err := queryClient.Do(httpReq)
	if err != nil {
		writeJSONResponse(w, http.StatusBadGateway, map[string]string{
			"error": fmt.Sprintf("query request failed: %v", err),
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

// handleStandup handles GET /standup from lead agents.
// It proxies the request to crewshipd internal API to get crew standup summary.
func (s *Server) handleStandup(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}

	since := r.URL.Query().Get("since")

	standupPath := "/api/v1/internal/standup?crew_id=" + neturl.QueryEscape(s.ipc.CrewID)
	if since != "" {
		standupPath += "&since=" + neturl.QueryEscape(since)
	}

	s.proxyIPCJSON(w, r, http.MethodGet, standupPath, "standup", nil)
}

// handleEscalate handles POST /escalate from agents wanting to flag something for the lead.
// The call blocks until a human responds (up to 5 minutes) or times out.
func (s *Server) handleEscalate(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}

	var req escalateRequest
	if !decodeCappedJSON(w, r, &req) {
		return
	}

	// #812: derive the ACTING agent from the per-agent bearer token. A valid
	// token is authoritative — it overrides any `from` in the body, closing
	// the intra-crew impersonation that #796's membership check couldn't (a
	// spoofed `from` naming a real sibling passed that check). A token that
	// matches no crew member is a forgery and is refused. With no token we
	// fall back to #796's membership-validated `from` (advisory attribution),
	// so legacy callers keep working.
	tokenAuthed := false
	if actorID, actorSlug, present, ok := s.actingIdentity(r); present {
		if !ok {
			writeJSONResponse(w, http.StatusForbidden, map[string]string{"error": "unrecognized agent token"})
			return
		}
		if req.From != "" && req.From != actorSlug {
			s.logger.Warn("escalate: ignoring spoofed from-slug, attributing to authenticated agent",
				"claimed_from", req.From, "acting_slug", actorSlug, "acting_agent_id", actorID)
		}
		req.From = actorSlug
		tokenAuthed = true
	}

	if req.From == "" || req.Reason == "" {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "from and reason required"})
		return
	}

	// If evidence_pack is provided and metadata is empty, forward it as metadata.
	metadata := req.Metadata
	if metadata == "" && req.EvidencePack != "" {
		metadata = req.EvidencePack
	}

	// Fallback attribution (no per-agent token): validate the caller-supplied
	// `from` is a real member of THIS crew (mirrors CRE-137's /mcp/memory/<slug>
	// membership check). The per-crew sidecar is SHARED across the crew's
	// agents, so s.ipc.AgentSlug is only the agent that booted it — overriding
	// with it would mis-attribute every sibling agent's escalation (and any
	// credential proposal it carries) to the boot agent. Validating membership
	// rejects a slug outside the crew while keeping correct per-agent
	// attribution.
	if !tokenAuthed {
		if s.tokensProvisioned() {
			// Per-agent tokens are in force for this crew; a token-less request
			// is a downgrade/impersonation attempt (a sibling omitting the
			// Authorization header to fall through to the spoofable membership
			// check). Refuse — do NOT accept a caller-supplied `from`.
			writeJSONResponse(w, http.StatusForbidden, map[string]string{
				"error": "per-agent token required",
			})
			return
		}
		if !s.isCrewMember(req.From) {
			writeJSONResponse(w, http.StatusBadRequest, map[string]string{
				"error": fmt.Sprintf("from %q is not a member of this crew", req.From),
			})
			return
		}
	}
	body := map[string]string{
		"from_slug":    req.From,
		"reason":       req.Reason,
		"context":      req.Context,
		"type":         req.Type,
		"metadata":     metadata,
		"crew_id":      s.ipc.CrewID,
		"workspace_id": s.ipc.WorkspaceID,
		"chat_id":      s.ipc.ChatID,
	}
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "failed to serialize request"})
		return
	}

	// Step 1: Create the escalation (short timeout).
	createCtx, createCancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer createCancel()

	httpReq, err := http.NewRequestWithContext(createCtx, http.MethodPost,
		s.ipc.BaseURL+"/api/v1/internal/escalations", bytes.NewReader(bodyJSON))
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "failed to create request"})
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Internal-Token", s.ipc.Token)

	resp, err := ipcClient.Do(httpReq)
	if err != nil {
		writeJSONResponse(w, http.StatusBadGateway, map[string]string{
			"error": fmt.Sprintf("escalation request failed: %v", err),
		})
		return
	}
	defer resp.Body.Close()

	var createResult map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&createResult); err != nil {
		writeJSONResponse(w, http.StatusBadGateway, map[string]string{"error": "invalid response from crewshipd"})
		return
	}

	if resp.StatusCode != http.StatusCreated {
		writeJSONResponse(w, resp.StatusCode, createResult)
		return
	}

	escalationID, _ := createResult["escalation_id"].(string)
	if escalationID == "" {
		writeJSONResponse(w, http.StatusBadGateway, map[string]string{"error": "missing escalation_id in response"})
		return
	}

	// Step 2: Long-poll for human response (5 minute timeout).
	waitCtx, waitCancel := context.WithTimeout(r.Context(), 300*time.Second)
	defer waitCancel()

	waitURL := fmt.Sprintf("%s/api/v1/internal/escalations/%s/wait", s.ipc.BaseURL, neturl.PathEscape(escalationID))
	waitReq, err := http.NewRequestWithContext(waitCtx, http.MethodGet, waitURL, nil)
	if err != nil {
		// Return the create result if we can't build the wait request.
		writeJSONResponse(w, http.StatusCreated, createResult)
		return
	}
	waitReq.Header.Set("X-Internal-Token", s.ipc.Token)

	waitResp, err := escalateWaitClient.Do(waitReq)
	if err != nil {
		// Timeout or connection error — return TIMEOUT status to agent.
		writeJSONResponse(w, http.StatusOK, map[string]interface{}{
			"escalation_id": escalationID,
			"status":        "TIMEOUT",
			"resolution":    "",
		})
		return
	}
	defer waitResp.Body.Close()

	// Non-200 response (e.g. 408 timeout, 404, 500) — treat as timeout.
	if waitResp.StatusCode != http.StatusOK {
		writeJSONResponse(w, http.StatusOK, map[string]interface{}{
			"escalation_id": escalationID,
			"status":        "TIMEOUT",
			"resolution":    "",
		})
		return
	}

	var waitResult map[string]interface{}
	if err := json.NewDecoder(waitResp.Body).Decode(&waitResult); err != nil {
		writeJSONResponse(w, http.StatusOK, map[string]interface{}{
			"escalation_id": escalationID,
			"status":        "TIMEOUT",
			"resolution":    "",
		})
		return
	}

	// Merge escalation_id into the response.
	waitResult["escalation_id"] = escalationID

	writeJSONResponse(w, http.StatusOK, waitResult)
}

// handleReportConfidence handles POST /report-confidence — agent reports mid-task confidence.
func (s *Server) handleReportConfidence(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}

	var req struct {
		Confidence float64 `json:"confidence"`
		Reason     string  `json:"reason"`
	}
	if !decodeCappedJSON(w, r, &req) {
		return
	}

	// #812: attribute the confidence report to the ACTING agent (per-agent
	// token), not the boot agent of the shared sidecar. Forged token → 403.
	agentID, ok := s.actingAgentID(r)
	if !ok {
		writeJSONResponse(w, http.StatusForbidden, map[string]string{"error": "unrecognized agent token"})
		return
	}
	body := map[string]interface{}{
		"agent_id":     agentID,
		"crew_id":      s.ipc.CrewID,
		"workspace_id": s.ipc.WorkspaceID,
		"chat_id":      s.ipc.ChatID,
		"confidence":   req.Confidence,
		"reason":       req.Reason,
	}
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "failed to serialize request"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.ipc.BaseURL+"/api/v1/internal/report-confidence", bytes.NewReader(bodyJSON))
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "failed to create request"})
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Internal-Token", s.ipc.Token)

	resp, err := ipcClient.Do(httpReq)
	if err != nil {
		writeJSONResponse(w, http.StatusBadGateway, map[string]string{
			"error": fmt.Sprintf("confidence report failed: %v", err),
		})
		return
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		writeJSONResponse(w, http.StatusBadGateway, map[string]string{
			"error":       "invalid response from confidence endpoint",
			"status_code": fmt.Sprintf("%d", resp.StatusCode),
		})
		return
	}
	if resp.StatusCode >= 400 {
		writeJSONResponse(w, resp.StatusCode, result)
		return
	}
	writeJSONResponse(w, http.StatusOK, result)
}
