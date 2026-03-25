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
var escalateWaitClient = &http.Client{Timeout: 310 * time.Second}

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

// handleQuery handles POST /query from agents wanting to ask a peer a question.
// The query is synchronous — the caller blocks until the target agent responds.
func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}

	var req queryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
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

	standupURL := fmt.Sprintf("%s/api/v1/internal/standup?crew_id=%s", s.ipc.BaseURL, neturl.QueryEscape(s.ipc.CrewID))
	if since != "" {
		standupURL += "&since=" + neturl.QueryEscape(since)
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, standupURL, nil)
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "failed to create request"})
		return
	}
	httpReq.Header.Set("X-Internal-Token", s.ipc.Token)

	resp, err := ipcClient.Do(httpReq)
	if err != nil {
		writeJSONResponse(w, http.StatusBadGateway, map[string]string{
			"error": fmt.Sprintf("standup request failed: %v", err),
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

// handleEscalate handles POST /escalate from agents wanting to flag something for the lead.
// The call blocks until a human responds (up to 5 minutes) or times out.
func (s *Server) handleEscalate(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}

	var req escalateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
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

	waitURL := fmt.Sprintf("%s/api/v1/internal/escalations/%s/wait", s.ipc.BaseURL, escalationID)
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
