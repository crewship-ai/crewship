package api

import (
	"database/sql"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type listItem struct {
	ID            string  `json:"id"`
	AgentID       string  `json:"agent_id"`
	AgentSlug     string  `json:"agent_slug"`
	ContainerPort int     `json:"container_port"`
	Description   string  `json:"description,omitempty"`
	Status        string  `json:"status"`
	ExpiresAt     string  `json:"expires_at"`
	RevokedAt     *string `json:"revoked_at,omitempty"`
	RevokedReason *string `json:"revoked_reason,omitempty"`
	CreatedAt     string  `json:"created_at"`
}

// List is GET /api/v1/crews/{crewId}/port-expose. Any workspace MEMBER can
// list exposures for their crew. Filtering by ?status=active|revoked|expired
// is optional; default is ACTIVE only (matches the CLI's common case).
func (h *PortExposeHandler) List(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	crewID := r.PathValue("crewId")
	if crewID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "crewId required"})
		return
	}

	statusFilter := strings.ToUpper(r.URL.Query().Get("status"))
	if statusFilter == "" {
		statusFilter = "ACTIVE"
	}
	if statusFilter != "ACTIVE" && statusFilter != "REVOKED" && statusFilter != "EXPIRED" && statusFilter != "ALL" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "status must be active, revoked, expired, or all",
		})
		return
	}

	var rows *sql.Rows
	var err error
	const baseQuery = `
		SELECT pe.id, pe.agent_id, a.slug, pe.container_port, pe.description,
		       pe.status, pe.expires_at, pe.revoked_at, pe.revoked_reason, pe.created_at
		FROM port_exposures pe
		JOIN agents a ON a.id = pe.agent_id
		WHERE pe.workspace_id = ? AND pe.crew_id = ?
	`
	if statusFilter == "ALL" {
		rows, err = h.db.QueryContext(r.Context(), baseQuery+" ORDER BY pe.created_at DESC", workspaceID, crewID)
	} else {
		rows, err = h.db.QueryContext(r.Context(),
			baseQuery+" AND pe.status = ? ORDER BY pe.created_at DESC",
			workspaceID, crewID, statusFilter)
	}
	if err != nil {
		h.logger.Error("port_expose: list query", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	out := make([]listItem, 0, 8)
	for rows.Next() {
		var it listItem
		var description, revokedAt, revokedReason sql.NullString
		if err := rows.Scan(&it.ID, &it.AgentID, &it.AgentSlug, &it.ContainerPort,
			&description, &it.Status, &it.ExpiresAt, &revokedAt, &revokedReason, &it.CreatedAt); err != nil {
			h.logger.Error("port_expose: list scan", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		if description.Valid {
			it.Description = description.String
		}
		if revokedAt.Valid {
			v := revokedAt.String
			it.RevokedAt = &v
		}
		if revokedReason.Valid {
			v := revokedReason.String
			it.RevokedReason = &v
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("port_expose: list rows err", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	writeJSON(w, http.StatusOK, out)
}

// revokePayload carries the human-provided reason. Not required, but strongly
// encouraged — it's what shows up in audit queries.
type revokePayload struct {
	Reason string `json:"reason"`
}

// Revoke is POST /api/v1/crews/{crewId}/port-expose/{id}/revoke. Flips the
// row to REVOKED, drops the token from the registry, and broadcasts so any
// UI showing the live list updates. Requires MANAGER+ to match the policy
// used by issue review / escalation resolve.
func (h *PortExposeHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}
	crewID := r.PathValue("crewId")
	exposeID := r.PathValue("id")
	if crewID == "" || exposeID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "crewId and id required"})
		return
	}

	var body revokePayload
	// Body is optional — ignore decode error on empty payloads.
	_ = readJSON(r, &body)
	if len(body.Reason) > 500 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "reason too long (max 500)"})
		return
	}

	// Two-phase: conditional UPDATE (status was ACTIVE or PENDING) + lookup
	// token afterwards so we can drop it from the registry and broadcast.
	now := time.Now().UTC().Format(time.RFC3339)
	var reasonVal interface{}
	if body.Reason != "" {
		reasonVal = body.Reason
	}
	res, err := h.db.ExecContext(r.Context(), `
		UPDATE port_exposures
		SET status = 'REVOKED', revoked_at = ?, revoked_reason = ?
		WHERE id = ? AND workspace_id = ? AND crew_id = ? AND status IN ('ACTIVE','PENDING')
	`, now, reasonVal, exposeID, workspaceID, crewID)
	if err != nil {
		h.logger.Error("port_expose: revoke update", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "exposure not found or already revoked/expired"})
		return
	}

	// Fetch token to clear the in-memory entry.
	var token string
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT token FROM port_exposures WHERE id = ?`, exposeID).Scan(&token); err == nil && token != "" {
		h.registry.Remove(token)
	}

	if h.hub != nil {
		broadcastWorkspaceEvent(h.hub, workspaceID, "port_expose.revoked", map[string]string{
			"id":      exposeID,
			"crew_id": crewID,
			"reason":  body.Reason,
		})
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// ServeExposed handles GET /exposed/{token}/... — the capability URL the
// agent hands to the user. No authentication: the token IS the capability.
// Unknown tokens get 404, expired get 410, WebSocket upgrade gets 426.
// Everything else is reverse-proxied into the target container.
//
// The container IP stored on the entry is re-verified against Docker on
// every request when a DockerInspector is wired up. Crew containers can
// restart and grab a different bridge IP; we'd rather do one docker
// inspect per request (~1ms locally) than silently forward to whoever
// inherited the old IP.
func (h *PortExposeHandler) ServeExposed(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if token == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	entry, ok := h.registry.Lookup(token)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if entry.Expired(time.Now().UTC()) {
		http.Error(w, "gone (expired)", http.StatusGone)
		return
	}

	// MVP does not proxy WebSocket upgrades — the code path is small but
	// the security surface isn't. Block the upgrade outright rather than
	// silently downgrading to HTTP.
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "websocket not supported", http.StatusUpgradeRequired)
		return
	}

	// Resolve the live container IP. If the DockerInspector is not wired
	// (unit tests) we fall back to the cached IP on the entry.
	target := entry.Target()
	if h.docker != nil && entry.ContainerID != "" {
		freshIP, err := h.docker.ContainerIP(r.Context(), entry.ContainerID, h.cfg.NetworkName)
		if err != nil {
			h.logger.Warn("port_expose proxy: container IP re-resolve failed",
				"token", safeTokenPrefix(token), "container_id", entry.ContainerID, "error", err)
			http.Error(w, "bad gateway: container unreachable", http.StatusBadGateway)
			return
		}
		if freshIP != entry.ContainerIP {
			// Container was recreated and re-IP'd. Update the cache so
			// future requests skip the "IP changed" branch until the next
			// restart.
			h.registry.UpdateIP(token, freshIP)
		}
		target = &url.URL{
			Scheme: "http",
			Host:   freshIP + ":" + strconv.Itoa(entry.ContainerPort),
		}
	}

	// Strip the /exposed/{token} prefix before handing the request to the
	// reverse proxy so the target sees the URL it would normally see.
	prefix := "/exposed/" + token
	r.URL.Path = strings.TrimPrefix(r.URL.Path, prefix)
	if r.URL.Path == "" {
		r.URL.Path = "/"
	}
	r.URL.RawPath = ""

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorLog = slog.NewLogLogger(h.logger.Handler(), slog.LevelWarn)
	proxy.ErrorHandler = func(rw http.ResponseWriter, rq *http.Request, err error) {
		h.logger.Warn("port_expose proxy error",
			"token", safeTokenPrefix(token), "target", target.String(), "error", err)
		http.Error(rw, "bad gateway", http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}

// ----- helpers -----

// errAgentNotFound is returned from validateAgentBoundary when the (agent,
// crew, workspace) tuple doesn't exist. Callers convert it to 403.
