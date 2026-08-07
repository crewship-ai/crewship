package api

// Attachments on an issue — the human/CLI surface (#1768, item 7).
//
// Routes (nested under the crew/issue path like comments, relations and code
// links, so they inherit the same `crews:write` route scope instead of needing a
// new entry in scopeForRoute):
//
//	GET    /api/v1/crews/{crewId}/issues/{identifier}/attachments
//	POST   /api/v1/crews/{crewId}/issues/{identifier}/attachments
//	GET    /api/v1/crews/{crewId}/issues/{identifier}/attachments/{attachmentId}
//	DELETE /api/v1/crews/{crewId}/issues/{identifier}/attachments/{attachmentId}
//
// The store — blob layout, filename rules, the type allowlist, refcounted
// deletion — is in attachments.go. What lives here is the HTTP shape and, more
// importantly, the tenancy: every query is scoped by workspace_id AND by the
// resolved mission, and a miss is a 404 rather than a 403. A 403 on another
// tenant's attachment id confirms the id exists, which is the whole disclosure.

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/ws"
)

// AttachmentHandler serves the issue attachment surface.
type AttachmentHandler struct {
	db     *sql.DB
	hub    *ws.Hub
	logger *slog.Logger
	// journal makes an attach/detach notifiable. Defaults to noopEmitter, and
	// SetJournal(nil) must restore that rather than store a nil interface —
	// same contract every other SetJournal in this package is held to.
	journal journal.Emitter
	// storagePath is the storage root the blobs live under. An empty value
	// means no storage provider was wired: uploads then fail with a 503 rather
	// than writing a metadata row for bytes that went nowhere.
	storagePath string
}

// NewAttachmentHandler wires the handler.
func NewAttachmentHandler(db *sql.DB, hub *ws.Hub, logger *slog.Logger) *AttachmentHandler {
	return &AttachmentHandler{db: db, hub: hub, logger: logger, journal: noopEmitter{}}
}

// SetJournal wires a journal emitter after construction.
func (h *AttachmentHandler) SetJournal(j journal.Emitter) {
	if j == nil {
		h.journal = noopEmitter{}
		return
	}
	h.journal = j
}

// SetStoragePath points the handler at the storage root.
func (h *AttachmentHandler) SetStoragePath(p string) { h.storagePath = p }

// events builds the shared issue-event emitter from the fields this handler
// already holds (issue_events.go).
func (h *AttachmentHandler) events() issueEvents {
	return issueEvents{db: h.db, hub: h.hub, logger: h.logger, journal: h.journal}
}

// attachmentResponse is the wire shape of one attachment.
//
// It carries the sha256 deliberately. A client that has downloaded the bytes can
// verify them, a backup verify pass can tell "blob missing" from "blob corrupt",
// and an agent deciding whether to re-read a file it has already seen can
// compare digests instead of re-fetching. The storage key is NOT exposed: it is
// an internal layout detail, and publishing it invites clients to construct
// paths rather than ask for resources.
type attachmentResponse struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	OwnerType   string `json:"owner_type"`
	OwnerID     string `json:"owner_id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	SHA256      string `json:"sha256"`

	UploadedByUserID  *string `json:"uploaded_by_user_id"`
	UploadedByAgentID *string `json:"uploaded_by_agent_id"`
	UploadedByName    *string `json:"uploaded_by_name"`

	CreatedAt string `json:"created_at"`
}

// attachmentSelect is the one projection every read uses. The uploader's display
// name is resolved in SQL from whichever arc is set, so no caller has to
// remember that an attachment may have been left by an agent.
const attachmentSelect = `
	SELECT a.id, a.workspace_id, a.owner_type,
	       COALESCE(a.mission_id, a.comment_id, a.chat_id),
	       a.filename, a.content_type, a.size_bytes, a.sha256,
	       a.uploaded_by_user_id, a.uploaded_by_agent_id,
	       CASE
	         WHEN a.uploaded_by_user_id  IS NOT NULL THEN (SELECT full_name FROM users  WHERE id = a.uploaded_by_user_id)
	         WHEN a.uploaded_by_agent_id IS NOT NULL THEN (SELECT name      FROM agents WHERE id = a.uploaded_by_agent_id)
	       END,
	       a.created_at
	FROM attachments a`

func scanAttachment(s interface{ Scan(...interface{}) error }) (attachmentResponse, error) {
	var a attachmentResponse
	err := s.Scan(&a.ID, &a.WorkspaceID, &a.OwnerType, &a.OwnerID,
		&a.Filename, &a.ContentType, &a.SizeBytes, &a.SHA256,
		&a.UploadedByUserID, &a.UploadedByAgentID, &a.UploadedByName, &a.CreatedAt)
	return a, err
}

// listIssueAttachments is the shared read the public List and the agent-facing
// internal list both go through. Scoping by BOTH mission and workspace is
// redundant only if the mission was resolved inside the workspace — it always
// is, and the redundancy is what makes a future caller that resolves it some
// other way still safe.
func listIssueAttachments(r *http.Request, db *sql.DB, missionID, wsID string) ([]attachmentResponse, error) {
	rows, err := db.QueryContext(r.Context(),
		attachmentSelect+` WHERE a.mission_id = ? AND a.workspace_id = ? ORDER BY a.created_at DESC, a.id DESC`,
		missionID, wsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []attachmentResponse{}
	for rows.Next() {
		a, err := scanAttachment(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, rows.Err()
}

// ── List — GET …/attachments ───────────────────────────────────────────────

// List returns every attachment on an issue, newest first.
func (h *AttachmentHandler) List(w http.ResponseWriter, r *http.Request) {
	missionID, wsID, ok := h.resolveIssue(w, r)
	if !ok {
		return
	}
	result, err := listIssueAttachments(r, h.db, missionID, wsID)
	if err != nil {
		internalError(w, r, h.logger, "list attachments", err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// ── Upload — POST …/attachments ────────────────────────────────────────────

// Upload accepts a multipart form with one "file" field and attaches it.
//
// Uploading the same FILE — the same bytes under the same name — to the same
// issue twice returns the EXISTING row with 200 rather than creating a second
// one or answering 409. It is not an error: the caller asked for "this file is
// attached to this issue" and after the call it is. A 409 would make every
// retried or double-clicked upload look like a failure, and a second row would
// double a refcount that decides whether the blob may ever be unlinked.
//
// The same bytes under a DIFFERENT name are a different attachment and get a
// 201. crash-before-fix.log and crash-after-fix.log can be byte-identical and
// mean opposite things; collapsing them silently answered 200 with the wrong
// filename and left no trace that the second file was ever offered.
func (h *AttachmentHandler) Upload(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}
	missionID, wsID, ok := h.resolveIssue(w, r)
	if !ok {
		return
	}
	if h.storagePath == "" {
		writeProblem(w, r, http.StatusServiceUnavailable, "Attachment storage is not configured on this instance")
		return
	}

	// MaxBytesReader before ParseMultipartForm: the reader is what makes an
	// oversized body fail at the cap instead of after it has been buffered.
	r.Body = http.MaxBytesReader(w, r.Body, maxAttachmentBytes)
	if err := r.ParseMultipartForm(maxAttachmentBytes); err != nil {
		// "too large" and "malformed" are different problems with different
		// fixes, and collapsing them sends someone who sent a broken form off to
		// shrink a file that was never the issue. MaxBytesError is the only
		// signal that separates them.
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeProblem(w, r, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("File larger than %d MiB", maxAttachmentBytes>>20))
			return
		}
		writeProblem(w, r, http.StatusBadRequest, "Invalid multipart form")
		return
	}
	// ParseMultipartForm spills uploads near the limit to disk; without this
	// those temp files accumulate under repeated uploads.
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	file, header, err := r.FormFile("file")
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, "file field is required")
		return
	}
	defer file.Close()

	// LimitReader at cap+1 so a body that lies about its length is caught by the
	// length of what we actually read, not by a header we were told.
	data, err := io.ReadAll(io.LimitReader(file, maxAttachmentBytes+1))
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Could not read the uploaded file")
		return
	}
	if len(data) > maxAttachmentBytes {
		writeProblem(w, r, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("File larger than %d MiB", maxAttachmentBytes>>20))
		return
	}

	var userID string
	if u := UserFromContext(r.Context()); u != nil {
		userID = u.ID
	}
	att, created, err := h.attachBytes(r, wsID, missionID, header.Filename, data, userID, "")
	if err != nil {
		h.replyAttachError(w, r, err)
		return
	}
	if created {
		h.recordAttachmentEvent(r, missionID, wsID, "user", userID, actionAttachmentAdded, att)
		writeJSON(w, http.StatusCreated, att)
		return
	}
	// Idempotent re-upload: no new row, so no new audit row either. Auditing it
	// would put "attached server.log" on the timeline every time someone
	// double-clicked, which makes the timeline less trustworthy, not more.
	writeJSON(w, http.StatusOK, att)
}

// attachBytes is the shared write both the human and the agent doors go
// through: sanitise, type, store the blob, insert the row.
//
// It returns created=false when the row already existed, which is what makes a
// retried upload idempotent. Exactly one of userID / agentID must be non-empty;
// the schema does not enforce that (both columns are independently nullable) but
// the two call sites are the only writers and each knows which one it is.
func (h *AttachmentHandler) attachBytes(
	r *http.Request, wsID, missionID, rawName string, data []byte, userID, agentID string,
) (attachmentResponse, bool, error) {
	filename, err := sanitizeAttachmentFilename(rawName)
	if err != nil {
		return attachmentResponse{}, false, err
	}
	contentType, err := resolveAttachmentType(filename)
	if err != nil {
		return attachmentResponse{}, false, err
	}

	// The blob write and the row insert are ONE critical section, keyed by
	// (workspace, digest) — see attachments.go. Between them the blob is on disk
	// with nothing naming it, which is exactly what the reclaim sweep calls
	// garbage; holding the lock is what stops a concurrent sweep from deleting
	// the bytes of a row that is one statement from committing.
	sha := attachmentDigest(data)
	defer lockAttachmentBlob(wsID, sha)()

	// The blob is written BEFORE the row. The other order would put a row in the
	// table describing bytes that are not on disk yet, and a crash in between
	// would leave a listed attachment that 404s on download. This order's
	// failure mode is a blob with no row — invisible, and reclaimable by
	// reclaimAttachmentBlobs.
	storedSHA, key, err := storeAttachmentBlob(h.storagePath, wsID, data)
	if err != nil {
		return attachmentResponse{}, false, err
	}
	if storedSHA != sha {
		// Unreachable unless the two digests are computed differently, which
		// would mean the lock guards a different key than the file. Refuse rather
		// than write a row the sweep cannot reason about.
		return attachmentResponse{}, false, fmt.Errorf("attachment digest mismatch")
	}

	id := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = h.db.ExecContext(r.Context(), `
		INSERT INTO attachments
			(id, workspace_id, owner_type, mission_id, filename, content_type, size_bytes,
			 sha256, storage_key, uploaded_by_user_id, uploaded_by_agent_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, wsID, string(attachmentOwnerIssue), missionID, filename, contentType, len(data),
		sha, key, nullIfEmpty(userID), nullIfEmpty(agentID), now)
	if err != nil {
		if strings.Contains(strings.ToUpper(err.Error()), "UNIQUE") {
			// The partial UNIQUE index fired: this exact FILE — same bytes, same
			// name — is already on this issue. Return the row that is there.
			//
			// Matching on the name as well as the digest is what makes the answer
			// true. The index used to be (mission_id, sha256), so attaching
			// crash-before-fix.log and crash-after-fix.log with identical bytes
			// returned 200 carrying the FIRST file's name and wrote no activity
			// row: the user believed both were attached and the second name was
			// never recorded anywhere.
			existing, loadErr := h.loadByDigest(r, missionID, wsID, sha, filename)
			if loadErr != nil {
				return attachmentResponse{}, false, loadErr
			}
			return existing, false, nil
		}
		return attachmentResponse{}, false, err
	}

	att, err := h.loadOne(r, id, wsID)
	return att, true, err
}

// ── Download — GET …/attachments/{attachmentId} ────────────────────────────

// Download streams the bytes.
//
// Three headers are not optional here and each closes a different hole:
//
//	Content-Type is the value WE resolved from the extension, never the
//	uploader's — see attachmentTypes;
//	X-Content-Type-Options: nosniff stops a browser from overriding that and
//	sniffing HTML out of a file we labelled text/plain;
//	Content-Disposition: attachment means even a type we got wrong is
//	downloaded rather than rendered in our origin.
func (h *AttachmentHandler) Download(w http.ResponseWriter, r *http.Request) {
	missionID, wsID, ok := h.resolveIssue(w, r)
	if !ok {
		return
	}
	att, err := h.loadScoped(r, r.PathValue("attachmentId"), missionID, wsID)
	if err != nil {
		h.replyNotFoundOr500(w, r, "load attachment", err)
		return
	}
	if h.storagePath == "" {
		writeProblem(w, r, http.StatusServiceUnavailable, "Attachment storage is not configured on this instance")
		return
	}

	data, err := readAttachmentBlob(h.storagePath, wsID, att.SHA256)
	if err != nil {
		// A row whose blob is gone is a 404 on the FILE, not a 500: the most
		// likely cause is a restore that carried the metadata without the blob,
		// and telling the caller "not found" is both true and actionable.
		h.replyNotFoundOr500(w, r, "read attachment blob", err)
		return
	}

	w.Header().Set("Content-Type", att.ContentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition",
		mime.FormatMediaType("attachment", map[string]string{"filename": att.Filename}))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// ── Delete — DELETE …/attachments/{attachmentId} ───────────────────────────

// Delete removes an attachment and, if nothing else in the workspace names its
// bytes, the blob.
//
// Hard delete of the row, like mission_relations and mission_code_links: the
// fact worth keeping is that someone attached and then removed a file, and that
// is what the mission_activity pair records. A soft-deleted row would also have
// to be excluded from the refcount, and getting that wrong deletes a blob
// another owner is still using.
func (h *AttachmentHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}
	missionID, wsID, ok := h.resolveIssue(w, r)
	if !ok {
		return
	}
	// Read the row before deleting it: the sha256 is what the refcount needs and
	// the filename is what the audit row says.
	att, err := h.loadScoped(r, r.PathValue("attachmentId"), missionID, wsID)
	if err != nil {
		h.replyNotFoundOr500(w, r, "load attachment for delete", err)
		return
	}

	res, err := h.db.ExecContext(r.Context(),
		`DELETE FROM attachments WHERE id = ? AND mission_id = ? AND workspace_id = ?`,
		att.ID, missionID, wsID)
	if err != nil {
		internalError(w, r, h.logger, "delete attachment", err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeProblem(w, r, http.StatusNotFound, "Attachment not found")
		return
	}

	unlinkAttachmentBlobIfUnreferenced(r.Context(), h.db, h.logger, h.storagePath, wsID, att.SHA256)

	var userID string
	if u := UserFromContext(r.Context()); u != nil {
		userID = u.ID
	}
	actorType := "user"
	if userID == "" {
		actorType = "system"
	}
	h.recordAttachmentEvent(r, missionID, wsID, actorType, userID, actionAttachmentRemoved, att)

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ── plumbing ───────────────────────────────────────────────────────────────

// resolveIssue turns {crewId}/{identifier} into a mission id inside the
// CALLER's workspace, replying 404 when it does not resolve.
//
// The workspace comes from the request context (set by wsCtx from the
// authenticated session), never from the path or the body. That is the whole
// tenancy story for this surface: an identifier that exists in another tenant
// resolves to nothing here and the caller cannot tell it apart from a typo.
func (h *AttachmentHandler) resolveIssue(w http.ResponseWriter, r *http.Request) (missionID, wsID string, ok bool) {
	wsID = WorkspaceIDFromContext(r.Context())
	if wsID == "" {
		writeProblem(w, r, http.StatusBadRequest, "workspace is required")
		return "", "", false
	}
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id FROM missions WHERE identifier = ? AND crew_id = ? AND workspace_id = ?`,
		r.PathValue("identifier"), r.PathValue("crewId"), wsID).Scan(&missionID)
	if err != nil {
		writeProblem(w, r, http.StatusNotFound, "Issue not found")
		return "", "", false
	}
	return missionID, wsID, true
}

func (h *AttachmentHandler) loadOne(r *http.Request, id, wsID string) (attachmentResponse, error) {
	return scanAttachment(h.db.QueryRowContext(r.Context(),
		attachmentSelect+` WHERE a.id = ? AND a.workspace_id = ?`, id, wsID))
}

// loadScoped reads one attachment by id, scoped to BOTH the issue and the
// workspace.
//
// The mission_id predicate is not decoration. Without it an attachment id from
// another issue in the same workspace would download through any issue's URL,
// and — worse — DELETE through it, because the delete's own WHERE clause is
// built from this row.
func (h *AttachmentHandler) loadScoped(r *http.Request, id, missionID, wsID string) (attachmentResponse, error) {
	return scanAttachment(h.db.QueryRowContext(r.Context(),
		attachmentSelect+` WHERE a.id = ? AND a.mission_id = ? AND a.workspace_id = ?`,
		id, missionID, wsID))
}

// loadByDigest reads the row a duplicate INSERT collided with. Its predicate is
// the UNIQUE index's own key — (mission, digest, filename) — because a lookup
// narrower than the constraint returns some OTHER row and the caller reports it
// as the file the user just uploaded.
func (h *AttachmentHandler) loadByDigest(r *http.Request, missionID, wsID, sha, filename string) (attachmentResponse, error) {
	return scanAttachment(h.db.QueryRowContext(r.Context(),
		attachmentSelect+` WHERE a.mission_id = ? AND a.workspace_id = ? AND a.sha256 = ? AND a.filename = ?`,
		missionID, wsID, sha, filename))
}

// replyNotFoundOr500 maps a row/blob lookup failure onto a response. Anything
// that is not "no such thing" is ours, and is logged as such.
func (h *AttachmentHandler) replyNotFoundOr500(w http.ResponseWriter, r *http.Request, msg string, err error) {
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, os.ErrNotExist) {
		writeProblem(w, r, http.StatusNotFound, "Attachment not found")
		return
	}
	internalError(w, r, h.logger, msg, err)
}

// replyAttachError maps a store failure onto a response. The two precondition
// failures name what to change; anything else is a 500.
func (h *AttachmentHandler) replyAttachError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errAttachmentFilename):
		writeProblem(w, r, http.StatusBadRequest, "Invalid filename")
	case errors.Is(err, errAttachmentType):
		writeProblem(w, r, http.StatusUnsupportedMediaType,
			err.Error()+" — allowed: "+strings.Join(allowedAttachmentExtensions(), ", "))
	default:
		internalError(w, r, h.logger, "attach file", err)
	}
}

// recordAttachmentEvent records an attach/detach on the issue timeline, in the
// journal and on the hub, through the one shared emitter (issue_events.go).
//
// `details` is the filename and the size — never the content, for the reason
// describeDescriptionChange spells out: an audit row is read back into the
// timeline, exported by backup and truncated into a notification body, and file
// content belongs in none of those.
func (h *AttachmentHandler) recordAttachmentEvent(
	r *http.Request, missionID, wsID, actorType, actorID string, action issueAction, att attachmentResponse,
) {
	h.events().record(r.Context(), wsID, map[string]string{"id": missionID}, issueEvent{
		MissionID: missionID,
		ActorType: actorType,
		ActorID:   actorID,
		Action:    action,
		Details:   describeAttachment(att),
	})
}

// describeAttachment builds the `details` payload for an attachment event.
func describeAttachment(att attachmentResponse) string {
	return fmt.Sprintf("%s (%s, %d bytes)", att.Filename, att.ContentType, att.SizeBytes)
}

// allowedAttachmentExtensions renders the allowlist for an error message, so a
// refused upload says what WOULD work instead of only what did not.
func allowedAttachmentExtensions() []string {
	out := make([]string, 0, len(attachmentTypes))
	for ext := range attachmentTypes {
		out = append(out, ext)
	}
	sort.Strings(out)
	return out
}
