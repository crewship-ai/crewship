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

type queryRequest struct {
	Target   string `json:"target"`
	Question string `json:"question"`
	From     string `json:"from"`
	Depth    int    `json:"depth"`
}

type escalateRequest struct {
	From     string `json:"from"`
	Reason   string `json:"reason"`
	Context  string `json:"context"`
	Type     string `json:"type"`
	Metadata string `json:"metadata"`
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

	body := map[string]string{
		"from_slug":    req.From,
		"reason":       req.Reason,
		"context":      req.Context,
		"type":         req.Type,
		"metadata":     req.Metadata,
		"crew_id":      s.ipc.CrewID,
		"workspace_id": s.ipc.WorkspaceID,
		"chat_id":      s.ipc.ChatID,
	}
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "failed to serialize request"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
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

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		writeJSONResponse(w, http.StatusBadGateway, map[string]string{"error": "invalid response from crewshipd"})
		return
	}

	writeJSONResponse(w, resp.StatusCode, result)
}
