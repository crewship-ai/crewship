package api

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
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
// Body: multipart/form-data with one "file" field (25 MB cap).
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
		// Forward the IPC diagnostic — but as a sentence, not as a nested
		// document. Bound the read: a runaway IPC error body shouldn't be
		// able to OOM us.
		buf, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		writeJSON(w, resp.StatusCode, map[string]string{"error": attachmentSaveErrorMessage(buf)})
		return
	}

	// Record the metadata row (#1768 item 7).
	//
	// Until now this endpoint wrote bytes and nothing else: a chat attachment had
	// no recorded size, checksum, MIME type or uploader, could not be listed and
	// could not be deleted through any API. The docs claimed otherwise and were
	// corrected on this branch. This is the other half of that correction.
	//
	// The BLOB LOCATION is deliberately unchanged. It stays at
	// <crewID>/<agentSlug>/attachments/<chatId>/<filename>, because that path is
	// the agent-visible contract — the file appears in the container at
	// /output/<agentSlug>/... and prompts, tools and users already reference it
	// there. Moving it to the content-addressed layout the issue attachments use
	// would break every one of those for no user-visible gain. `storage_key`
	// exists on the row precisely so the two layouts can differ; it is the
	// authority on where a given attachment lives.
	//
	// One consequence, named rather than discovered later: because these blobs
	// are not content-addressed, they are outside the refcounted-unlink and
	// reclaim machinery. reclaimAttachmentBlobs walks only
	// attachments/<workspace>/ and will not touch them. Deleting a chat
	// attachment's bytes is the crew-files surface's job, as it is today.
	//
	// Best-effort, after the bytes have landed: the upload is the thing the
	// caller asked for, and a failed bookkeeping row must not turn a stored file
	// into an error.
	h.recordChatAttachment(r, workspaceID, chatID, filename, fullPath, body)

	writeJSON(w, http.StatusCreated, map[string]any{
		"filename": filename,
		"size":     len(body),
		"path":     relPath,
		// Agent-side absolute path — handy for the LLM prompt so the
		// agent can read the attachment without guessing.
		"agent_path": "/output/" + slug.String + "/" + relPath,
	})
}

// attachmentSaveErrorMessage turns the IPC save endpoint's error body into one
// sentence the composer can put in a toast.
//
// Two things were wrong with forwarding it verbatim. The IPC layer answers in
// JSON, so `{"error": string(buf)}` nested a whole document inside a string
// field — the user's toast read `{"error":"failed to save file"}`, braces and
// all. And nothing in that text said which operation failed: the IPC route
// serves the file editor and `crewship agent file-write` as well, so it can
// only describe the DESTINATION ("the agent's output directory is owned by the
// crew runtime… start the crew and retry"). Naming the attachment is this
// layer's job, because this is the only layer that knows that is what the
// caller was doing.
//
// A body that isn't the expected shape is forwarded as text rather than
// dropped — an unexpected body is still the best diagnostic available.
func attachmentSaveErrorMessage(body []byte) string {
	msg := strings.TrimSpace(string(body))
	var env struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err == nil && strings.TrimSpace(env.Error) != "" {
		msg = strings.TrimSpace(env.Error)
	}
	if msg == "" {
		return "failed to save attachment"
	}
	return "failed to save attachment: " + msg
}

// recordChatAttachment writes the metadata row for a chat attachment.
//
// It mirrors the issue path's rules where they are about the DATA — the
// filename is already a sanitised basename by the time it gets here, the
// content type is resolved from the extension against the shared allowlist, and
// the digest is computed over the bytes that were actually stored — and departs
// from it only on the blob location, which is the caller's (see the call site).
//
// An extension outside the allowlist does NOT refuse the upload. The bytes are
// already stored, the endpoint has shipped for releases with no type restriction
// at all, and turning an established chat feature into a 415 as a side effect of
// adding metadata would be a breaking change smuggled into a bookkeeping one.
// The row records what the file actually is (application/octet-stream for an
// unrecognised extension) rather than pretending it was refused. Tightening the
// chat surface is its own change, with its own release note.
func (h *ProxyHandler) recordChatAttachment(r *http.Request, workspaceID, chatID, filename, storageKey string, body []byte) {
	contentType, err := resolveAttachmentType(filename)
	if err != nil {
		contentType = "application/octet-stream"
	}
	sum := sha256.Sum256(body)
	var userID string
	if u := UserFromContext(r.Context()); u != nil {
		userID = u.ID
	}
	_, err = h.db.ExecContext(r.Context(), `
		INSERT INTO attachments
			(id, workspace_id, owner_type, chat_id, filename, content_type, size_bytes,
			 sha256, storage_key, uploaded_by_user_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		generateCUID(), workspaceID, string(attachmentOwnerChat), chatID, filename, contentType,
		len(body), hex.EncodeToString(sum[:]), storageKey, nullIfEmpty(userID),
		time.Now().UTC().Format(time.RFC3339))
	if err == nil {
		return
	}
	if strings.Contains(strings.ToUpper(err.Error()), "UNIQUE") {
		// The same FILE — same bytes, same name — was already attached to this
		// chat. The partial unique index is doing its job; re-uploading a file is
		// not an error and the bytes on disk are byte-identical to what is
		// already recorded.
		//
		// The name is part of that key since
		// 20260806214500_attachments_dedupe_filename.sql, and on this surface it
		// has to be: a chat blob's path CONTAINS the filename, so two
		// differently-named uploads of identical bytes are two files on disk. The
		// old (chat_id, sha256) key recorded one row for them and left the second
		// file with no row at all — the exact gap this call site was added to
		// close.
		return
	}
	if h.logger != nil {
		h.logger.Error("record chat attachment", "chat_id", chatID, "error", err)
	}
}
