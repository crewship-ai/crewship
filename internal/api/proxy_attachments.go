package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Chat attachments — the upload, the list and the delete.
//
// ── The location contract ──────────────────────────────────────────────────
//
// A chat attachment is the one attachment kind whose PATH is a product
// contract rather than an implementation detail. The file surfaces inside the
// agent container at
//
//	/output/<agentSlug>/attachments/<chatId>/<attachmentId>/<filename>
//
// and prompts, lib/attachment-message.ts and the ask-form renderer all put
// that string in front of the agent. It is therefore never content-addressed:
// a digest-named path would be unreadable to the reader it exists for.
//
// ── What the attachment id segment is doing there ──────────────────────────
//
// It used to be attachments/<chatId>/<filename>, and the filename was the
// storage identity. Uploading different bytes under one name overwrote the
// blob, while the metadata was de-duplicated by (chat, sha256, filename) — so
// two rows could name one storage_key, each claiming a different checksum,
// with only the last bytes surviving. A message's path did not identify the
// version the agent read, and neither provenance nor deletion could be built
// on top of it (chat surface audit 2026-08-13, P0.2).
//
// The identity is the ROW ID, not the digest, and it is the id that makes the
// location unique. That choice is made against the reclaim story: content
// addressing is what forces the refcount ("may these bytes go? only if no
// other row names them"), and a refcount is exactly what this arc must not
// have, because its blob path has to stay readable and per-chat. One upload,
// one id, one directory, one file — so deleting an attachment is unlinking its
// own bytes, with nothing to arbitrate. The digest stays on the row as the
// integrity witness for what the agent read; the id is the identity.
//
// The filename stays the last segment, so everything that made the old path
// legible still holds.
//
// ── The publication order ──────────────────────────────────────────────────
//
// Metadata first, bytes second, promotion third. See reserveChatAttachment and
// the migration 20260813212500_chat_attachment_publication_state.sql. The rule
// the whole surface rests on:
//
//	a 201 means there is a `stored` row AND bytes at its storage_key.
//
// Every failure leaves a `pending` row — invisible to the list, never returned
// to a caller, reclaimable with its bytes by the collector in
// attachments_gc.go. The direction that used to be possible, bytes with no
// row, is gone: nothing is written to storage before a row names it.

// Attachment publication states. The domain is enforced here rather than by a
// CHECK constraint (SQLite cannot ALTER one on), and both readers fail safe on
// an unrecognised value: the list matches `stored` exactly, the reclaimer
// matches everything that is not `stored`.
const (
	attachmentStatePending = "pending"
	attachmentStateStored  = "stored"
)

// AgentChatAttachment handles file uploads attached to a specific chat session.
//
// POST /api/v1/agents/{agentId}/chats/{chatId}/attachments
//
// Body: multipart/form-data with one "file" field (25 MB cap).
// Returns: {filename, size, path, agent_path}. That shape is consumed by the
// composer and is deliberately unchanged by the identity work above — only the
// VALUE of `path` gained a segment.
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

	// The upload keeps its historical 403 for a chat that exists but belongs to
	// another agent; the list and delete below use the 404 every other chat
	// route answers. The divergence is deliberate and narrow — the upload's 403
	// is a pinned contract (webhook_proxy_mission_cov_test.go) and changing it
	// is not this change's business.
	scope, ok := h.resolveChatScope(w, r, agentID, chatID, workspaceID, http.StatusForbidden)
	if !ok {
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

	// Buffer the upload: the digest has to be known BEFORE the row is written,
	// and the row has to be written before the bytes are published. 25 MB is
	// the cap, so memory pressure stays bounded.
	body, err := io.ReadAll(file)
	if err != nil {
		replyError(w, http.StatusBadRequest, "read upload body")
		return
	}
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])

	// (1) Reserve the identity. Nothing reaches storage until this row exists.
	ref, err := h.reserveChatAttachment(r, workspaceID, chatID, filename, digest, len(body), scope)
	if err != nil {
		replyInternalError(w, h.logger, "reserve chat attachment", err)
		return
	}

	// (2) Publish the bytes at the location the row records. localfs.Write
	// handles MkdirAll + permissions on the host side.
	ipcPath := fmt.Sprintf("/crews/%s/files/save?path=%s",
		url.PathEscape(scope.crewID), url.QueryEscape(ref.storageKey))
	resp, err := h.ipcPut(r.Context(), ipcPath, bytes.NewReader(body))
	if err != nil {
		h.abandonChatAttachment(r, ref)
		replyError(w, http.StatusBadGateway, "Failed to save attachment")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		h.abandonChatAttachment(r, ref)
		// Forward the IPC diagnostic — but as a sentence, not as a nested
		// document. Bound the read: a runaway IPC error body shouldn't be
		// able to OOM us.
		buf, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		writeJSON(w, resp.StatusCode, map[string]string{"error": attachmentSaveErrorMessage(buf)})
		return
	}

	// (3) Publish the row. Until this commits the attachment does not exist as
	// far as any reader is concerned, and a failure here is a failure of the
	// request — answering 201 for a row we could not promote would put the
	// "success means recorded" rule back where it was.
	if err := h.publishChatAttachment(r, ref); err != nil {
		replyInternalError(w, h.logger, "publish chat attachment", err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"filename": filename,
		"size":     len(body),
		"path":     ref.relPath,
		// Agent-side absolute path — handy for the LLM prompt so the
		// agent can read the attachment without guessing.
		"agent_path": chatAttachmentAgentPath(scope.slug, ref.relPath),
	})
}

// ListAgentChatAttachments returns every published attachment on one chat.
//
// GET /api/v1/agents/{agentId}/chats/{chatId}/attachments
//
// Scope and status codes mirror the neighbouring chat routes (ListChats,
// MarkChatRead, UpdateChat): the workspace comes from the request context and
// never from the query, and a chat that is not this agent's — or not this
// workspace's — is a 404 that does not distinguish "yours but mis-nested" from
// "someone else's" from "never existed".
func (h *ProxyHandler) ListAgentChatAttachments(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	chatID := r.PathValue("chatId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	if agentID == "" || chatID == "" {
		replyError(w, http.StatusBadRequest, "agentId and chatId required")
		return
	}
	scope, ok := h.resolveChatScope(w, r, agentID, chatID, workspaceID, http.StatusNotFound)
	if !ok {
		return
	}

	rows, err := h.db.QueryContext(r.Context(), chatAttachmentSelect+`
		WHERE a.chat_id = ? AND a.workspace_id = ? AND a.owner_type = ? AND a.state = ?
		ORDER BY a.created_at DESC, a.id DESC`,
		chatID, workspaceID, string(attachmentOwnerChat), attachmentStateStored)
	if err != nil {
		replyInternalError(w, h.logger, "list chat attachments", err)
		return
	}
	defer rows.Close()

	result := []chatAttachmentResponse{}
	for rows.Next() {
		item, err := scanChatAttachment(rows, scope)
		if err != nil {
			replyInternalError(w, h.logger, "scan chat attachment", err)
			return
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		replyInternalError(w, h.logger, "rows iteration (chat attachments)", err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// DeleteAgentChatAttachment removes one attachment: the bytes and the row.
//
// DELETE /api/v1/agents/{agentId}/chats/{chatId}/attachments/{attachmentId}
//
// IDEMPOTENT, and 204 either way. An attachment that is already gone is not an
// error — the caller asked for "this is not attached any more" and after the
// call it is not. The refusal that matters happens one step earlier: an
// attachment in another workspace is reachable only through a chat in that
// workspace, and that chat 404s here, so a cross-tenant delete never reaches
// the row at all.
//
// The bytes go FIRST and the row second. The other order — row, then
// best-effort unlink, as DeleteChat does for a whole chat directory — would
// on a failed unlink leave bytes that no row names, in a tree the
// content-addressed sweep deliberately never walks: permanently unreachable.
// This way a failure leaves a row whose bytes are gone, which is visible,
// truthful, and repaired by retrying the same call.
func (h *ProxyHandler) DeleteAgentChatAttachment(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	chatID := r.PathValue("chatId")
	attachmentID := r.PathValue("attachmentId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	if !canRole(RoleFromContext(r.Context()), "create") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	if agentID == "" || chatID == "" || attachmentID == "" {
		replyError(w, http.StatusBadRequest, "agentId, chatId and attachmentId required")
		return
	}
	scope, ok := h.resolveChatScope(w, r, agentID, chatID, workspaceID, http.StatusNotFound)
	if !ok {
		return
	}

	var storageKey string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT storage_key FROM attachments
		  WHERE id = ? AND chat_id = ? AND workspace_id = ? AND owner_type = ?`,
		attachmentID, chatID, workspaceID, string(attachmentOwnerChat)).Scan(&storageKey)
	if errors.Is(err, sql.ErrNoRows) {
		// Already gone (or never existed under this chat). Idempotent.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "load chat attachment for delete", err)
		return
	}

	ipcPath := fmt.Sprintf("/crews/%s/files/delete?path=%s",
		url.PathEscape(scope.crewID), url.QueryEscape(chatAttachmentUnlinkTarget(storageKey, attachmentID)))
	resp, err := h.ipcDelete(r.Context(), ipcPath)
	if err != nil {
		replyError(w, http.StatusBadGateway, "Failed to delete attachment")
		return
	}
	defer resp.Body.Close()
	// A blob that is not there is a successful delete: the storage layer's
	// removal is a RemoveAll, so "already gone" arrives as 200 and only a real
	// failure (permissions, a stopped crew that owns the tree) is a 4xx/5xx.
	if resp.StatusCode >= 400 {
		buf, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		writeJSON(w, resp.StatusCode, map[string]string{"error": attachmentDeleteErrorMessage(buf)})
		return
	}

	if _, err := h.db.ExecContext(r.Context(),
		`DELETE FROM attachments WHERE id = ? AND chat_id = ? AND workspace_id = ?`,
		attachmentID, chatID, workspaceID); err != nil {
		replyInternalError(w, h.logger, "delete chat attachment", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── scope ──────────────────────────────────────────────────────────────────

// chatAttachmentScope is what the storage path is built from — the crew that
// owns the tree and the agent slug that names the namespace inside it —
// resolved together with the proof that the chat is this agent's and this
// workspace's.
type chatAttachmentScope struct {
	crewID string
	slug   string
}

// resolveChatScope answers the two tenancy questions every chat attachment
// route asks, and replies itself when the answer is no (ok=false means a
// response has already been written).
//
// mismatchStatus is the code for "the chat exists in this workspace but under
// a different agent". Everything else — an agent outside the workspace, a
// missing agent, an agent with no crew, a chat outside the workspace — is a
// 404, indistinguishable from "never existed", so a caller cannot use this
// route to probe another tenant's ids.
func (h *ProxyHandler) resolveChatScope(w http.ResponseWriter, r *http.Request, agentID, chatID, workspaceID string, mismatchStatus int) (chatAttachmentScope, bool) {
	var slug, crewID sql.NullString
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT slug, crew_id FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		agentID, workspaceID).Scan(&slug, &crewID); err != nil || !crewID.Valid {
		replyError(w, http.StatusNotFound, "Agent not found")
		return chatAttachmentScope{}, false
	}

	// Verify the chat belongs to this agent so a stray chatID can't
	// land files in — or read files out of — another agent's namespace.
	var ownerAgent string
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT agent_id FROM chats WHERE id = ? AND workspace_id = ?", chatID, workspaceID).Scan(&ownerAgent); err != nil {
		replyError(w, http.StatusNotFound, "Chat not found")
		return chatAttachmentScope{}, false
	}
	if ownerAgent != agentID {
		if mismatchStatus == http.StatusForbidden {
			replyError(w, http.StatusForbidden, "chat not scoped to this agent")
		} else {
			replyError(w, http.StatusNotFound, "Chat not found")
		}
		return chatAttachmentScope{}, false
	}
	return chatAttachmentScope{crewID: crewID.String, slug: slug.String}, true
}

// ── paths ──────────────────────────────────────────────────────────────────

// chatAttachmentRelPath is the agent-relative location of one attachment:
// attachments/<chatId>/<attachmentId>/<filename>.
//
// The id sits between the chat and the filename rather than replacing either:
// the chat segment keeps a conversation's files together (and is what
// cleanupChatAttachments removes wholesale when a chat is deleted), and the
// filename stays last so the path an agent is handed still says what the file
// is.
func chatAttachmentRelPath(chatID, attachmentID, filename string) string {
	return fmt.Sprintf("attachments/%s/%s/%s", chatID, attachmentID, filename)
}

// chatAttachmentAgentPath is the same location as the agent sees it inside its
// container.
func chatAttachmentAgentPath(slug, relPath string) string {
	return "/output/" + slug + "/" + relPath
}

// chatAttachmentUnlinkTarget is what a delete removes: the attachment's own
// DIRECTORY when it has one, and otherwise just the file.
//
// A current key ends .../attachments/<chatId>/<attachmentId>/<filename>, so the
// parent directory belongs to this attachment and nothing else — removing it
// leaves no empty directory behind for every deleted upload. A LEGACY key ends
// .../attachments/<chatId>/<filename>, where the parent is the chat's SHARED
// directory; removing that would take every other attachment in the
// conversation with it. The id check is what tells the two apart, and it is a
// comparison rather than a count of segments so a filename containing the id,
// or a chat id that looks like one, cannot fool it.
func chatAttachmentUnlinkTarget(storageKey, attachmentID string) string {
	dir := path.Dir(storageKey)
	if attachmentID != "" && path.Base(dir) == attachmentID {
		return dir
	}
	return storageKey
}

// chatAttachmentRelFromStorageKey turns a stored key back into the path the
// agent uses. storage_key is the authority on where an attachment lives — it
// is a column precisely so the layout can differ per row — so the response is
// derived from it rather than recomputed, which is what keeps attachments
// uploaded BEFORE the id segment existed listable and deletable at their
// original two-segment path.
//
// The prefix is stripped rather than assumed: a key that is not under this
// agent's namespace at all (its crew or slug changed since the upload) still
// yields the right relative path, because that is the part that does not
// depend on either.
func chatAttachmentRelFromStorageKey(key string, scope chatAttachmentScope) string {
	prefix := scope.crewID + "/" + scope.slug + "/"
	if rest, ok := strings.CutPrefix(key, prefix); ok {
		return rest
	}
	if i := strings.Index(key, "attachments/"); i >= 0 {
		return key[i:]
	}
	return key
}

// ── the two-phase write ────────────────────────────────────────────────────

// chatAttachmentRef is one reserved identity: the row that exists, and the
// place its bytes belong.
type chatAttachmentRef struct {
	id         string
	relPath    string
	storageKey string
}

// reserveChatAttachment writes the metadata row BEFORE anything is stored, in
// state `pending`.
//
// It mirrors the issue path's rules where they are about the DATA — the
// filename is already a sanitised basename by the time it gets here, the
// content type is resolved from the extension against the shared allowlist,
// and the digest is computed over the bytes that are about to be stored — and
// departs from it only on the blob location, which is this file's subject.
//
// An extension outside the allowlist does NOT refuse the upload. The endpoint
// has shipped for releases with no type restriction at all, and turning an
// established chat feature into a 415 as a side effect of a lifecycle change
// would be a breaking change smuggled into a bookkeeping one. The row records
// what the file actually is (application/octet-stream for an unrecognised
// extension) rather than pretending it was refused.
//
// A UNIQUE violation is the de-duplication index doing its job: the same FILE
// — same bytes, same name, same chat — is already an attachment. That is not
// an error and must not become a second identity, because "one upload, one
// blob" is what lets delete unlink without a refcount. The reservation
// RESOLVES to the row that already exists and the bytes are re-published at
// its recorded location, which is byte-identical to what is there and also
// repairs the case where a previous attempt recorded the row and never landed
// the file.
func (h *ProxyHandler) reserveChatAttachment(r *http.Request, workspaceID, chatID, filename, digest string, size int, scope chatAttachmentScope) (chatAttachmentRef, error) {
	contentType, err := resolveAttachmentType(filename)
	if err != nil {
		contentType = "application/octet-stream"
	}
	var userID string
	if u := UserFromContext(r.Context()); u != nil {
		userID = u.ID
	}

	id := generateCUID()
	relPath := chatAttachmentRelPath(chatID, id, filename)
	ref := chatAttachmentRef{
		id:         id,
		relPath:    relPath,
		storageKey: scope.crewID + "/" + scope.slug + "/" + relPath,
	}

	_, err = h.db.ExecContext(r.Context(), `
		INSERT INTO attachments
			(id, workspace_id, owner_type, chat_id, filename, content_type, size_bytes,
			 sha256, storage_key, uploaded_by_user_id, state, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ref.id, workspaceID, string(attachmentOwnerChat), chatID, filename, contentType,
		size, digest, ref.storageKey, nullIfEmpty(userID), attachmentStatePending,
		time.Now().UTC().Format(time.RFC3339))
	if err == nil {
		return ref, nil
	}
	if !isUniqueViolation(err) {
		return chatAttachmentRef{}, err
	}

	var existingID, existingKey string
	if err := h.db.QueryRowContext(r.Context(), `
		SELECT id, storage_key FROM attachments
		 WHERE chat_id = ? AND sha256 = ? AND filename = ?`,
		chatID, digest, filename).Scan(&existingID, &existingKey); err != nil {
		return chatAttachmentRef{}, err
	}
	// A key outside this agent's namespace cannot be written through this crew's
	// file endpoint (the agent's crew or slug changed since the upload), so the
	// attachment adopts a location in the namespace it now lives in. Its
	// identity is the id and storage_key is by design the mutable half — which
	// is why the adopted key is built from the EXISTING id and not from the one
	// generated above for the insert that lost. "The directory is the
	// attachment's id" has to keep holding; the delete relies on it.
	if !strings.HasPrefix(existingKey, scope.crewID+"/"+scope.slug+"/") {
		existingKey = scope.crewID + "/" + scope.slug + "/" +
			chatAttachmentRelPath(chatID, existingID, filename)
	}
	return chatAttachmentRef{
		id:         existingID,
		relPath:    chatAttachmentRelFromStorageKey(existingKey, scope),
		storageKey: existingKey,
	}, nil
}

// publishChatAttachment promotes a reserved row to `stored` once its bytes are
// in place. storage_key is written again in the same statement so the row and
// the blob cannot disagree even in the adoption case above; for every ordinary
// upload it is the value that is already there.
func (h *ProxyHandler) publishChatAttachment(r *http.Request, ref chatAttachmentRef) error {
	_, err := h.db.ExecContext(r.Context(),
		`UPDATE attachments SET state = ?, storage_key = ? WHERE id = ?`,
		attachmentStateStored, ref.storageKey, ref.id)
	return err
}

// abandonChatAttachment removes a reservation whose bytes never landed.
//
// Guarded on `state <> 'stored'` so a failed RE-upload against an attachment
// that already exists cannot delete the good row underneath it. If this delete
// itself fails there is nothing more to do on the request path — the row stays
// `pending`, which is the state the collector exists for, so the failure is
// bounded rather than silent.
//
// It runs on a context stripped of cancellation: the most common way to reach
// here with a dead request context is the client hanging up mid-upload, and
// that is precisely when the compensation is worth doing. Without this the
// cleanest failure — a cancelled upload — would be the one that always left a
// row for the collector.
func (h *ProxyHandler) abandonChatAttachment(r *http.Request, ref chatAttachmentRef) {
	if _, err := h.db.ExecContext(context.WithoutCancel(r.Context()),
		`DELETE FROM attachments WHERE id = ? AND state <> ?`, ref.id, attachmentStateStored); err != nil && h.logger != nil {
		h.logger.Warn("abandon chat attachment reservation",
			"attachment_id", ref.id, "error", err)
	}
}

// ── read projection ────────────────────────────────────────────────────────

// chatAttachmentResponse is one chat attachment on the wire: the shared
// attachment projection plus the two paths.
//
// The issue surface deliberately hides storage_key — publishing it there
// invites clients to construct paths instead of asking for resources. Here the
// path IS the resource: the agent is told to open it, so `path` and
// `agent_path` are the fields the caller came for. Neither is the raw key; the
// crew/agent prefix stays internal.
type chatAttachmentResponse struct {
	attachmentResponse
	Path      string `json:"path"`
	AgentPath string `json:"agent_path"`
}

// chatAttachmentSelect is attachmentSelect plus storage_key. It is spelled out
// rather than composed because the shared projection ends at its FROM clause
// and a column cannot be appended after it; the uploader CASE is identical on
// purpose, so a chat attachment and an issue attachment answer the "who
// attached this" question the same way.
const chatAttachmentSelect = `
	SELECT a.id, a.workspace_id, a.owner_type, a.chat_id,
	       a.filename, a.content_type, a.size_bytes, a.sha256,
	       a.uploaded_by_user_id, a.uploaded_by_agent_id,
	       CASE
	         WHEN a.uploaded_by_user_id  IS NOT NULL THEN (SELECT full_name FROM users  WHERE id = a.uploaded_by_user_id)
	         WHEN a.uploaded_by_agent_id IS NOT NULL THEN (SELECT name      FROM agents WHERE id = a.uploaded_by_agent_id)
	       END,
	       a.created_at, a.storage_key
	FROM attachments a`

func scanChatAttachment(s interface{ Scan(...interface{}) error }, scope chatAttachmentScope) (chatAttachmentResponse, error) {
	var out chatAttachmentResponse
	var storageKey string
	err := s.Scan(&out.ID, &out.WorkspaceID, &out.OwnerType, &out.OwnerID,
		&out.Filename, &out.ContentType, &out.SizeBytes, &out.SHA256,
		&out.UploadedByUserID, &out.UploadedByAgentID, &out.UploadedByName,
		&out.CreatedAt, &storageKey)
	if err != nil {
		return chatAttachmentResponse{}, err
	}
	out.Path = chatAttachmentRelFromStorageKey(storageKey, scope)
	out.AgentPath = chatAttachmentAgentPath(scope.slug, out.Path)
	return out, nil
}

// ── error text ─────────────────────────────────────────────────────────────

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
func attachmentSaveErrorMessage(body []byte) string {
	return attachmentIPCErrorMessage("save", body)
}

// attachmentDeleteErrorMessage is the same sentence for the unlink half. The
// remedies the IPC layer describes are the same ones — a stopped crew owns the
// tree, so it can refuse a removal for exactly the reason it refuses a write.
func attachmentDeleteErrorMessage(body []byte) string {
	return attachmentIPCErrorMessage("delete", body)
}

// attachmentIPCErrorMessage decodes the IPC envelope and prefixes it with the
// operation this layer knows it was performing.
//
// A body that isn't the expected shape is forwarded as text rather than
// dropped — an unexpected body is still the best diagnostic available.
func attachmentIPCErrorMessage(verb string, body []byte) string {
	msg := strings.TrimSpace(string(body))
	var env struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err == nil && strings.TrimSpace(env.Error) != "" {
		msg = strings.TrimSpace(env.Error)
	}
	if msg == "" {
		return "failed to " + verb + " attachment"
	}
	return "failed to " + verb + " attachment: " + msg
}
