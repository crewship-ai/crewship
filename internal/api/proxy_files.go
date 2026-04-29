package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
)

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
		cleanSub := filepath.Clean(subdir)
		if strings.HasPrefix(cleanSub, "..") || filepath.IsAbs(cleanSub) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid subdir path"})
			return
		}
		ipcPath += "&subdir=" + url.QueryEscape(cleanSub)
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

// AgentFileDownload streams a file from an agent's working directory.
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

// AgentFileSave uploads and saves a file to an agent's working directory.
func (h *ProxyHandler) AgentFileSave(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	// V-21: Require create permission for file save operations
	if !canRole(role, "create") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}
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

// CrewFiles lists files in a crew's shared directory via the sidecar.
func (h *ProxyHandler) CrewFiles(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("crewId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	found, err := crewExists(r.Context(), h.db, crewID, workspaceID)
	if err != nil {
		h.logger.Error("crew exists check", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if !found {
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

// CrewFileDownload streams a file from a crew's shared directory.
func (h *ProxyHandler) CrewFileDownload(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("crewId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path parameter required"})
		return
	}
	found, err := crewExists(r.Context(), h.db, crewID, workspaceID)
	if err != nil {
		h.logger.Error("crew exists check", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if !found {
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

// CrewFileSave uploads and saves a file to a crew's shared directory.
func (h *ProxyHandler) CrewFileSave(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("crewId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	// V-21: Require create permission for file save operations
	if !canRole(role, "create") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}
	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path parameter required"})
		return
	}
	found, err := crewExists(r.Context(), h.db, crewID, workspaceID)
	if err != nil {
		h.logger.Error("crew exists check", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if !found {
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

// AgentLogs returns collected log entries for a running agent.

func (h *ProxyHandler) AgentContainerFiles(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())

	var crewID sql.NullString
	err := h.db.QueryRowContext(r.Context(),
		"SELECT crew_id FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		agentID, workspaceID).Scan(&crewID)
	if err != nil || !crewID.Valid {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent not found or not assigned to a crew"})
		return
	}

	ipcPath := fmt.Sprintf("/crews/%s/container-files", crewID.String)
	if subdir := r.URL.Query().Get("subdir"); subdir != "" {
		ipcPath += "?subdir=" + url.QueryEscape(subdir)
	}
	resp, err := h.ipcGet(r.Context(), ipcPath)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Failed to fetch container files"})
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

// AgentGitLog fetches recent git commits from inside the agent's container.
