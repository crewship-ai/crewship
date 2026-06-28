package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// ProxyHandler proxies requests from the UI to the crewshipd sidecar over the Unix socket.
type ProxyHandler struct {
	db     *sql.DB
	logger *slog.Logger
	client *http.Client
}

// NewProxyHandler creates a ProxyHandler that communicates with the sidecar via the given Unix socket path.
func NewProxyHandler(db *sql.DB, logger *slog.Logger, socketPath string) *ProxyHandler {
	return &ProxyHandler{
		db:     db,
		logger: logger,
		client: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return net.Dial("unix", socketPath)
				},
			},
		},
	}
}

func (h *ProxyHandler) ipcGet(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "http://crewshipd"+path, nil)
	if err != nil {
		return nil, err
	}
	return h.client.Do(req)
}

func (h *ProxyHandler) ipcPost(ctx context.Context, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", "http://crewshipd"+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return h.client.Do(req)
}

func (h *ProxyHandler) ipcPut(ctx context.Context, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "PUT", "http://crewshipd"+path, body)
	if err != nil {
		return nil, err
	}
	return h.client.Do(req)
}

func (h *ProxyHandler) proxyJSON(w http.ResponseWriter, resp *http.Response) {
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		h.logger.Debug("proxy JSON stream error", "error", err)
	}
}

// CrewshipdHealth checks the health of the crewshipd sidecar process.
func (h *ProxyHandler) CrewshipdHealth(w http.ResponseWriter, r *http.Request) {
	resp, err := h.ipcGet(r.Context(), "/health")
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unreachable"})
		return
	}
	h.proxyJSON(w, resp)
}

// AgentDebug returns debug information for a running agent (container state, process info).
func (h *ProxyHandler) AgentDebug(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())

	if !canRole(role, "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	var agentName, cliAdapter, status, crewID sql.NullString
	err := h.db.QueryRowContext(r.Context(),
		"SELECT name, cli_adapter, status, crew_id FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		agentID, workspaceID).Scan(&agentName, &cliAdapter, &status, &crewID)
	if err != nil {
		if err == sql.ErrNoRows {
			replyError(w, http.StatusNotFound, "Agent not found")
			return
		}
		h.logger.Error("get agent for debug", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	debug := map[string]interface{}{
		"agent": map[string]interface{}{
			"id": agentID, "name": agentName.String,
			"cli_adapter": cliAdapter.String, "db_status": status.String,
		},
		"crewshipd_reachable": false,
	}

	if resp, err := h.ipcGet(r.Context(), "/debug/info"); err == nil {
		defer resp.Body.Close()
		var data map[string]interface{}
		if json.NewDecoder(resp.Body).Decode(&data) == nil {
			debug["crewshipd"] = data
			debug["crewshipd_reachable"] = true
		}
	}

	if resp, err := h.ipcGet(r.Context(), fmt.Sprintf("/agents/%s/status", url.PathEscape(agentID))); err == nil {
		defer resp.Body.Close()
		var data interface{}
		if json.NewDecoder(resp.Body).Decode(&data) == nil {
			debug["runtime"] = data
		}
	} else {
		debug["runtime"] = map[string]string{"status": "unreachable"}
	}

	if resp, err := h.ipcGet(r.Context(), fmt.Sprintf("/debug/logs?limit=200&agent_id=%s", url.QueryEscape(agentID))); err == nil {
		defer resp.Body.Close()
		var data map[string]interface{}
		if json.NewDecoder(resp.Body).Decode(&data) == nil {
			debug["service_logs"] = data["logs"]
		}
	} else {
		debug["service_logs"] = []interface{}{}
	}

	if crewID.Valid {
		path := fmt.Sprintf("/agents/%s/logs?crew_id=%s&offset=0&limit=50", url.PathEscape(agentID), url.QueryEscape(crewID.String))
		if resp, err := h.ipcGet(r.Context(), path); err == nil {
			defer resp.Body.Close()
			var data map[string]interface{}
			if json.NewDecoder(resp.Body).Decode(&data) == nil {
				debug["agent_logs"] = data["logs"]
			}
		}
	} else {
		debug["agent_logs"] = []interface{}{}
	}

	writeJSON(w, http.StatusOK, debug)
}

// AgentFiles lists files in an agent's working directory via the sidecar.

func (h *ProxyHandler) AgentLogs(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())

	// Audit M13: agent logs are sensitive (may contain prompts,
	// partial responses, secrets that escaped lookout.Redact). Gate
	// on "read" so an empty / VIEWER role can't tail another tenant's
	// agent traffic.
	role := RoleFromContext(r.Context())
	if !canRole(role, "read") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	limitInt, offsetInt := parsePagination(r, 100, 500)
	offset := strconv.Itoa(offsetInt)
	limit := strconv.Itoa(limitInt)

	var crewID sql.NullString
	var slug string
	err := h.db.QueryRowContext(r.Context(),
		"SELECT crew_id, slug FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		agentID, workspaceID).Scan(&crewID, &slug)
	if err != nil {
		replyError(w, http.StatusNotFound, "Agent not found")
		return
	}
	if !crewID.Valid {
		writeJSON(w, http.StatusOK, []interface{}{})
		return
	}

	path := fmt.Sprintf("/agents/%s/logs?crew_id=%s&offset=%s&limit=%s", url.PathEscape(slug), url.QueryEscape(crewID.String), offset, limit)
	resp, err := h.ipcGet(r.Context(), path)
	if err != nil {
		writeJSON(w, http.StatusOK, []interface{}{})
		return
	}
	defer resp.Body.Close()

	var data map[string]interface{}
	if json.NewDecoder(resp.Body).Decode(&data) == nil {
		if logs, ok := data["logs"]; ok {
			writeJSON(w, http.StatusOK, logs)
			return
		}
	}
	writeJSON(w, http.StatusOK, []interface{}{})
}

// AgentStop sends a stop signal to a running agent via the sidecar.
func (h *ProxyHandler) AgentStop(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())

	if !canRole(role, "create") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	found, err := agentExists(r.Context(), h.db, agentID, workspaceID)
	if err != nil {
		h.logger.Error("agent exists check", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if !found {
		replyError(w, http.StatusNotFound, "Agent not found")
		return
	}

	// Try to stop via crewshipd (best effort)
	h.ipcPost(r.Context(), fmt.Sprintf("/agents/%s/stop", url.PathEscape(agentID)), nil)

	now := time.Now().UTC().Format(time.RFC3339)
	res, err := h.db.ExecContext(r.Context(),
		"UPDATE agents SET status = 'STOPPED', updated_at = ? WHERE id = ? AND workspace_id = ?",
		now, agentID, workspaceID)
	if err != nil {
		h.logger.Error("update agent status to STOPPED", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		replyError(w, http.StatusNotFound, "Agent not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"id": agentID, "status": "STOPPED"})
}

// ChatMessages returns the conversation message history for a chat session.
func (h *ProxyHandler) ChatMessages(w http.ResponseWriter, r *http.Request) {
	chatID := r.PathValue("chatId")
	user := UserFromContext(r.Context())
	// Audit #495 follow-up: read-tier gate. The existing workspace_member
	// lookup tests *membership* but not *role* -- canRole(role, "read")
	// fails closed on empty / unmapped role.
	if !canRole(RoleFromContext(r.Context()), "read") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	var chatWSID string
	err := h.db.QueryRowContext(r.Context(),
		"SELECT workspace_id FROM chats WHERE id = ?", chatID).Scan(&chatWSID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Chat doesn't exist yet (new session before first message) — return empty messages
			writeJSON(w, http.StatusOK, map[string]interface{}{"messages": []interface{}{}})
			return
		}
		h.logger.Error("get chat workspace", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	var memberRole string
	err = h.db.QueryRowContext(r.Context(),
		"SELECT role FROM workspace_members WHERE workspace_id = ? AND user_id = ?",
		chatWSID, user.ID).Scan(&memberRole)
	if err != nil {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}

	path := fmt.Sprintf("/chats/%s/messages?offset=%d&limit=%d", url.PathEscape(chatID), offset, limit)
	resp, err := h.ipcGet(r.Context(), path)
	if err != nil {
		replyError(w, http.StatusBadGateway, "Failed to fetch messages")
		return
	}
	h.proxyJSON(w, resp)
}

// AgentContainerFiles lists files inside the agent's running container.

func (h *ProxyHandler) AgentGitLog(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	// Audit #495 follow-up: read-tier gate matches the AgentFiles trio.
	if !canRole(RoleFromContext(r.Context()), "read") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	var slug, crewID sql.NullString
	err := h.db.QueryRowContext(r.Context(),
		"SELECT slug, crew_id FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		agentID, workspaceID).Scan(&slug, &crewID)
	if err != nil || !crewID.Valid {
		replyError(w, http.StatusNotFound, "Agent not found or not assigned to a crew")
		return
	}

	ipcPath := fmt.Sprintf("/crews/%s/git-log", url.PathEscape(crewID.String))
	if slug.Valid {
		ipcPath += "?agent_slug=" + url.QueryEscape(slug.String)
	}
	resp, err := h.ipcGet(r.Context(), ipcPath)
	if err != nil {
		replyError(w, http.StatusBadGateway, "Failed to fetch git log")
		return
	}
	defer resp.Body.Close()

	var data map[string]interface{}
	if json.NewDecoder(resp.Body).Decode(&data) == nil {
		if commits, ok := data["commits"]; ok {
			writeJSON(w, http.StatusOK, commits)
			return
		}
	}
	writeJSON(w, http.StatusOK, []interface{}{})
}

// CrewGitDiff proxies the crew container's base-branch git diff to the
// dashboard's Changes tab. Crew-scoped (the diff is the whole crew
// workspace's change set) — GET /api/v1/crews/{crewId}/git-diff.
func (h *ProxyHandler) CrewGitDiff(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("crewId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	if !canRole(RoleFromContext(r.Context()), "read") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	// Confirm the crew exists in this workspace before reaching into its
	// container — masks cross-tenant probes as 404.
	var exists int
	err := h.db.QueryRowContext(r.Context(),
		"SELECT 1 FROM crews WHERE id = ? AND workspace_id = ?", crewID, workspaceID).Scan(&exists)
	if err != nil {
		replyError(w, http.StatusNotFound, "Crew not found")
		return
	}

	ipcPath := fmt.Sprintf("/crews/%s/git-diff", url.PathEscape(crewID))
	if slug := r.URL.Query().Get("agent_slug"); slug != "" {
		ipcPath += "?agent_slug=" + url.QueryEscape(slug)
	}
	resp, err := h.ipcGet(r.Context(), ipcPath)
	if err != nil {
		replyError(w, http.StatusBadGateway, "Failed to compute diff")
		return
	}
	defer resp.Body.Close()

	var data map[string]interface{}
	if json.NewDecoder(resp.Body).Decode(&data) != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"is_repo": false})
		return
	}
	writeJSON(w, http.StatusOK, data)
}

// RunGitDiff resolves a pipeline run to its crew and returns that crew
// container's base-branch diff — the change set the run produced. Powers
// the Activity dock's Changes tab.
// GET /api/v1/workspaces/{workspaceId}/pipeline-runs/{runId}/changes.
//
// Crew resolution: the run's invoking_crew_id, falling back to the
// pipeline's author_crew_id. A run with no resolvable crew (e.g. a
// workspace-level pipeline) degrades to is_repo:false rather than erroring.
func (h *ProxyHandler) RunGitDiff(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	if !canRole(RoleFromContext(r.Context()), "read") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	var crewID sql.NullString
	err := h.db.QueryRowContext(r.Context(), `
		SELECT COALESCE(NULLIF(pr.invoking_crew_id, ''), p.author_crew_id)
		FROM pipeline_runs pr
		LEFT JOIN pipelines p ON pr.pipeline_id = p.id AND p.workspace_id = pr.workspace_id
		WHERE pr.id = ? AND pr.workspace_id = ?`, runID, workspaceID).Scan(&crewID)
	if err != nil {
		replyError(w, http.StatusNotFound, "Run not found")
		return
	}
	if !crewID.Valid || crewID.String == "" {
		// No crew bound to this run — nothing to diff against a container.
		writeJSON(w, http.StatusOK, map[string]interface{}{"is_repo": false})
		return
	}

	resp, err := h.ipcGet(r.Context(), fmt.Sprintf("/crews/%s/git-diff", url.PathEscape(crewID.String)))
	if err != nil {
		replyError(w, http.StatusBadGateway, "Failed to compute diff")
		return
	}
	defer resp.Body.Close()

	var data map[string]interface{}
	if json.NewDecoder(resp.Body).Decode(&data) != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"is_repo": false})
		return
	}
	writeJSON(w, http.StatusOK, data)
}
