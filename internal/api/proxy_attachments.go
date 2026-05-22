package api

import (
	"bytes"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
)

// AgentChatAttachment handles file uploads attached to a specific chat
// session. Files land at:
//
//	<storage-root>/<crewID>/<agentSlug>/attachments/<chatId>/<filename>
//
// which surfaces inside the agent container as
// /output/<agentSlug>/attachments/<chatId>/<filename> — visible in the
// Files panel and writable from the agent's normal working dir.
//
// POST /api/v1/agents/{agentId}/chats/{chatId}/attachments
//
// Body: multipart/form-data with one "file" field (10 MB cap).
// Returns: {filename, size, path} where path is the relative path the
//
//	agent can use (e.g. "attachments/<chatId>/photo.png").
func (h *ProxyHandler) AgentChatAttachment(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	chatID := r.PathValue("chatId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	if agentID == "" || chatID == "" {
		replyError(w, http.StatusBadRequest, "agentId and chatId required")
		return
	}

	// Resolve the agent (must belong to workspace) + its crew. Cross-
	// tenant lookups return 404 (indistinguishable from "missing").
	var slug, crewID sql.NullString
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT slug, crew_id FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		agentID, workspaceID).Scan(&slug, &crewID); err != nil || !crewID.Valid {
		replyError(w, http.StatusNotFound, "Agent not found")
		return
	}

	// Verify the chat belongs to this agent so a stray chatID can't
	// land files in another agent's namespace.
	var ownerAgent string
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT agent_id FROM chats WHERE id = ? AND workspace_id = ?", chatID, workspaceID).Scan(&ownerAgent); err != nil {
		replyError(w, http.StatusNotFound, "Chat not found")
		return
	}
	if ownerAgent != agentID {
		replyError(w, http.StatusForbidden, "chat not scoped to this agent")
		return
	}

	// 25 MB upload cap — best-practice for chat attachments. Bigger
	// than the crew-messaging WriteFile (10 MB) which is for inter-
	// crew transfers; the chat composer needs headroom for log dumps
	// and screenshots without hitting the cap.
	const maxBytes = 25 << 20
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	if err := r.ParseMultipartForm(maxBytes); err != nil {
		replyError(w, http.StatusBadRequest, "invalid multipart form or file too large (max 25MB)")
		return
	}
	// ParseMultipartForm spills uploads near the size limit to disk —
	// without this defer those temp files accumulate under repeated
	// uploads.
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()
	file, header, err := r.FormFile("file")
	if err != nil {
		replyError(w, http.StatusBadRequest, "file field is required")
		return
	}
	defer file.Close()

	// Sanitise filename: keep only basename (strip directory components),
	// reject empties, hidden, and traversal attempts. Length cap mirrors
	// most filesystems (255 bytes).
	filename := filepath.Base(header.Filename)
	if filename == "" || filename == "." || filename == ".." || strings.ContainsAny(filename, "/\\") {
		replyError(w, http.StatusBadRequest, "invalid filename")
		return
	}
	if len(filename) > 255 {
		replyError(w, http.StatusBadRequest, "filename too long")
		return
	}

	// Build the full storage path the IPC layer expects (includes
	// crewID + agent slug prefix). Subpath under the agent's namespace
	// keeps each chat's attachments cleanly separated.
	relPath := fmt.Sprintf("attachments/%s/%s", chatID, filename)
	fullPath := fmt.Sprintf("%s/%s/%s", crewID.String, slug.String, relPath)

	// Stream the upload body to the IPC save endpoint; localfs.Write
	// handles MkdirAll + permissions on the host side. Using a buffer
	// (rather than a pipe) keeps the request simple — 10 MB cap means
	// memory pressure stays bounded.
	body, err := io.ReadAll(file)
	if err != nil {
		replyError(w, http.StatusBadRequest, "read upload body")
		return
	}
	ipcPath := fmt.Sprintf("/crews/%s/files/save?path=%s", url.PathEscape(crewID.String), url.QueryEscape(fullPath))
	resp, err := h.ipcPut(r.Context(), ipcPath, bytes.NewReader(body))
	if err != nil {
		replyError(w, http.StatusBadGateway, "Failed to save attachment")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		// Forward the IPC error verbatim — gives operators a usable
		// diagnostic without leaking internals. Bound the read: a
		// runaway IPC error body shouldn't be able to OOM us.
		buf, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		writeJSON(w, resp.StatusCode, map[string]string{"error": string(buf)})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"filename": filename,
		"size":     len(body),
		"path":     relPath,
		// Agent-side absolute path — handy for the LLM prompt so the
		// agent can read the attachment without guessing.
		"agent_path": "/output/" + slug.String + "/" + relPath,
	})
}
