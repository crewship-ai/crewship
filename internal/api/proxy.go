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
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type ProxyHandler struct {
	db         *sql.DB
	logger     *slog.Logger
	socketPath string
}

func NewProxyHandler(db *sql.DB, logger *slog.Logger, socketPath string) *ProxyHandler {
	return &ProxyHandler{db: db, logger: logger, socketPath: socketPath}
}

func (h *ProxyHandler) ipcClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", h.socketPath)
			},
		},
	}
}

func (h *ProxyHandler) ipcGet(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "http://crewshipd"+path, nil)
	if err != nil {
		return nil, err
	}
	return h.ipcClient().Do(req)
}

func (h *ProxyHandler) ipcPost(ctx context.Context, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", "http://crewshipd"+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return h.ipcClient().Do(req)
}

func (h *ProxyHandler) ipcPut(ctx context.Context, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "PUT", "http://crewshipd"+path, body)
	if err != nil {
		return nil, err
	}
	return h.ipcClient().Do(req)
}

func (h *ProxyHandler) proxyJSON(w http.ResponseWriter, resp *http.Response) {
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		h.logger.Debug("proxy JSON stream error", "error", err)
	}
}

func (h *ProxyHandler) CrewshipdHealth(w http.ResponseWriter, r *http.Request) {
	resp, err := h.ipcGet(r.Context(), "/health")
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unreachable"})
		return
	}
	h.proxyJSON(w, resp)
}

func (h *ProxyHandler) AgentDebug(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())

	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	var agentName, cliAdapter, status, crewID sql.NullString
	err := h.db.QueryRowContext(r.Context(),
		"SELECT name, cli_adapter, status, crew_id FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		agentID, workspaceID).Scan(&agentName, &cliAdapter, &status, &crewID)
	if err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent not found"})
			return
		}
		h.logger.Error("get agent for debug", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
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

	if resp, err := h.ipcGet(r.Context(), fmt.Sprintf("/agents/%s/status", agentID)); err == nil {
		defer resp.Body.Close()
		var data interface{}
		if json.NewDecoder(resp.Body).Decode(&data) == nil {
			debug["runtime"] = data
		}
	} else {
		debug["runtime"] = map[string]string{"status": "unreachable"}
	}

	if resp, err := h.ipcGet(r.Context(), fmt.Sprintf("/debug/logs?limit=200&agent_id=%s", agentID)); err == nil {
		defer resp.Body.Close()
		var data map[string]interface{}
		if json.NewDecoder(resp.Body).Decode(&data) == nil {
			debug["service_logs"] = data["logs"]
		}
	} else {
		debug["service_logs"] = []interface{}{}
	}

	if crewID.Valid {
		path := fmt.Sprintf("/agents/%s/logs?crew_id=%s&offset=0&limit=50", agentID, crewID.String)
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

func (h *ProxyHandler) AgentFiles(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())

	var slug, crewID sql.NullString
	err := h.db.QueryRowContext(r.Context(),
		"SELECT slug, crew_id FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		agentID, workspaceID).Scan(&slug, &crewID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent not found"})
		return
	}
	if !crewID.Valid {
		writeJSON(w, http.StatusOK, []interface{}{})
		return
	}

	ipcPath := fmt.Sprintf("/crews/%s/files?agent_slug=%s", crewID.String, slug.String)
	if r.URL.Query().Get("recursive") == "true" {
		ipcPath += "&recursive=true"
	}
	if subdir := r.URL.Query().Get("subdir"); subdir != "" {
		ipcPath += "&subdir=" + url.QueryEscape(subdir)
	}
	resp, err := h.ipcGet(r.Context(), ipcPath)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Failed to fetch files"})
		return
	}
	defer resp.Body.Close()

	var data map[string]interface{}
	if json.NewDecoder(resp.Body).Decode(&data) == nil {
		if files, ok := data["files"]; ok {
			writeJSON(w, http.StatusOK, files)
			return
		}
	}
	writeJSON(w, http.StatusOK, []interface{}{})
}

func (h *ProxyHandler) AgentFileDownload(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	filePath := r.URL.Query().Get("path")

	if filePath == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path parameter required"})
		return
	}

	var slug, crewID sql.NullString
	err := h.db.QueryRowContext(r.Context(),
		"SELECT slug, crew_id FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		agentID, workspaceID).Scan(&slug, &crewID)
	if err != nil || !crewID.Valid {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent not found"})
		return
	}

	cleanPath := filepath.Clean(filePath)
	if strings.HasPrefix(cleanPath, "..") || filepath.IsAbs(cleanPath) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid file path"})
		return
	}

	ipcPath := fmt.Sprintf("/crews/%s/files/download?path=%s", crewID.String, url.QueryEscape(cleanPath))

	resp, err := h.ipcGet(r.Context(), ipcPath)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "File not found"})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "File not found"})
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", resp.Header.Get("Content-Disposition"))
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		w.Header().Set("Content-Length", cl)
	}
	if _, err := io.Copy(w, resp.Body); err != nil {
		h.logger.Debug("agent file download stream error", "error", err, "agent_id", agentID)
	}
}

func (h *ProxyHandler) AgentFileSave(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	filePath := r.URL.Query().Get("path")

	if filePath == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path parameter required"})
		return
	}

	var slug, crewID sql.NullString
	err := h.db.QueryRowContext(r.Context(),
		"SELECT slug, crew_id FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		agentID, workspaceID).Scan(&slug, &crewID)
	if err != nil || !crewID.Valid {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent not found"})
		return
	}

	cleanPath := filepath.Clean(filePath)
	if strings.HasPrefix(cleanPath, "..") || filepath.IsAbs(cleanPath) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid file path"})
		return
	}

	ipcPath := fmt.Sprintf("/crews/%s/files/save?path=%s", crewID.String, url.QueryEscape(cleanPath))

	resp, err := h.ipcPut(r.Context(), ipcPath, r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Failed to save file"})
		return
	}
	h.proxyJSON(w, resp)
}

func (h *ProxyHandler) CrewFiles(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("crewId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	var exists int
	err := h.db.QueryRowContext(r.Context(),
		"SELECT 1 FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		crewID, workspaceID).Scan(&exists)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Crew not found"})
		return
	}
	ipcPath := fmt.Sprintf("/crews/%s/files", crewID)
	sep := "?"
	if agentSlug := r.URL.Query().Get("agent_slug"); agentSlug != "" {
		ipcPath += sep + "agent_slug=" + url.QueryEscape(agentSlug)
		sep = "&"
	}
	if r.URL.Query().Get("recursive") == "true" {
		ipcPath += sep + "recursive=true"
		sep = "&"
	}
	if subdir := r.URL.Query().Get("subdir"); subdir != "" {
		ipcPath += sep + "subdir=" + url.QueryEscape(subdir)
	}
	resp, err := h.ipcGet(r.Context(), ipcPath)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Failed to fetch files"})
		return
	}
	defer resp.Body.Close()
	var data map[string]interface{}
	if json.NewDecoder(resp.Body).Decode(&data) == nil {
		if files, ok := data["files"]; ok {
			writeJSON(w, http.StatusOK, files)
			return
		}
	}
	writeJSON(w, http.StatusOK, []interface{}{})
}

func (h *ProxyHandler) CrewFileDownload(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("crewId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path parameter required"})
		return
	}
	var exists int
	err := h.db.QueryRowContext(r.Context(),
		"SELECT 1 FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		crewID, workspaceID).Scan(&exists)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Crew not found"})
		return
	}
	cleanPath := filepath.Clean(filePath)
	if strings.HasPrefix(cleanPath, "..") || filepath.IsAbs(cleanPath) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid file path"})
		return
	}
	ipcPath := fmt.Sprintf("/crews/%s/files/download?path=%s", crewID, url.QueryEscape(cleanPath))
	resp, err := h.ipcGet(r.Context(), ipcPath)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "File not found"})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "File not found"})
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", resp.Header.Get("Content-Disposition"))
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		w.Header().Set("Content-Length", cl)
	}
	if _, err := io.Copy(w, resp.Body); err != nil {
		h.logger.Debug("crew file download stream error", "error", err, "crew_id", crewID)
	}
}

func (h *ProxyHandler) CrewFileSave(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("crewId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path parameter required"})
		return
	}
	var exists int
	err := h.db.QueryRowContext(r.Context(),
		"SELECT 1 FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		crewID, workspaceID).Scan(&exists)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Crew not found"})
		return
	}
	cleanPath := filepath.Clean(filePath)
	if strings.HasPrefix(cleanPath, "..") || filepath.IsAbs(cleanPath) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid file path"})
		return
	}
	ipcPath := fmt.Sprintf("/crews/%s/files/save?path=%s", crewID, url.QueryEscape(cleanPath))
	resp, err := h.ipcPut(r.Context(), ipcPath, r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Failed to save file"})
		return
	}
	h.proxyJSON(w, resp)
}

func (h *ProxyHandler) AgentLogs(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())

	offset := r.URL.Query().Get("offset")
	if offset == "" {
		offset = "0"
	}
	limit := r.URL.Query().Get("limit")
	if limit == "" {
		limit = "100"
	}

	var crewID sql.NullString
	var slug string
	err := h.db.QueryRowContext(r.Context(),
		"SELECT crew_id, slug FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		agentID, workspaceID).Scan(&crewID, &slug)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent not found"})
		return
	}
	if !crewID.Valid {
		writeJSON(w, http.StatusOK, []interface{}{})
		return
	}

	path := fmt.Sprintf("/agents/%s/logs?crew_id=%s&offset=%s&limit=%s", slug, crewID.String, offset, limit)
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

func (h *ProxyHandler) AgentStop(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())

	if !canRole(role, "create") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	var exists string
	err := h.db.QueryRowContext(r.Context(),
		"SELECT id FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		agentID, workspaceID).Scan(&exists)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent not found"})
		return
	}

	// Try to stop via crewshipd (best effort)
	h.ipcPost(r.Context(), fmt.Sprintf("/agents/%s/stop", agentID), nil)

	now := time.Now().UTC().Format(time.RFC3339)
	res, err := h.db.ExecContext(r.Context(),
		"UPDATE agents SET status = 'STOPPED', updated_at = ? WHERE id = ? AND workspace_id = ?",
		now, agentID, workspaceID)
	if err != nil {
		h.logger.Error("update agent status to STOPPED", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent not found"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"id": agentID, "status": "STOPPED"})
}

func (h *ProxyHandler) ChatMessages(w http.ResponseWriter, r *http.Request) {
	chatID := r.PathValue("chatId")
	user := UserFromContext(r.Context())

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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	var memberRole string
	err = h.db.QueryRowContext(r.Context(),
		"SELECT role FROM workspace_members WHERE workspace_id = ? AND user_id = ?",
		chatWSID, user.ID).Scan(&memberRole)
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
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

	path := fmt.Sprintf("/chats/%s/messages?offset=%d&limit=%d", chatID, offset, limit)
	resp, err := h.ipcGet(r.Context(), path)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Failed to fetch messages"})
		return
	}
	h.proxyJSON(w, resp)
}
