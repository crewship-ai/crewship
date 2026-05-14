package api

import (
	"errors"
	"net/http"
	"time"
)

type requestPayload struct {
	WorkspaceID string `json:"workspace_id"`
	CrewID      string `json:"crew_id"`
	AgentID     string `json:"agent_id"`
	ContainerID string `json:"container_id"`
	ChatID      string `json:"chat_id"`
	Port        int    `json:"port"`
	Description string `json:"description"`
	TTLSeconds  int    `json:"ttl_seconds,omitempty"`
}

// requestResponse is handed back to the sidecar and ultimately to the agent.
type requestResponse struct {
	ID        string `json:"id"`
	Token     string `json:"token"`
	URL       string `json:"url"`
	ExpiresAt string `json:"expires_at"`
}

// RequestExpose is POST /api/v1/internal/port-expose. The sidecar calls it
// after validating the agent's request payload; this handler trusts the IPC
// context but still enforces the agent↔crew↔workspace boundary against the
// DB in case the sidecar config ever diverges from DB reality.
func (h *PortExposeHandler) RequestExpose(w http.ResponseWriter, r *http.Request) {
	// Fail fast if the operator didn't configure where to point clients.
	// Returning a url built from "" would hand the agent a broken link.
	if h.cfg.PublicBaseURL == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "port-expose not configured: set CREWSHIP_PUBLIC_URL to this server's externally reachable origin",
		})
		return
	}
	var body requestPayload
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}

	if body.WorkspaceID == "" || body.CrewID == "" || body.AgentID == "" ||
		body.ContainerID == "" || body.Port < 1 || body.Port > 65535 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "workspace_id, crew_id, agent_id, container_id, and port (1-65535) are required",
		})
		return
	}
	if len(body.Description) > 200 {
		replyError(w, http.StatusBadRequest, "description too long (max 200 chars)")
		return
	}

	// Anti-spoof: verify the agent actually belongs to the crew + workspace
	// the sidecar claims. This mirrors the pattern KeeperHandler uses.
	agentSlug, err := h.validateAgentBoundary(r.Context(), body.AgentID, body.CrewID, body.WorkspaceID)
	if err != nil {
		if errors.Is(err, errAgentNotFound) {
			replyError(w, http.StatusForbidden, "agent does not belong to the requested crew/workspace")
			return
		}
		h.logger.Error("port_expose: agent boundary check", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Clamp TTL into config bounds. Zero / negative falls back to the default.
	ttl := h.cfg.DefaultTTL
	if body.TTLSeconds > 0 {
		ttl = time.Duration(body.TTLSeconds) * time.Second
	}
	if ttl > h.cfg.MaxTTL {
		ttl = h.cfg.MaxTTL
	}
	if ttl < time.Second {
		ttl = time.Second
	}

	// Rate limits: count rows the agent already has live.
	if err := h.checkQuota(r.Context(), body.AgentID, body.WorkspaceID); err != nil {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": err.Error()})
		return
	}

	// Policy decision. MVP's AllowAllPolicy returns "allow". A future
	// ApprovalPolicy would return "pending" here; the handler doesn't
	// branch into the approval flow yet because the sidecar synchronous
	// response shape doesn't support it. When we add approval, the
	// "pending" branch writes a PENDING row, creates an escalation, and
	// the sidecar switches to long-poll — no schema changes.
	decision, reason, err := h.policy.Check(r.Context(), &PortExposeRequest{
		WorkspaceID: body.WorkspaceID,
		CrewID:      body.CrewID,
		AgentID:     body.AgentID,
		AgentSlug:   agentSlug,
		Port:        body.Port,
		Description: body.Description,
		TTLSeconds:  int(ttl.Seconds()),
	})
	if err != nil {
		h.logger.Error("port_expose: policy check", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	switch decision {
	case ExposeDeny:
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "denied by policy: " + reason})
		return
	case ExposePending:
		// Reserved for a future change. Surface a clear error so the sidecar
		// doesn't silently misbehave if someone wires ApprovalPolicy before
		// the approval long-poll lands.
		replyError(w, http.StatusNotImplemented, "approval-required policy not yet supported")
		return
	case ExposeAllow:
		// fall through
	default:
		h.logger.Error("port_expose: unknown policy decision", "decision", string(decision))
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	// A misconfigured deployment (no Docker client wired) would panic on the
	// next call — match the ServeExposed nil-guard so we 503 cleanly.
	if h.docker == nil {
		h.logger.Error("port_expose: docker inspector not configured")
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "port-expose not available: container inspection not configured",
		})
		return
	}

	// Look up the container's IP on the crew bridge. Rejecting here also
	// blocks the agent from asking us to proxy to crewshipd or host
	// services — those aren't on the crewship-agents network.
	containerIP, err := h.docker.ContainerIP(r.Context(), body.ContainerID, h.cfg.NetworkName)
	if err != nil {
		h.logger.Warn("port_expose: container IP lookup",
			"container_id", body.ContainerID, "network", h.cfg.NetworkName, "error", err)
		replyError(w, http.StatusBadGateway, "container not reachable on crew network")
		return
	}

	token, err := generateExposeToken()
	if err != nil {
		h.logger.Error("port_expose: token generation", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	id := generateCUID()
	now := time.Now().UTC()
	expiresAt := now.Add(ttl)

	var chatIDVal interface{}
	if body.ChatID != "" {
		chatIDVal = body.ChatID
	}
	var descVal interface{}
	if body.Description != "" {
		descVal = body.Description
	}

	_, err = h.db.ExecContext(r.Context(), `
		INSERT INTO port_exposures (
			id, workspace_id, crew_id, agent_id, chat_id, token,
			container_id, container_ip, container_port, description,
			status, expires_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'ACTIVE', ?, ?)
	`,
		id, body.WorkspaceID, body.CrewID, body.AgentID, chatIDVal, token,
		body.ContainerID, containerIP, body.Port, descVal,
		expiresAt.Format(time.RFC3339), now.Format(time.RFC3339),
	)
	if err != nil {
		h.logger.Error("port_expose: insert row", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	entry := &ExposeEntry{
		ID:            id,
		Token:         token,
		ContainerID:   body.ContainerID,
		ContainerIP:   containerIP,
		ContainerPort: body.Port,
		ExpiresAt:     expiresAt,
	}
	h.registry.Add(entry)

	url := h.exposeURL(token)

	if h.hub != nil {
		payload := map[string]string{
			"id":          id,
			"agent_id":    body.AgentID,
			"agent_slug":  agentSlug,
			"crew_id":     body.CrewID,
			"description": body.Description,
			"url":         url,
			"expires_at":  expiresAt.Format(time.RFC3339),
		}
		broadcastWorkspaceEvent(h.hub, body.WorkspaceID, "port_expose.created", payload)
		if body.ChatID != "" {
			broadcastChannelEvent(h.hub, "session", body.ChatID, "port_expose_created", payload)
		}
	}

	h.logger.Info("port exposure created",
		"id", id, "agent_slug", agentSlug, "crew_id", body.CrewID,
		"port", body.Port, "ttl", ttl, "policy_reason", reason)

	writeJSON(w, http.StatusCreated, requestResponse{
		ID:        id,
		Token:     token,
		URL:       url,
		ExpiresAt: expiresAt.Format(time.RFC3339),
	})
}

// listItem is the row shape returned by List. Token is intentionally omitted
// so a MEMBER without revoke rights can't reconstruct the public URL of
// someone else's exposure — only the issuing agent gets the URL.
