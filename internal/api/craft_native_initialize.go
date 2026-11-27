package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/crewship-ai/crewship/internal/tsformat"
	"io"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/appengine"
	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/craftapps"
	"github.com/crewship-ai/crewship/internal/mooring"
)

type craftNativeInitializeResponse struct {
	Token         string `json:"session_token"`
	SessionID     string `json:"session_id"`
	ApplicationID string `json:"application_id"`
	PageID        string `json:"page_id"`
	Generation    string `json:"generation"`
	Version       string `json:"version"`
	// A workbook Sheet's first version: the uploaded file's hash and format.
	ContentHash string `json:"contentHash,omitempty"`
	Format      string `json:"format,omitempty"`
	// ChatRef: the Sheet was added to the chat named by X-Craft-Chat with its first version.
	ChatRef bool `json:"chatRef,omitempty"`
}

// initializeOwnerProof never converts an existing format. Each call uses the
// caller's transaction and original credential, including the final publication
// callback after SQL has run. Workspace owner status does not replace Page ownership.
func (m *craftNativeHTTP) initializeOwnerProof(ctx context.Context, tx *sql.Tx, source auth.OriginalCredential, scope mooring.EditLeaseScope, empty bool) (craftNativeProof, error) {
	var p craftNativeProof
	expiry, err := validateOriginalHumanCredential(ctx, tx, source, "pages:write")
	if err != nil {
		return p, err
	}
	ctx = context.WithValue(context.WithValue(ctx, pageAuthReaderKey{}, tx), ctxRole, "")
	rec, err := m.pages.loadPageForRead(ctx, scope.WorkspaceID, "@"+scope.ArtifactID)
	if err != nil {
		return p, err
	}
	if rec.OwnerUserID != source.UserID || rec.OwnerCrewID != "" {
		return p, errCraftNativeAccess
	}
	if rec.FolderID != "" {
		var local bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM page_folders WHERE id=? AND workspace_id=?)`, rec.FolderID, scope.WorkspaceID).Scan(&local); err != nil {
			return p, err
		}
		if !local {
			return p, errCraftNativeAccess
		}
	}
	role, err := m.pages.liveContentHumanRole(ctx, scope.WorkspaceID, source.UserID)
	if err != nil {
		return p, err
	}
	ctx = context.WithValue(ctx, ctxRole, role)
	viewer, err := m.pages.loadViewer(ctx, scope.WorkspaceID, source.UserID)
	if err != nil {
		return p, err
	}
	visible, err := m.pages.canSeePage(ctx, scope.WorkspaceID, rec, nil, viewer)
	if err != nil {
		return p, err
	}
	if !visible || !m.pages.mayEditSpec(ctx, scope.WorkspaceID, source.UserID, role, rec) {
		return p, errCraftNativeAccess
	}
	if err = tx.QueryRowContext(ctx, `SELECT CAST(w.authority_revision AS TEXT),wm.id,wm.access_revision FROM workspaces w JOIN workspace_members wm ON wm.workspace_id=w.id WHERE w.id=? AND wm.user_id=? AND w.deleted_at IS NULL`, scope.WorkspaceID, source.UserID).Scan(&p.workspaceEpoch, &p.memberID, &p.memberRevision); err != nil {
		return p, err
	}
	if p.workspaceEpoch == "" || p.memberID == "" || p.memberRevision < 1 || !expiry.IsZero() && !time.Now().Before(expiry) {
		return p, errCraftNativeAccess
	}
	if empty {
		var exists bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM craft_native_documents WHERE page_id=? UNION ALL SELECT 1 FROM mooring_collab_documents WHERE page_id=?)`, scope.ArtifactID, scope.ArtifactID).Scan(&exists); err != nil {
			return p, err
		}
		vacant, e := appengine.PageHasNoContent(ctx, tx, scope.ArtifactID)
		if e != nil {
			return p, e
		}
		if exists || !vacant {
			return p, artifacts.ErrVersionConflict
		}
	}
	p.record = rec
	p.expiry = expiry
	return p, nil
}

func (d *craftNativeDocumentsHTTP) initialize(w http.ResponseWriter, r *http.Request) {
	d.initializeFrom(w, r, nil)
}

// importInitialize publishes a converted workbook the server holds itself
// (POST .../craft/import/{id}/initialize): same owner, same job, and from there
// exactly the checks, storage admission, validation and lease of initialize,
// without the browser downloading 30 MB and uploading 40 MB again.
func (d *craftNativeDocumentsHTTP) importInitialize(w http.ResponseWriter, r *http.Request) {
	if d.workbookSheets() {
		replyCode(w, 404, "use_initialize", "this server keeps Sheets as workbook files: start the Sheet from the file with craft/initialize")
		return
	}
	job := d.importJob(r)
	if job == nil {
		replyError(w, 404, "import not found")
		return
	}
	im := d.importer()
	im.mu.Lock()
	native, state := job.native, job.state
	im.mu.Unlock()
	if state != "ready" || len(native) == 0 {
		replyError(w, 409, "the import is not ready")
		return
	}
	d.initializeFrom(w, r, &craftNativeImportSource{job: job, native: native})
}

// craftNativeImportSource is the server-held document initialize publishes.
type craftNativeImportSource struct {
	job    *craftNativeImportJob
	native []byte
}

func (d *craftNativeDocumentsHTTP) initializeFrom(w http.ResponseWriter, r *http.Request, held *craftNativeImportSource) {
	m := d.human
	if m == nil || !m.enabled || m.clock == nil || m.pages == nil {
		craftNativeDocumentError(w, mooring.ErrNativeDisabled)
		return
	}
	source, err := craftNativeHuman(r.Context())
	if err != nil {
		craftNativeInitializeRefused(w, "identity", err)
		return
	}
	// The application is named in the body; when the administrator turned both off, refuse
	// before reading it. The decoded application is checked again below.
	if held == nil && !m.apps.allows(r.Context(), "gridcraft", craftapps.OpCreate) && !m.apps.allows(r.Context(), "wordcraft", craftapps.OpCreate) {
		m.apps.refuse(w, r.Context(), "gridcraft")
		return
	}
	// Resolve the target before decoding the large native body. The publication
	// transaction resolves/checks it again and cannot consume another Page's allowance.
	recForUpload, e := m.pages.loadPageForRead(r.Context(), WorkspaceIDFromContext(r.Context()), r.PathValue("slug"))
	if e != nil {
		craftNativeInitializeRefused(w, "page", e)
		return
	}
	// X-Craft-Chat: the Sheet is opened from a file in this chat and belongs to it from version 1.
	// Only the caller's own chat; any other, or none, is the same 404 and nothing is created.
	chat, chatNamed := r.Header.Get("X-Craft-Chat"), len(r.Header.Values("X-Craft-Chat")) > 0
	if chatNamed && !craftCallerOwnsChat(r.Context(), m.pages.db, WorkspaceIDFromContext(r.Context()), chat, source.UserID) {
		replyCode(w, 404, "chat_not_found", "Chat not found")
		return
	}
	// A Sheet opened from a file the person attached in their private chat with an agent is private
	// to them: only an empty page just made, theirs, without a folder or
	// a crew; an existing page keeps its audience. Before any authority is taken: the change moves the
	// workspace's authority revision, which every later proof then includes. Failing later leaves an
	// empty page of the same owner private, never wider.
	if chatNamed {
		if _, err := m.pages.db.ExecContext(r.Context(), `UPDATE pages SET private_audience=1 WHERE id=? AND workspace_id=? AND owner_user_id=? AND owner_crew_id IS NULL AND folder_id IS NULL AND private_audience=0
 AND julianday(created_at) >= julianday('now','-15 minutes')
 AND NOT EXISTS(SELECT 1 FROM artifact_versions v WHERE v.page_id=pages.id AND v.workspace_id=pages.workspace_id)
 AND EXISTS(SELECT 1 FROM chats c WHERE c.id=? AND c.workspace_id=? AND c.created_by=? AND c.visibility='private')`,
			recForUpload.ID, WorkspaceIDFromContext(r.Context()), source.UserID, chat, WorkspaceIDFromContext(r.Context()), source.UserID); err != nil {
			craftNativeInitializeRefused(w, "page", err)
			return
		}
	}
	uploadKey := artifacts.VersionKey{WorkspaceID: WorkspaceIDFromContext(r.Context()), PageID: recForUpload.ID}
	uploadScope := mooring.EditLeaseScope{WorkspaceID: uploadKey.WorkspaceID, ArtifactID: uploadKey.PageID}
	var uploadRefused bool // the storage admission failed on the owner check, not on storage
	authorizeUpload := func(ctx context.Context, tx *sql.Tx) (string, error) {
		_, e := m.initializeOwnerProof(ctx, tx, source, uploadScope, true)
		uploadRefused = e != nil
		return source.UserID, e
	}
	var bound *http.Request
	var cleanup func()
	if held != nil {
		bound, cleanup, e = artifactStorageUploadSized(w, r, artifacts.NewVersionStore(m.pages.db, artifacts.VersionOptions{Enabled: true}), uploadKey, int64(len(held.native)), authorizeUpload)
	} else {
		bound, cleanup, e = artifactStorageUpload(w, r, artifacts.NewVersionStore(m.pages.db, artifacts.VersionOptions{Enabled: true}), uploadKey, 45<<20, authorizeUpload)
	}
	if e != nil {
		if uploadRefused {
			craftNativeInitializeRefused(w, "storage_owner", e)
		} else {
			craftNativeInitializeFault(w, "storage", e)
		}
		return
	}
	defer cleanup()
	r = bound
	var body struct {
		ApplicationID string `json:"application_id"`
		Bytes         []byte `json:"bytes"`
	}
	if held != nil {
		body.ApplicationID, body.Bytes = "gridcraft", held.native
	} else {
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 45<<20))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&body); err != nil {
			replyError(w, 400, "invalid native initialization request")
			return
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF || body.ApplicationID != "wordcraft" && body.ApplicationID != "gridcraft" {
			replyError(w, 400, "invalid native initialization request")
			return
		}
	}
	if len(body.Bytes) > 32<<20 {
		replyError(w, 413, "document file exceeds 32 MiB")
		return
	}
	profile, qualified := d.application(body.ApplicationID)
	if !qualified {
		craftNativeDocumentError(w, mooring.ErrNativeDisabled)
		return
	}
	if !m.apps.allows(r.Context(), body.ApplicationID, craftapps.OpCreate) {
		m.apps.refuse(w, r.Context(), body.ApplicationID)
		return
	}
	tx, err := m.pages.db.BeginTx(r.Context(), nil)
	if err != nil {
		craftNativeInitializeFault(w, "begin", err)
		return
	}
	defer tx.Rollback()
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	rec, err := m.pages.loadPageForRead(ctx, WorkspaceIDFromContext(ctx), r.PathValue("slug"))
	if err != nil {
		craftNativeInitializeRefused(w, "page_tx", err)
		return
	}
	scope := mooring.EditLeaseScope{WorkspaceID: WorkspaceIDFromContext(ctx), ArtifactID: rec.ID}
	proof, err := m.initializeOwnerProof(ctx, tx, source, scope, true)
	if err != nil {
		craftNativeInitializeRefused(w, "owner", err)
		return
	}
	token, err := craftNativeOpaque()
	if err != nil {
		craftNativeInitializeFault(w, "token", err)
		return
	}
	id, err := craftNativeOpaque()
	if err != nil {
		craftNativeInitializeFault(w, "token", err)
		return
	}
	boot, now, err := m.clock.Now()
	if err != nil || boot == "" || now < 0 {
		if err == nil {
			err = errCraftNativeClock
		}
		craftNativeInitializeLog("clock", err)
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the server cannot start a native session now", "code": "clock"})
		return
	}
	cap := 24 * time.Hour
	if !proof.expiry.IsZero() && time.Until(proof.expiry) < cap {
		cap = time.Until(proof.expiry)
	}
	if cap <= 0 || now > time.Duration(math.MaxInt64)-cap {
		craftNativeInitializeRefused(w, "expiry", errCraftNativeAccess)
		return
	}
	generations, err := captureCraftNativeGenerations(ctx, tx, scope, source.UserID)
	if err != nil {
		craftNativeInitializeRefused(w, "generations", err)
		return
	}
	entry := &craftNativeSession{id: id, tokenHash: sha256.Sum256([]byte(token)), source: source, scope: scope, application: body.ApplicationID, memberID: proof.memberID, memberRevision: proof.memberRevision, generations: generations, boot: boot, deadline: now + cap}
	if err = tx.Commit(); err != nil {
		craftNativeInitializeFault(w, "commit", err)
		return
	}
	m.mu.Lock()
	if len(m.sessionsByID) >= 4096 {
		m.mu.Unlock()
		replyError(w, 503, "native Craft session limit reached")
		return
	}
	// By-ID is private to trusted transaction callbacks. The bearer is deliberately
	// absent from sessionsByHash until the entire native publication has committed.
	m.sessionsByID[id] = entry
	m.mu.Unlock()
	published := false
	defer func() {
		if !published {
			m.mu.Lock()
			delete(m.sessionsByID, id)
			m.mu.Unlock()
		}
	}()
	cfg := profile.Store
	cfg.Enabled = true
	cfg.Leases = m.leases
	var storeRefused bool // the store failed on the session's authority, not on its own work
	authorize := func(ctx context.Context, tx *sql.Tx, k mooring.EditLeaseScope, intent string) (mooring.EditLeasePrincipal, error) {
		m.mu.Lock()
		current := m.sessionsByID[id]
		m.mu.Unlock()
		if current != entry || k != entry.scope {
			return mooring.EditLeasePrincipal{}, errCraftNativeAccess
		}
		if intent != "initialize" && intent != "write" {
			return mooring.EditLeasePrincipal{}, errCraftNativeAccess
		}
		p, e := m.initializeOwnerProof(ctx, tx, entry.source, k, intent == "initialize")
		if e != nil {
			return mooring.EditLeasePrincipal{}, e
		}
		if p.memberID != entry.memberID || p.memberRevision != entry.memberRevision {
			return mooring.EditLeasePrincipal{}, errCraftNativeAccess
		}
		if e = craftNativeGenerationsCurrent(ctx, tx, entry); e != nil {
			return mooring.EditLeasePrincipal{}, e
		}
		b, n, e := m.clock.Now()
		if e != nil || b != entry.boot || n < 0 || n >= entry.deadline {
			return mooring.EditLeasePrincipal{}, errCraftNativeAccess
		}
		if intent == "write" {
			return m.authorizeNative(ctx, tx, k, intent)
		}
		return craftNativePrincipal(entry), nil
	}
	cfg.Authorize = func(ctx context.Context, tx *sql.Tx, k mooring.EditLeaseScope, intent string) (mooring.EditLeasePrincipal, error) {
		p, e := authorize(ctx, tx, k, intent)
		if e != nil {
			storeRefused = true
		}
		return p, e
	}
	nativeCtx := context.WithValue(r.Context(), craftNativeContextKey{}, entry)
	savedBy := craftSheetSavedByInitialize
	if held != nil {
		savedBy = craftSheetSavedByImport
	}
	record := craftSheetRecord(scope, entry.application, entry.source.UserID, savedBy)
	if chatNamed {
		record = craftChatRefRecord(record, chat, scope, entry.source.UserID)
	}
	lease, receipt, err := mooring.NewNativeStore(m.pages.db, cfg).InitializeRecorded(nativeCtx, scope, craftNativePrincipal(entry), body.Bytes, record)
	if err != nil {
		if errors.Is(err, errCraftPrivateRequired) {
			replyCode(w, 409, "private_required", "This Sheet could not be made private, so nothing was saved. Try again.")
		} else if storeRefused || errors.Is(err, sql.ErrNoRows) {
			craftNativeInitializeRefused(w, "store_authority", errCraftNativeAccess)
		} else {
			craftNativeInitializeFault(w, "store", err)
		}
		return
	}
	m.mu.Lock()
	m.sessionsByHash[entry.tokenHash] = entry
	published = true
	m.mu.Unlock()
	if held != nil {
		d.importer().discard(held.job.id) // consumed: the document is published
	}
	w.Header().Set("Cache-Control", "no-store")
	response := craftNativeInitializeResponse{Token: token, SessionID: id, ApplicationID: entry.application, PageID: scope.ArtifactID, Generation: strconv.FormatInt(lease.Generation, 10), Version: strconv.FormatInt(receipt.Version, 10)}
	if profile.Store.Format == mooring.NativeFormatWorkbook {
		response.ContentHash = craftContentHash(body.Bytes)
		response.Format, _ = mooring.DetectWorkbook(body.Bytes)
	}
	response.ChatRef = chatNamed
	writeJSON(w, http.StatusCreated, response)
}

var errCraftNativeClock = errors.New("native Craft owner clock unavailable")

// craftNativeInitializeLog names the step of initialize that failed and the error's class (the
// sentinels it matches and the types it wraps), never its text: that may carry document content.
func craftNativeInitializeLog(step string, err error) {
	var types []string
	for e := err; e != nil && len(types) < 8; e = errors.Unwrap(e) {
		types = append(types, fmt.Sprintf("%T", e))
	}
	var known []string
	for name, s := range map[string]error{"access": errCraftNativeAccess, "no_rows": sql.ErrNoRows, "disabled": mooring.ErrNativeDisabled,
		"validation": mooring.ErrNativeValidation, "invalid": mooring.ErrInvalid, "lease_busy": mooring.ErrEditLeaseBusy, "lease_stale": mooring.ErrEditLeaseStale,
		"storage_quota": artifacts.ErrStorageQuota, "storage_busy": artifacts.ErrStorageBusy, "version_quota": artifacts.ErrVersionQuota, "version_conflict": artifacts.ErrVersionConflict,
		"clock": errCraftNativeClock, "canceled": context.Canceled, "deadline": context.DeadlineExceeded} {
		if errors.Is(err, s) {
			known = append(known, name)
		}
	}
	sort.Strings(known)
	slog.Default().Warn("native Craft initialize failed", "step", step, "known", strings.Join(known, ","), "types", strings.Join(types, ">"))
}

// craftNativeInitializeRefused answers a refusal of the caller's authority as before (403 and the
// other mapped statuses), after logging it.
func craftNativeInitializeRefused(w http.ResponseWriter, step string, err error) {
	craftNativeInitializeLog(step, err)
	craftNativeDocumentError(w, err)
}

// craftNativeInitializeFault answers a failure of the server's own work: a known condition keeps
// its status (storage full or busy, invalid document, lease changed, Sheet off), anything else is
// a 500 with a code, never "access denied".
func craftNativeInitializeFault(w http.ResponseWriter, step string, err error) {
	craftNativeInitializeLog(step, err)
	switch {
	case errors.Is(err, artifacts.ErrStorageQuota), errors.Is(err, artifacts.ErrStorageBusy), errors.Is(err, mooring.ErrNativeDisabled),
		errors.Is(err, artifacts.ErrVersionQuota), errors.Is(err, mooring.ErrNativeAttemptAbsent),
		errors.Is(err, mooring.ErrEditLeaseBusy), errors.Is(err, mooring.ErrEditLeaseStale), errors.Is(err, artifacts.ErrVersionConflict),
		errors.Is(err, mooring.ErrNativeUnconfirmed), errors.Is(err, mooring.ErrNativeAttemptRetired), errors.Is(err, mooring.ErrNativeRetirementLimit),
		errors.Is(err, mooring.ErrNativeValidation), errors.Is(err, mooring.ErrInvalid), errors.Is(err, artifacts.ErrVersionInvalid):
		craftNativeDocumentError(w, err)
	default:
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the server could not initialize the document", "code": "server_" + step})
	}
}

var craftChatID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// craftCallerOwnsChat: chat is a chat of the workspace that userID started, the rule a message
// reference follows (artifact_chat_refs_own_chat checks it again in the saving transaction).
func craftCallerOwnsChat(ctx context.Context, db *sql.DB, workspaceID, chat, userID string) bool {
	if !craftChatID.MatchString(chat) || userID == "" {
		return false
	}
	var one int
	return db.QueryRowContext(ctx, `SELECT 1 FROM chats WHERE id=? AND workspace_id=? AND created_by=?`, chat, workspaceID, userID).Scan(&one) == nil
}

// craftChatRefRecord adds the new Sheet to the chat in the transaction that stores its first
// version: no version, no reference.
// errCraftPrivateRequired: a Sheet from the person's private chat could not be made private to
// them, so nothing was saved.
var errCraftPrivateRequired = errors.New("the Sheet is not private to the person")

func craftChatRefRecord(inner mooring.NativeRecord, chat string, scope mooring.EditLeaseScope, userID string) mooring.NativeRecord {
	return func(ctx context.Context, tx *sql.Tx, r artifacts.VersionReceipt, native []byte) error {
		if err := inner(ctx, tx, r, native); err != nil {
			return err
		}
		// From the person's own private chat a version is stored only into a page that is private to
		// them now: checked here, in the first version's transaction, whatever the
		// earlier update changed. A group chat keeps the page's audience.
		var private, owned int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM chats c WHERE c.id=? AND c.workspace_id=? AND c.created_by=? AND c.visibility='private'),
 EXISTS(SELECT 1 FROM pages p WHERE p.id=? AND p.workspace_id=? AND p.private_audience=1 AND p.owner_user_id=? AND p.owner_crew_id IS NULL AND p.folder_id IS NULL)`,
			chat, scope.WorkspaceID, userID, scope.ArtifactID, scope.WorkspaceID, userID).Scan(&private, &owned); err != nil {
			return err
		}
		if private == 1 && owned != 1 {
			return errCraftPrivateRequired
		}
		now := tsformat.Format(time.Now().UTC())
		if _, err := tx.ExecContext(ctx, `INSERT INTO artifact_chat_refs(chat_id,page_id,workspace_id,human_user_id,created_at) VALUES(?,?,?,?,?) ON CONFLICT(chat_id,page_id) DO NOTHING`,
			chat, scope.ArtifactID, scope.WorkspaceID, userID, now); err != nil {
			return err
		}
		// The Sheet stays with the chat's agent for later chats (it grants nothing by itself).
		_, err := tx.ExecContext(ctx, `INSERT INTO agent_files(agent_id,page_id,workspace_id,added_by,added_at) SELECT c.agent_id,?,?,?,? FROM chats c WHERE c.id=? AND c.workspace_id=? AND true
 ON CONFLICT(agent_id,page_id) DO UPDATE SET removed_at=NULL,removed_by=NULL,added_by=excluded.added_by,added_at=excluded.added_at WHERE agent_files.removed_at IS NOT NULL`,
			scope.ArtifactID, scope.WorkspaceID, userID, now, chat, scope.WorkspaceID)
		return err
	}
}
