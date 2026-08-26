package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

func (h *ProxyHandler) AgentFiles(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())

	// Audit M13: every read of agent artefacts must require at least
	// the "read" capability. Without the gate any authenticated
	// workspace member -- including the VIEWER role used for guests --
	// can list and download agent files / logs, which the role matrix
	// elsewhere in this package treats as a privileged surface. Empty
	// role is denied by canRole, so this also fails closed when a
	// middleware regression strips the role from the context.
	role := RoleFromContext(r.Context())
	if !canRole(role, "read") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	var slug, crewID sql.NullString
	err := h.db.QueryRowContext(r.Context(),
		"SELECT slug, crew_id FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		agentID, workspaceID).Scan(&slug, &crewID)
	if err != nil {
		replyError(w, http.StatusNotFound, "Agent not found")
		return
	}
	if !crewID.Valid {
		writeJSON(w, http.StatusOK, []interface{}{})
		return
	}

	ipcPath := fmt.Sprintf("/crews/%s/files?agent_slug=%s", url.PathEscape(crewID.String), url.QueryEscape(slug.String))
	if r.URL.Query().Get("recursive") == "true" {
		ipcPath += "&recursive=true"
	}
	if subdir := r.URL.Query().Get("subdir"); subdir != "" {
		cleanSub, ok := normalizeRequestPath(subdir)
		if !ok {
			replyError(w, http.StatusBadRequest, "Invalid subdir path")
			return
		}
		ipcPath += "&subdir=" + url.QueryEscape(cleanSub)
	}
	resp, err := h.ipcGet(r.Context(), ipcPath)
	if err != nil {
		replyError(w, http.StatusBadGateway, "Failed to fetch files")
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

// forwardFileDownloadError maps crewshipd's download status onto the public
// API's, preserving the distinction between absent and unreadable. Anything
// unrecognised stays 404, which is the historical answer.
func forwardFileDownloadError(w http.ResponseWriter, status int) {
	switch status {
	case http.StatusForbidden:
		replyError(w, http.StatusForbidden, "File is not readable by the server")
	case http.StatusConflict:
		replyError(w, http.StatusConflict,
			"File exists but could not be read — the crew container is not running")
	default:
		replyError(w, http.StatusNotFound, "File not found")
	}
}

// resolveAgentFilePath turns whatever the FE sent into a full storage key, or
// says why it may not.
//
// Three shapes arrive here, and all three are legitimate:
//
//	"workspace/x.toml"                     relative — gets this agent's prefix
//	"<crewID>/<slug>/workspace/x.toml"     this agent's own namespace
//	"<crewID>/report.md"                   the CREW ROOT
//
// The third is the one that used to be refused, and refusing it was a
// straightforward contradiction with the listing: handleFileList, when given
// an agent_slug, deliberately merges the crew-root files into that agent's
// result — its own comment says "files the agent saved to /output/ instead of
// /output/<agent-slug>/". So the list handed out `<crewID>/report.md` and the
// download answered 403 "path not scoped to this agent" for it. That is not a
// rare corner: an agent told to write a report puts it in the working
// directory, which IS the crew root, so it was the common case.
//
// What the 403 legitimately guards is a SIBLING agent's namespace —
// "<crewID>/<other-slug>/notes.md" — and that is still refused. Crew root is
// shared by construction: every agent in the crew writes there and every
// agent's listing shows it, so an agent of this crew reading it leaks nothing
// the list did not already offer. Cross-CREW is unreachable either way; crewID
// comes from this agent's own row.
func resolveAgentFilePath(crewID, slug, cleanPath string) (string, bool) {
	prefix := crewID + "/" + slug + "/"
	if strings.HasPrefix(cleanPath, prefix) {
		return cleanPath, true
	}
	if rest, isCrewScoped := strings.CutPrefix(cleanPath, crewID+"/"); isCrewScoped {
		// A crew-root FILE has no further separator. Anything with one names a
		// directory under the crew — a sibling agent's namespace — and is not
		// this agent's to touch.
		if rest != "" && !strings.Contains(rest, "/") {
			return cleanPath, true
		}
		return "", false
	}
	return prefix + cleanPath, true
}

// AgentFileDownload streams a file from an agent's working directory.
//
// The FE may send EITHER a relative path (e.g. "workspace/demo/config/x.toml")
// or a full storage-rooted path (e.g. "<crewID>/<slug>/workspace/demo/x.toml")
// — list responses include the full form, so this used to 404 when the FE
// passed `entry.path` straight back. We accept both: relative paths get the
// `<crewID>/<slug>/` prefix prepended; full paths are validated to ensure
// they're scoped to THIS agent (no peeking at sibling agents' files).
func (h *ProxyHandler) AgentFileDownload(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	filePath := r.URL.Query().Get("path")

	// Same role gate as AgentFiles -- see audit M13 commentary there.
	role := RoleFromContext(r.Context())
	if !canRole(role, "read") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	if filePath == "" {
		replyError(w, http.StatusBadRequest, "path parameter required")
		return
	}

	var slug, crewID sql.NullString
	err := h.db.QueryRowContext(r.Context(),
		"SELECT slug, crew_id FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		agentID, workspaceID).Scan(&slug, &crewID)
	if err != nil || !crewID.Valid {
		replyError(w, http.StatusNotFound, "Agent not found")
		return
	}

	cleanPath, ok := normalizeRequestPath(filePath)
	if !ok {
		replyError(w, http.StatusBadRequest, "Invalid file path")
		return
	}

	fullPath, allowed := resolveAgentFilePath(crewID.String, slug.String, cleanPath)
	if !allowed {
		replyError(w, http.StatusForbidden, "path not scoped to this agent")
		return
	}

	ipcPath := fmt.Sprintf("/crews/%s/files/download?path=%s", url.PathEscape(crewID.String), url.QueryEscape(fullPath))

	resp, err := h.ipcGet(r.Context(), ipcPath)
	if err != nil {
		replyError(w, http.StatusNotFound, "File not found")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Do not flatten every non-200 into 404.
		//
		// crewshipd distinguishes "the bytes are not there" from "the bytes
		// are there and neither the host nor the container could hand them
		// over" (routes_files.go). Collapsing the second into the first is
		// what made the agent-file defect unreadable from the outside: the
		// panel listed a tree and said every entry in it was missing.
		forwardFileDownloadError(w, resp.StatusCode)
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
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	filePath := r.URL.Query().Get("path")

	if filePath == "" {
		replyError(w, http.StatusBadRequest, "path parameter required")
		return
	}

	var slug, crewID sql.NullString
	err := h.db.QueryRowContext(r.Context(),
		"SELECT slug, crew_id FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		agentID, workspaceID).Scan(&slug, &crewID)
	if err != nil || !crewID.Valid {
		replyError(w, http.StatusNotFound, "Agent not found")
		return
	}

	cleanPath, ok := normalizeRequestPath(filePath)
	if !ok {
		replyError(w, http.StatusBadRequest, "Invalid file path")
		return
	}

	// Same resolver as the download, so a file the editor could OPEN is never
	// one it then refuses to SAVE.
	fullPath, allowed := resolveAgentFilePath(crewID.String, slug.String, cleanPath)
	if !allowed {
		replyError(w, http.StatusForbidden, "path not scoped to this agent")
		return
	}

	ipcPath := fmt.Sprintf("/crews/%s/files/save?path=%s", url.PathEscape(crewID.String), url.QueryEscape(fullPath))

	resp, err := h.ipcPut(r.Context(), ipcPath, r.Body)
	if err != nil {
		replyError(w, http.StatusBadGateway, "Failed to save file")
		return
	}
	h.proxyJSON(w, resp)
}

// CrewFiles lists files in a crew's shared directory via the sidecar.
func (h *ProxyHandler) CrewFiles(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("crewId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	// Audit #495 follow-up: read-tier gate -- the existing
	// crewExists check tests workspace scope but not role.
	if !canRole(RoleFromContext(r.Context()), "read") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	found, err := crewExists(r.Context(), h.db, crewID, workspaceID)
	if err != nil {
		replyInternalError(w, h.logger, "crew exists check", err)
		return
	}
	if !found {
		replyError(w, http.StatusNotFound, "Crew not found")
		return
	}
	ipcPath := fmt.Sprintf("/crews/%s/files", url.PathEscape(crewID))
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
		cleanSub, ok := normalizeRequestPath(subdir)
		if !ok {
			replyError(w, http.StatusBadRequest, "Invalid subdir path")
			return
		}
		ipcPath += sep + "subdir=" + url.QueryEscape(cleanSub)
	}
	resp, err := h.ipcGet(r.Context(), ipcPath)
	if err != nil {
		replyError(w, http.StatusBadGateway, "Failed to fetch files")
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
	// Audit #495 follow-up: read-tier gate.
	if !canRole(RoleFromContext(r.Context()), "read") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		replyError(w, http.StatusBadRequest, "path parameter required")
		return
	}
	found, err := crewExists(r.Context(), h.db, crewID, workspaceID)
	if err != nil {
		replyInternalError(w, h.logger, "crew exists check", err)
		return
	}
	if !found {
		replyError(w, http.StatusNotFound, "Crew not found")
		return
	}
	cleanPath, ok := normalizeRequestPath(filePath)
	if !ok {
		replyError(w, http.StatusBadRequest, "Invalid file path")
		return
	}
	ipcPath := fmt.Sprintf("/crews/%s/files/download?path=%s", url.PathEscape(crewID), url.QueryEscape(cleanPath))
	resp, err := h.ipcGet(r.Context(), ipcPath)
	if err != nil {
		replyError(w, http.StatusNotFound, "File not found")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Do not flatten every non-200 into 404.
		//
		// crewshipd distinguishes "the bytes are not there" from "the bytes
		// are there and neither the host nor the container could hand them
		// over" (routes_files.go). Collapsing the second into the first is
		// what made the agent-file defect unreadable from the outside: the
		// panel listed a tree and said every entry in it was missing.
		forwardFileDownloadError(w, resp.StatusCode)
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
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		replyError(w, http.StatusBadRequest, "path parameter required")
		return
	}
	found, err := crewExists(r.Context(), h.db, crewID, workspaceID)
	if err != nil {
		replyInternalError(w, h.logger, "crew exists check", err)
		return
	}
	if !found {
		replyError(w, http.StatusNotFound, "Crew not found")
		return
	}
	cleanPath, ok := normalizeRequestPath(filePath)
	if !ok {
		replyError(w, http.StatusBadRequest, "Invalid file path")
		return
	}
	ipcPath := fmt.Sprintf("/crews/%s/files/save?path=%s", url.PathEscape(crewID), url.QueryEscape(cleanPath))
	resp, err := h.ipcPut(r.Context(), ipcPath, r.Body)
	if err != nil {
		replyError(w, http.StatusBadGateway, "Failed to save file")
		return
	}
	h.proxyJSON(w, resp)
}

// CrewFileDelete removes a file from a crew's shared directory via the sidecar.
func (h *ProxyHandler) CrewFileDelete(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("crewId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	// Same RBAC as the upload route (CrewFileSave): mutating a crew's shared
	// files requires the "create" tier.
	if !canRole(role, "create") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		replyError(w, http.StatusBadRequest, "path parameter required")
		return
	}
	found, err := crewExists(r.Context(), h.db, crewID, workspaceID)
	if err != nil {
		replyInternalError(w, h.logger, "crew exists check", err)
		return
	}
	if !found {
		replyError(w, http.StatusNotFound, "Crew not found")
		return
	}
	cleanPath, ok := normalizeRequestPath(filePath)
	if !ok {
		replyError(w, http.StatusBadRequest, "Invalid file path")
		return
	}
	ipcPath := fmt.Sprintf("/crews/%s/files/delete?path=%s", url.PathEscape(crewID), url.QueryEscape(cleanPath))
	resp, err := h.ipcDelete(r.Context(), ipcPath)
	if err != nil {
		replyError(w, http.StatusBadGateway, "Failed to delete file")
		return
	}
	h.proxyJSON(w, resp)
}

// AgentLogs returns collected log entries for a running agent.

func (h *ProxyHandler) AgentContainerFiles(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	// Audit #495 follow-up: read-tier gate.
	if !canRole(RoleFromContext(r.Context()), "read") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	var crewID sql.NullString
	err := h.db.QueryRowContext(r.Context(),
		"SELECT crew_id FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		agentID, workspaceID).Scan(&crewID)
	if err != nil || !crewID.Valid {
		replyError(w, http.StatusNotFound, "Agent not found or not assigned to a crew")
		return
	}

	ipcPath := fmt.Sprintf("/crews/%s/container-files", url.PathEscape(crewID.String))
	if subdir := r.URL.Query().Get("subdir"); subdir != "" {
		cleanSub, ok := normalizeRequestPath(subdir)
		if !ok {
			replyError(w, http.StatusBadRequest, "Invalid subdir path")
			return
		}
		ipcPath += "?subdir=" + url.QueryEscape(cleanSub)
	}
	resp, err := h.ipcGet(r.Context(), ipcPath)
	if err != nil {
		replyError(w, http.StatusBadGateway, "Failed to fetch container files")
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
