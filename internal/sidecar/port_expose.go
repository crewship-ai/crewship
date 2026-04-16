package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// maxExposeDescriptionLen matches the server-side limit in
	// internal/api/port_expose_handler.go. Enforced here so obviously-bad
	// payloads never leave the container and we give the agent a local
	// 400 instead of a round-trip failure.
	maxExposeDescriptionLen = 200

	// exposeTTLSecondsMax mirrors the server's MaxTTL (24h). The handler
	// clamps again server-side so this is just a cheap pre-check.
	exposeTTLSecondsMax = 24 * 60 * 60
)

// exposePortRequestBody is the agent-supplied payload. All contextual ids
// (workspace, crew, agent, container, chat) come from s.ipc — agents cannot
// override who they are or which container they're targeting.
type exposePortRequestBody struct {
	Port        int    `json:"port"`
	Description string `json:"description"`
	TTLSeconds  int    `json:"ttl_seconds,omitempty"`
}

// handleExposePort handles POST /expose-port. Agents running in the
// container call it to ask crewshipd for a public reverse-proxy URL into a
// port they've opened locally. MVP is synchronous — the sidecar forwards to
// crewshipd, which returns the URL immediately (open-by-default policy).
// When a future approval layer lands the handler will switch to long-poll;
// the public shape {token, url, expires_at} stays the same.
func (s *Server) handleExposePort(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{
			"error": "port-expose IPC not configured",
		})
		return
	}

	var req exposePortRequestBody
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	req.Description = strings.TrimSpace(req.Description)

	if req.Port < 1 || req.Port > 65535 {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{
			"error": "port must be between 1 and 65535",
		})
		return
	}
	if len(req.Description) > maxExposeDescriptionLen {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{
			"error": "description too long (max 200 chars)",
		})
		return
	}
	// Reject obviously hostile description content. Server re-validates, but
	// failing fast here gives the agent a cleaner 400.
	if strings.ContainsAny(req.Description, "\x00\n\r") {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{
			"error": "description contains forbidden characters",
		})
		return
	}
	if req.TTLSeconds < 0 || req.TTLSeconds > exposeTTLSecondsMax {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{
			"error": "ttl_seconds must be between 0 and 86400",
		})
		return
	}

	ipcPayload := map[string]interface{}{
		"workspace_id": s.ipc.WorkspaceID,
		"crew_id":      s.ipc.CrewID,
		"agent_id":     s.ipc.AgentID,
		"container_id": s.ipc.ContainerID,
		"port":         req.Port,
		"description":  req.Description,
	}
	if s.ipc.ChatID != "" {
		ipcPayload["chat_id"] = s.ipc.ChatID
	}
	if req.TTLSeconds > 0 {
		ipcPayload["ttl_seconds"] = req.TTLSeconds
	}

	bodyJSON, err := json.Marshal(ipcPayload)
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "failed to serialize request"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 32*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.ipc.BaseURL+"/api/v1/internal/port-expose", bytes.NewReader(bodyJSON))
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "failed to build IPC request"})
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Internal-Token", s.ipc.Token)

	resp, err := ipcClient.Do(httpReq)
	if err != nil {
		s.logger.Error("port_expose bridge: IPC request failed", "error", err)
		writeJSONResponse(w, http.StatusBadGateway, map[string]string{
			"error": "port-expose request failed — crewshipd unreachable",
		})
		return
	}
	defer resp.Body.Close()

	// Pass the crewshipd response through verbatim. We deliberately don't
	// try to re-shape errors — the agent is more useful when it sees the
	// exact server message.
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		writeJSONResponse(w, http.StatusBadGateway, map[string]string{
			"error": "invalid response from crewshipd",
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(raw)
}
