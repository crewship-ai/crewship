package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/appengine"
	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/pages"
	"github.com/crewship-ai/crewship/internal/runauthority"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

type gridcraftHTTP struct {
	pages      *PageHandler
	store      *appengine.Store
	engine     appengine.Engine
	operations artifacts.GridCraftOperations
	// editor is the operator switch of the live editing surface.
	editor bool
	// broadcast pushes workbook.committed on a Page channel; nil disables it.
	broadcast        func(prefix, id, eventType string, payload any)
	mooringBroadcast func(prefix, id, eventType string, payload any)
	// editorBundle reports the qualified editor bundle digest and frame URL
	// for a new session opened by the request; nil while the editor is off.
	editorBundle func(*http.Request) (digest, url string)
	// outbox is the router's push sweeper; once started it sends pushes off
	// the response path.
	outbox *gridcraftOutboxWorker
}

// RegisterGridCraft installs only the explicit experimental offline surface.
// Disabled installs no routes. The engine must be an operator-qualified worker.
func (r *Router) RegisterGridCraft(options appengine.Options, engine appengine.Engine) {
	if !options.Enabled || engine == nil {
		return
	}
	store := appengine.New(r.db, options)
	adapter := artifacts.NewGridCraftAdapter(store, engine, true)
	operations, ok := artifacts.NewRegistry(artifacts.EnabledTypes{"gridcraft-workbook": true}, nil, adapter).GridCraft()
	if !ok {
		return
	}
	h := &gridcraftHTTP{pages: r.pages, store: store, engine: engine, operations: operations, editor: options.Editor, editorBundle: r.workbookEditorBundle}
	if r.hub != nil {
		h.broadcast = r.hub.BroadcastChannel
		h.mooringBroadcast = r.hub.BroadcastChannel
	}
	authed, wsCtx := r.authMw.RequireAuth, r.authMw.RequireWorkspace
	internalAuth := func(next http.Handler) http.Handler {
		return r.internalHandler.requireInternal(withHostChatOrigin(next))
	}
	// openapi: responses 200,201,400,401,403,404,409,429,500,503
	r.authedMut("POST", "/api/v1/workbooks", roleInline, h.create)
	// openapi: responses 200,400,401,403,404,409,429,500,503
	r.mux.Handle("GET /api/v1/pages/{slug}/workbook", authed(wsCtx(http.HandlerFunc(h.read))))
	// openapi: responses 200,400,401,403,404,409,429,500,503
	r.authedMut("POST", "/api/v1/pages/{slug}/workbook/operations", roleInline, h.importFile)
	// openapi: responses 200,400,401,403,404,409,429,500,503
	r.authedMut("POST", "/api/v1/pages/{slug}/workbook/edits", roleInline, h.edit)
	// openapi: responses 200,201,400,401,403,404,409,429,500,503
	r.authedMut("POST", "/api/v1/pages/{slug}/workbook/sessions", roleInline, h.openSession)
	// openapi: responses 200,400,401,403,404,500,503
	r.authedMut("DELETE", "/api/v1/pages/{slug}/workbook/sessions/{session}", roleInline, h.closeSession)
	// openapi: responses 200,400,401,403,404,410,429,500,503
	r.mux.Handle("GET /api/v1/pages/{slug}/workbook/commits", authed(wsCtx(http.HandlerFunc(h.commits))))
	// openapi: responses 200,400,401,403,404,409,429,500,503
	r.mux.Handle("GET /api/v1/pages/{slug}/workbook/operations/{mutation}", authed(wsCtx(http.HandlerFunc(h.status))))
	// openapi: responses 200,400,401,403,404,409,429,500,503
	r.authedMut("POST", "/api/v1/pages/{slug}/workbook/mandates", roleInline, h.mandate)
	// openapi: responses 200,400,401,403,404,409,429,500,503
	r.authedMut("DELETE", "/api/v1/pages/{slug}/workbook/mandates/{mandate}", roleInline, h.revoke)
	// openapi: responses 200,400,401,403,404,409,429,500,503
	r.authedMut("POST", "/api/v1/pages/{slug}/workbook/editors", roleInline, h.share)
	// The crew IPC authenticates transport; the original signed run bearer is
	// independently verified by artifactAgentRequest and checked again in SQL.
	// openapi: responses 200,400,403,404,409,429,500,503
	r.mux.Handle("POST /api/v1/internal/gridcraft", internalAuth(http.HandlerFunc(h.agent)))
	r.registerMooring(h)
	r.gridcraftOutbox = &gridcraftOutboxWorker{h: h}
	h.outbox = r.gridcraftOutbox
}
func gridcraftError(w http.ResponseWriter, err error) {
	if artifactStorageRefusal(w, err) {
		return
	}
	if errors.Is(err, appengine.ErrUnauthenticated) {
		// Same wire shape as the auth middleware gives the next request.
		writeAuthError(w, http.StatusUnauthorized, reasonSessionInvalid)
		return
	}
	code, message := 500, "Workbook operation failed"
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		// Still authorized, but the call was interrupted (the caller left).
		code, message = 503, "Workbook unavailable"
	case errors.Is(err, appengine.ErrForbidden), errors.Is(err, appengine.ErrNotFound):
		code, message = 404, "Workbook not found"
	case errors.Is(err, appengine.ErrInvalid), errors.Is(err, appengine.ErrCandidate):
		code, message = 400, "Invalid workbook operation"
	case errors.Is(err, appengine.ErrConflict):
		code, message = 409, "conflict"
	case errors.Is(err, appengine.ErrPrecondition):
		code, message = 409, "precondition_failed"
	case errors.Is(err, appengine.ErrEffects):
		code, message = 403, "effect_out_of_mandate"
	case errors.Is(err, appengine.ErrInProgress):
		code, message = 409, "in_progress"
	case errors.Is(err, appengine.ErrFence):
		code, message = 409, "stale_generation"
	case errors.Is(err, appengine.ErrCapacity):
		code, message = 429, "capacity_exhausted"
	case errors.Is(err, appengine.ErrQuota):
		code, message = 429, "quota_exceeded"
	case errors.Is(err, errWorkbookProposalRequired):
		code, message = 409, "proposal_required: shared workbook changes require proposals; proposal delivery is not available yet"
	case errors.Is(err, appengine.ErrDisabled), errors.Is(err, appengine.ErrEngineUnavailable):
		code, message = 503, "Workbook unavailable"
	}
	replyError(w, code, message)
}
func (h *gridcraftHTTP) humanAuthority(r *http.Request, k appengine.Key, write bool, capture func(*pageRecord, workbookAccess)) appengine.Authorize {
	return func(ctx context.Context, tx *sql.Tx) (appengine.Principal, error) {
		if capture != nil {
			capture(nil, workbookNoAccess)
		}
		ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
		request := r.WithContext(ctx)
		user := UserFromContext(ctx)
		if !gridcraftHumanCaller(request) {
			return appengine.Principal{}, appengine.ErrForbidden
		}
		if err := gridcraftCredential(request, tx, write); err != nil {
			return appengine.Principal{}, err
		}
		rec, access, err := h.pages.workbookHumanAccess(ctx, k.WorkspaceID, k.PageID, user.ID, write)
		if err != nil {
			return appengine.Principal{}, err
		}
		if capture != nil {
			capture(rec, access)
		}
		principal := appengine.Principal{AuthorityID: "user:" + user.ID, MandateID: "human-import", Targets: []string{"*"}}
		if access == workbookEditor {
			// Publication shares the current head, not the private history
			// before it: a named editor's commits feed starts there.
			if principal.HistoryFloor, err = workbookPublicationFloor(ctx, tx, k); err != nil {
				return appengine.Principal{}, err
			}
		}
		return principal, nil
	}
}

// workbookPublicationFloor is the head revision at which the owner published
// the workbook (recorded with artifact.private_output_published). Without that
// record the floor is the current head, so no earlier revision is served.
func workbookPublicationFloor(ctx context.Context, tx *sql.Tx, k appengine.Key) (int64, error) {
	var floor sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT json_extract(metadata,'$.head_rev') FROM audit_logs WHERE workspace_id=? AND entity_type='page' AND entity_id=? AND action='artifact.private_output_published' ORDER BY created_at DESC LIMIT 1`, k.WorkspaceID, k.PageID).Scan(&floor)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if floor.Valid {
		return floor.Int64, nil
	}
	err = tx.QueryRowContext(ctx, `SELECT coalesce(max(head_rev),0) FROM gridcraft_heads WHERE workspace_id=? AND page_id=?`, k.WorkspaceID, k.PageID).Scan(&floor)
	return floor.Int64, err
}

// auditRead keeps the workspace OWNER read audit for a private workbook and
// for a published one the OWNER reaches only through that audit authority.
func (h *gridcraftHTTP) auditRead(w http.ResponseWriter, r *http.Request, rec *pageRecord, access workbookAccess) bool {
	if rec == nil {
		gridcraftError(w, appengine.ErrForbidden)
		return false
	}
	audited := *rec
	if access == workbookAuditor {
		audited.PrivateAudience = true
	}
	return h.pages.auditPrivateOwnerAccess(w, r, &audited)
}

// gridcraftHumanCaller reports whether the request comes from a human user
// directly: not an agent, an acting agent, an internal token or an artifact run.
func gridcraftHumanCaller(r *http.Request) bool {
	ctx := r.Context()
	agent, _ := pageGrantCallerIsAgent(r)
	return UserFromContext(ctx) != nil && !agent && projectAgentFrom(ctx) == nil && ctx.Value(artifactRunKey{}) == nil
}

// gridcraftCredential rechecks the human credential inside the operation's
// transaction. Only a credential that is actually revoked or expired becomes
// ErrUnauthenticated (401): the middleware admitted it, so it was lost while
// the request ran. A missing scope or a storage failure while reading the
// credential keeps the hidden refusal, so a busy database never signs a user
// out.
func gridcraftCredential(r *http.Request, tx *sql.Tx, write bool) error {
	scope := "pages:read"
	if write {
		scope = "pages:write"
	}
	err := validateContentHumanCredential(r, tx, scope)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errInvalidCLIToken), errors.Is(err, errContentCredentialNotCurrent):
		return appengine.ErrUnauthenticated
	default:
		return appengine.ErrForbidden
	}
}

// createAuthority rechecks the existing credential, live membership ceiling and
// Page-create policy through the same transaction that stages the private Page.
func (h *gridcraftHTTP) createAuthority(r *http.Request, tx *sql.Tx, ws, user string) error {
	if e := gridcraftCredential(r, tx, true); e != nil {
		return e
	}
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	role, e := h.pages.liveContentHumanRole(ctx, ws, user)
	if e != nil {
		return appengine.ErrForbidden
	}
	var caps sql.NullString
	var currentRole string
	if e = tx.QueryRowContext(ctx, `SELECT capabilities,role FROM workspace_members WHERE workspace_id=? AND user_id=?`, ws, user).Scan(&caps, &currentRole); e != nil {
		return appengine.ErrForbidden
	}
	if currentRole != role || (!canRole(role, "create") && !HasCapability(resolveCapabilitiesFromRow(caps, role), CapabilityPageCreate)) {
		return appengine.ErrForbidden
	}
	return nil
}

func (h *gridcraftHTTP) humanKey(r *http.Request, write bool) (appengine.Key, error) {
	k := appengine.Key{WorkspaceID: WorkspaceIDFromContext(r.Context())}
	column, ref := "slug", r.PathValue("slug")
	if strings.HasPrefix(ref, "@") {
		column, ref = "id", strings.TrimPrefix(ref, "@")
	}
	// A read transaction: resolving the key never writes, so it must not
	// take the database-wide write lock.
	tx, err := h.pages.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return k, err
	}
	defer tx.Rollback()
	// An agent or internal caller is refused as not found before any credential
	// check. For a human, the credential comes before the slug lookup: a revoked
	// credential gets the same answer whether or not the Page exists.
	if !gridcraftHumanCaller(r.WithContext(context.WithValue(r.Context(), pageAuthReaderKey{}, tx))) {
		return k, appengine.ErrForbidden
	}
	if err = gridcraftCredential(r, tx, write); err != nil {
		return k, err
	}
	if tx.QueryRowContext(r.Context(), "SELECT id FROM pages WHERE workspace_id=? AND "+column+"=?", k.WorkspaceID, ref).Scan(&k.PageID) != nil {
		return k, appengine.ErrForbidden
	}
	_, err = h.humanAuthority(r, k, write, nil)(r.Context(), tx)
	return k, err
}

type gridcraftCreate struct {
	CreatedByTest bool   `json:"created_by_test,omitempty"`
	Slug          string `json:"slug"`
	Name          string `json:"name"`
}

type gridcraftCreated struct {
	PageID string `json:"page_id"`
	Slug   string `json:"slug"`
	Name   string `json:"name"`
}

// maxEmptyWorkbookPages bounds the empty private Pages one author may stage,
// so nobody can exhaust the shared workspace Page limit with Pages that
// administrators cannot see.
const maxEmptyWorkbookPages = 5

// create stages the empty personal private Page a human then imports into.
// The slug is the idempotency key: an identical retry by the same author
// returns the same still-empty Page; anything else is a conflict, and an
// existing Page is never converted to a private workbook Page.
func (h *gridcraftHTTP) create(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	user := UserFromContext(r.Context())
	if agent, _ := pageGrantCallerIsAgent(r); user == nil || agent || projectAgentFrom(r.Context()) != nil {
		gridcraftError(w, appengine.ErrForbidden)
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())
	if !requireRoleOrCapabilityOrForbid(w, r, h.pages.logger, h.pages.db, wsID, user.ID,
		RoleFromContext(r.Context()), CapabilityPageCreate, "page.create", "page:new", "create") {
		return
	}
	var req gridcraftCreate
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	raw, err := io.ReadAll(r.Body)
	if err != nil || appengine.DecodeStrict(raw, &req) != nil {
		gridcraftError(w, appengine.ErrInvalid)
		return
	}
	spec := pages.Document{APIVersion: pages.DocumentAPIVersion, Kind: pages.DocumentKind, Metadata: pages.Metadata{Slug: req.Slug, Name: req.Name}}
	if spec.ValidateEmpty() != nil || len(req.Name) > 200 {
		gridcraftError(w, appengine.ErrInvalid)
		return
	}
	specJSON, err := json.Marshal(spec)
	if err != nil {
		gridcraftError(w, err)
		return
	}
	tx, err := h.pages.db.BeginTx(r.Context(), nil)
	if err != nil {
		gridcraftError(w, err)
		return
	}
	defer tx.Rollback()
	if h.createAuthority(r, tx, wsID, user.ID) != nil {
		replyError(w, 403, "Forbidden")
		return
	}
	var existingTest bool
	var existing gridcraftCreated
	var owner, name string
	var private bool
	err = tx.QueryRowContext(r.Context(), `SELECT id,coalesce(owner_user_id,''),name,created_by_test,private_audience=1 AND folder_id IS NULL AND owner_crew_id IS NULL FROM pages WHERE workspace_id=? AND slug=?`, wsID, req.Slug).Scan(&existing.PageID, &owner, &name, &existingTest, &private)
	switch {
	case err == nil:
		empty, emptyErr := appengine.PageHasNoContent(r.Context(), tx, existing.PageID)
		if emptyErr != nil {
			gridcraftError(w, emptyErr)
			return
		}
		if owner != user.ID || !private || name != req.Name || !empty || existingTest != req.CreatedByTest {
			replyError(w, 409, "conflict")
			return
		}
		writeJSON(w, 200, gridcraftCreated{PageID: existing.PageID, Slug: req.Slug, Name: req.Name})
		return
	case !errors.Is(err, sql.ErrNoRows):
		gridcraftError(w, err)
		return
	}
	var count int
	if err = tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM pages WHERE workspace_id=?`, wsID).Scan(&count); err != nil {
		gridcraftError(w, err)
		return
	}
	if count >= pages.MaxPagesPerWorkspace {
		replyError(w, 409, "workspace page limit reached")
		return
	}
	var staged int
	if err = tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM pages p WHERE p.workspace_id=? AND p.owner_user_id=? AND p.private_audience=1 AND p.created_by_agent_id IS NULL AND NOT EXISTS(SELECT 1 FROM gridcraft_heads g WHERE g.page_id=p.id) AND NOT EXISTS(SELECT 1 FROM documents d WHERE d.page_id=p.id) AND NOT EXISTS(SELECT 1 FROM slides_heads s WHERE s.page_id=p.id)`, wsID, user.ID).Scan(&staged); err != nil {
		gridcraftError(w, err)
		return
	}
	if staged >= maxEmptyWorkbookPages {
		replyError(w, 409, "too many empty workbook pages; import into or delete one first")
		return
	}
	pageID, stamp := generateCUID(), tsformat.Format(time.Now())
	if _, err = tx.ExecContext(r.Context(), `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,created_at,updated_at,private_audience,created_by_test) VALUES(?,?,?,?,?,?,?,?,1,?)`, pageID, wsID, req.Slug, req.Name, user.ID, string(specJSON), stamp, stamp, req.CreatedByTest); err != nil {
		if isUniqueViolation(err) {
			replyError(w, 409, "conflict")
			return
		}
		gridcraftError(w, err)
		return
	}
	if err = insertPageVersion(r.Context(), tx, pageID, 1, string(specJSON), user.ID, stamp); err != nil {
		gridcraftError(w, err)
		return
	}
	if h.createAuthority(r, tx, wsID, user.ID) != nil {
		replyError(w, 403, "Forbidden")
		return
	}
	if err = tx.Commit(); err != nil {
		gridcraftError(w, err)
		return
	}
	broadcastWorkspaceEvent(h.pages.hub, wsID, "page.updated", map[string]any{})
	writeJSON(w, 201, gridcraftCreated{PageID: pageID, Slug: req.Slug, Name: req.Name})
}

func (h *gridcraftHTTP) read(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	k, err := h.humanKey(r, false)
	if err != nil {
		gridcraftError(w, err)
		return
	}
	var record *pageRecord
	var access workbookAccess
	state, err := h.operations.Read(r.Context(), k, h.humanAuthority(r, k, false, func(p *pageRecord, a workbookAccess) { record, access = p, a }))
	if err != nil {
		gridcraftError(w, err)
		return
	}
	if !h.auditRead(w, r, record, access) {
		return
	}
	writeJSON(w, 200, state)
}
func decodeGridCraftBody(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, appengine.MaxSnapshotBytes+16384)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return appengine.ErrInvalid
	}
	return appengine.DecodeStrict(raw, target)
}
func (h *gridcraftHTTP) importFile(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	k, err := h.humanKey(r, true)
	if err != nil {
		gridcraftError(w, err)
		return
	}
	var req appengine.Request
	if err = decodeGridCraftBody(w, r, &req); err != nil {
		gridcraftError(w, err)
		return
	}
	// Client sessions belong to cell edits; an import is never attributed to one.
	if req.Command.Kind != "import" || req.MandateID != "human-import" || req.SessionID != "" {
		gridcraftError(w, appengine.ErrInvalid)
		return
	}
	result, err := h.operations.Apply(r.Context(), k, req, h.humanAuthority(r, k, true, nil))
	var precondition *appengine.PreconditionError
	if errors.As(err, &precondition) {
		h.writePrecondition(w, r, precondition)
		return
	}
	if err != nil {
		gridcraftError(w, err)
		return
	}
	writeJSON(w, 200, result)
	h.published(k.PageID)
}

// edit commits one atomic human cell edit from a live editor session. The Page
// owner and, on a published workbook, the editors the owner named may edit
// (workbookHumanAccess); the writer for cell-level concurrency is user and
// session.
func (h *gridcraftHTTP) edit(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	k, err := h.humanKey(r, true)
	if err != nil {
		gridcraftError(w, err)
		return
	}
	if !h.editorOn(r.Context(), h.pages.db, k.WorkspaceID) {
		editorError(w, errGridCraftEditorDisabled)
		return
	}
	var body appengine.Edit
	if err = decodeGridCraftBody(w, r, &body); err != nil {
		gridcraftError(w, err)
		return
	}
	req, err := body.Request()
	if err != nil {
		gridcraftError(w, err)
		return
	}
	var seen liveEditorSession
	result, err := h.operations.Apply(r.Context(), k, req, h.sessionAuthority(r, k, req.SessionID, &seen))
	var precondition *appengine.PreconditionError
	if errors.As(err, &precondition) {
		h.writePrecondition(w, r, precondition)
		return
	}
	if err != nil {
		if !editorError(w, err) {
			gridcraftError(w, err)
		}
		return
	}
	h.renewOnUse(r, req.SessionID, seen)
	writeJSON(w, 200, result)
	h.published(k.PageID)
}

type gridcraftConflict struct {
	Sheet      string `json:"sheet"`
	Cell       string `json:"cell"`
	Input      string `json:"input"`
	Rev        int64  `json:"rev"`
	ActorLabel string `json:"actor_label"`
}

// writePrecondition reports the head revision, whether the client must reread
// the workbook, and each conflicting cell's input in the head with the revision
// and writer that set it. Writers are shown only by display label, never by
// internal authority or session.
func (h *gridcraftHTTP) writePrecondition(w http.ResponseWriter, r *http.Request, p *appengine.PreconditionError) {
	labels := map[string]string{}
	conflicts := make([]gridcraftConflict, 0, len(p.Conflicts))
	for _, c := range p.Conflicts {
		label, ok := labels[c.AuthorityID]
		if !ok {
			label = h.actorLabel(r.Context(), c.AuthorityID)
			labels[c.AuthorityID] = label
		}
		conflicts = append(conflicts, gridcraftConflict{c.Sheet, c.Cell, c.Input, c.Rev, label})
	}
	writeJSON(w, 409, map[string]any{"error": "precondition_failed", "head_rev": p.HeadRev, "snapshot_required": p.SnapshotRequired, "conflicts": conflicts, "reason": p.Reason})
}
func (h *gridcraftHTTP) actorLabel(ctx context.Context, authority string) string {
	var label sql.NullString
	switch {
	case strings.HasPrefix(authority, "user:"):
		_ = h.pages.db.QueryRowContext(ctx, `SELECT nullif(full_name,'') FROM users WHERE id=?`, strings.TrimPrefix(authority, "user:")).Scan(&label)
	case strings.HasPrefix(authority, "agent:"):
		agent, _, _ := strings.Cut(strings.TrimPrefix(authority, "agent:"), ":")
		_ = h.pages.db.QueryRowContext(ctx, `SELECT nullif(name,'') FROM agents WHERE id=?`, agent).Scan(&label)
	}
	if label.Valid {
		return label.String
	}
	return "Another editor"
}

func (h *gridcraftHTTP) status(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	k, err := h.humanKey(r, false)
	if err != nil {
		gridcraftError(w, err)
		return
	}
	var record *pageRecord
	var access workbookAccess
	result, err := h.store.Status(r.Context(), k, r.PathValue("mutation"), h.humanAuthority(r, k, false, func(p *pageRecord, a workbookAccess) { record, access = p, a }))
	if err != nil {
		gridcraftError(w, err)
		return
	}
	if !h.auditRead(w, r, record, access) {
		return
	}
	writeJSON(w, 200, result)
}

type gridcraftMandate struct {
	MandateID  string   `json:"mandate_id"`
	RunID      string   `json:"run_id"`
	BaseRev    int64    `json:"base_rev"`
	Generation int64    `json:"generation"`
	Targets    []string `json:"targets"`
}

func (h *gridcraftHTTP) mandate(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	k, err := h.humanKey(r, true)
	if err != nil {
		gridcraftError(w, err)
		return
	}
	var req gridcraftMandate
	if err = decodeGridCraftBody(w, r, &req); err != nil {
		gridcraftError(w, err)
		return
	}
	// Mandate names of human operations ("human-import", "human-edit") are
	// reserved, so an agent revision is never recorded under one.
	if len(req.MandateID) == 0 || len(req.MandateID) > 256 || strings.HasPrefix(req.MandateID, appengine.HumanMandatePrefix) || len(req.RunID) == 0 || len(req.Targets) != 1 || req.Targets[0] == "" || len(req.Targets[0]) > 256 || req.BaseRev < 1 || req.Generation < 1 {
		gridcraftError(w, appengine.ErrInvalid)
		return
	}
	tx, err := h.pages.db.BeginTx(r.Context(), nil)
	if err != nil {
		gridcraftError(w, err)
		return
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(r.Context(), `UPDATE gridcraft_writer_fence SET seq=seq WHERE id=1`); err != nil {
		gridcraftError(w, err)
		return
	}
	var record *pageRecord
	var access workbookAccess
	p, err := h.humanAuthority(r, k, true, func(rec *pageRecord, a workbookAccess) { record, access = rec, a })(r.Context(), tx)
	if err != nil {
		gridcraftError(w, err)
		return
	}
	// Shared content requires the approved proposal path, not a direct mandate.
	wildcard := req.Targets[0] == "*"
	if record == nil || access != workbookOwner || !record.PrivateAudience {
		gridcraftError(w, errWorkbookProposalRequired)
		return
	}
	if wildcard {
		gridcraftError(w, appengine.ErrInvalid)
		return
	}
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	a, err := h.run(ctx, tx, req.RunID, k.WorkspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		err = h.retainOrdinaryHumanRun(r, tx, req.RunID, k.WorkspaceID, p.AuthorityID)
		if err == nil {
			a, err = h.run(ctx, tx, req.RunID, k.WorkspaceID)
		}
	}

	if err != nil || p.AuthorityID != "user:"+a.HumanUserID || h.pages.checkArtifactRunPrincipal(ctx, &a) != nil || !h.pages.artifactAgentPolicyAllows(ctx, a.WorkspaceID, a.CrewID) {
		gridcraftError(w, appengine.ErrForbidden)
		return
	}
	// The private chat seal is durable before the first agent read or prompt copy.
	var sealed int
	if tx.QueryRowContext(ctx, `SELECT count(*) FROM chats WHERE id=? AND workspace_id=? AND created_by=? AND visibility='private' AND (private_artifact_author_user_id IS NULL OR private_artifact_author_user_id=?)`, a.ChatID, a.WorkspaceID, a.HumanUserID, a.HumanUserID).Scan(&sealed) != nil || sealed != 1 {
		gridcraftError(w, appengine.ErrForbidden)
		return
	}
	var rev, generation, pageVersion, policyVersion int64
	if tx.QueryRowContext(ctx, `SELECT g.head_rev,g.generation,p.pages_version FROM gridcraft_heads g JOIN pages p ON p.id=g.page_id AND p.workspace_id=g.workspace_id WHERE g.workspace_id=? AND g.page_id=?`, k.WorkspaceID, k.PageID).Scan(&rev, &generation, &pageVersion) != nil {
		gridcraftError(w, appengine.ErrNotFound)
		return
	}
	if tx.QueryRowContext(ctx, `SELECT version FROM workspace_artifact_creation_policy WHERE workspace_id=? AND enabled=1`, k.WorkspaceID).Scan(&policyVersion) != nil {
		gridcraftError(w, appengine.ErrForbidden)
		return
	}
	targets, _ := json.Marshal(req.Targets)
	payload, _ := json.Marshal(req)
	digest := sha256.Sum256(payload)
	fingerprint := hex.EncodeToString(digest[:])
	var old string
	e := tx.QueryRowContext(ctx, `SELECT payload_hash FROM gridcraft_intents WHERE workspace_id=? AND page_id=? AND mandate_id=? AND human_user_id=? AND revoked_at IS NULL`, k.WorkspaceID, k.PageID, req.MandateID, a.HumanUserID).Scan(&old)
	if e == nil {
		if old != fingerprint {
			gridcraftError(w, appengine.ErrConflict)
			return
		}
	} else {
		if !errors.Is(e, sql.ErrNoRows) {
			gridcraftError(w, e)
			return
		}
		if req.BaseRev != rev || req.Generation != generation {
			gridcraftError(w, appengine.ErrPrecondition)
			return
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO gridcraft_intents(workspace_id,page_id,mandate_id,human_user_id,run_id,base_rev,generation,page_version,policy_version,targets_json,payload_hash) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, k.WorkspaceID, k.PageID, req.MandateID, a.HumanUserID, a.RunID, req.BaseRev, req.Generation, pageVersion, policyVersion, string(targets), fingerprint)
		if err != nil {
			gridcraftError(w, appengine.ErrConflict)
			return
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE chats SET private_artifact_author_user_id=? WHERE id=?`, a.HumanUserID, a.ChatID); err != nil {
		gridcraftError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		gridcraftError(w, err)
		return
	}
	writeJSON(w, 200, req)
}

// Explicit human workbook admission may retain an ordinary host-dispatched
// chat run. It uses only the original durable run receipt, never caller/agent
// metadata, and adds no consent or admission behavior to the Docs subsystem.
func (h *gridcraftHTTP) retainOrdinaryHumanRun(r *http.Request, tx *sql.Tx, run, workspace, authority string) error {
	var agent, crew string
	if err := tx.QueryRowContext(r.Context(), `SELECT agent_id,crew_id FROM journal_entries WHERE trace_id=? AND workspace_id=? AND entry_type='run.started' AND json_extract(payload,'$.host_admitted')=1 AND json_extract(payload,'$.trigger_type')='USER'`, run, workspace).Scan(&agent, &crew); err != nil {
		return appengine.ErrForbidden
	}
	origin, err := runauthority.Resolve(r.Context(), tx, run, agent, workspace)
	if err != nil || origin.HumanAuthority == nil || origin.ChatOriginID != "" || origin.TriggerMessageID == "" || authority != "user:"+origin.HumanAuthority.UserID {
		return appengine.ErrForbidden
	}
	return captureArtifactChatPrincipalQuery(r, tx, run, workspace, agent, crew, origin.ChatID, origin.TriggerMessageID, origin.HumanAuthority)
}

func (h *gridcraftHTTP) run(ctx context.Context, tx *sql.Tx, run, workspace string) (a artifactRunAuthority, err error) {
	err = tx.QueryRowContext(ctx, `SELECT run_id,workspace_id,agent_id,crew_id,human_user_id,chat_id,message_id FROM artifact_run_principals WHERE run_id=? AND workspace_id=?`, run, workspace).Scan(&a.RunID, &a.WorkspaceID, &a.AgentID, &a.CrewID, &a.HumanUserID, &a.ChatID, &a.MessageID)
	return
}
func (h *gridcraftHTTP) revoke(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	k, err := h.humanKey(r, true)
	if err != nil {
		gridcraftError(w, err)
		return
	}
	tx, err := h.pages.db.BeginTx(r.Context(), nil)
	if err != nil {
		gridcraftError(w, err)
		return
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(r.Context(), `UPDATE gridcraft_writer_fence SET seq=seq WHERE id=1`); err != nil {
		gridcraftError(w, err)
		return
	}
	p, err := h.humanAuthority(r, k, true, nil)(r.Context(), tx)
	if err != nil {
		gridcraftError(w, err)
		return
	}
	_, err = tx.ExecContext(r.Context(), `UPDATE gridcraft_intents SET revoked_at=? WHERE workspace_id=? AND page_id=? AND mandate_id=? AND human_user_id=? AND revoked_at IS NULL`, tsformat.Format(time.Now()), k.WorkspaceID, k.PageID, r.PathValue("mandate"), strings.TrimPrefix(p.AuthorityID, "user:"))
	if err != nil {
		gridcraftError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		gridcraftError(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "revoked"})
}

type gridcraftAgentRequest struct {
	Action     string             `json:"action"`
	ArtifactID string             `json:"artifact_id"`
	MandateID  string             `json:"mandate_id"`
	MutationID string             `json:"mutation_id,omitempty"`
	Request    *appengine.Request `json:"request,omitempty"`
	// Since and Limit page the commits action.
	Since *int64 `json:"since,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

func (h *gridcraftHTTP) agent(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	checked, ok := h.pages.artifactAgentRequest(w, r)
	if !ok {
		return
	}
	r = checked
	var body gridcraftAgentRequest
	if err := decodeGridCraftBody(w, r, &body); err != nil {
		gridcraftError(w, err)
		return
	}
	a, _ := r.Context().Value(artifactRunKey{}).(*artifactRunAuthority)
	if a == nil {
		gridcraftError(w, appengine.ErrForbidden)
		return
	}
	k := appengine.Key{WorkspaceID: a.WorkspaceID, PageID: body.ArtifactID}
	authority := func(ctx context.Context, tx *sql.Tx) (appengine.Principal, error) {
		ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
		if strings.HasPrefix(body.MandateID, appengine.HumanMandatePrefix) || h.pages.checkArtifactRunPrincipal(ctx, a) != nil || !h.pages.artifactAgentPolicyAllows(ctx, a.WorkspaceID, a.CrewID) {
			return appengine.Principal{}, appengine.ErrForbidden
		}
		role, err := h.pages.liveContentHumanRole(ctx, a.WorkspaceID, a.HumanUserID)
		if err != nil {
			return appengine.Principal{}, appengine.ErrForbidden
		}
		ctx = context.WithValue(ctx, ctxRole, role)
		rec, err := h.pages.loadPage(ctx, a.WorkspaceID, "@"+k.PageID)
		shared := err == nil && !rec.PrivateAudience && rec.HasWorkbook && rec.OwnerUserID == a.HumanUserID && rec.OwnerCrewID == "" && rec.FolderID == ""
		if shared {
			// Recheck at use as well: legacy DIRECT mandates grant no shared access.
			return appengine.Principal{}, errWorkbookProposalRequired
		} else if err != nil || rec.OwnerUserID != a.HumanUserID || rec.OwnerCrewID != "" || !rec.PrivateAudience || rec.FolderID != "" || !h.pages.mayEditSpec(ctx, a.WorkspaceID, a.HumanUserID, role, rec) {
			return appengine.Principal{}, appengine.ErrForbidden
		}
		var targets string
		var base, generation, pageVersion, policyVersion int64
		if tx.QueryRowContext(ctx, `SELECT targets_json,base_rev,generation,page_version,policy_version FROM gridcraft_intents WHERE workspace_id=? AND page_id=? AND mandate_id=? AND run_id=? AND human_user_id=? AND revoked_at IS NULL`, k.WorkspaceID, k.PageID, body.MandateID, a.RunID, a.HumanUserID).Scan(&targets, &base, &generation, &pageVersion, &policyVersion) != nil {
			return appengine.Principal{}, appengine.ErrForbidden
		}
		var livePolicy bool
		if tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workspace_artifact_creation_policy WHERE workspace_id=? AND enabled=1 AND version=?)`, k.WorkspaceID, policyVersion).Scan(&livePolicy) != nil || !livePolicy {
			return appengine.Principal{}, appengine.ErrForbidden
		}
		if rec.PagesVersion != pageVersion {
			return appengine.Principal{}, appengine.ErrForbidden
		}
		if body.Request != nil && (body.Request.MandateID != body.MandateID || body.Request.BaseRev != base || body.Request.Generation != generation) {
			return appengine.Principal{}, appengine.ErrForbidden
		}
		var bounds []string
		if json.Unmarshal([]byte(targets), &bounds) != nil {
			return appengine.Principal{}, appengine.ErrForbidden
		}

		// The mandate's base revision is also the agent's history floor: the
		// commits feed never shows it what changed before the mandate.
		return appengine.Principal{AuthorityID: "agent:" + a.AgentID + ":run:" + a.RunID + ":mandate:" + body.MandateID, MandateID: body.MandateID, Targets: bounds, HistoryFloor: base}, nil
	}
	if body.Action != "commits" && (body.Since != nil || body.Limit != 0) {
		gridcraftError(w, appengine.ErrInvalid)
		return
	}
	switch body.Action {
	case "read":
		if body.Request != nil || body.MutationID != "" {
			gridcraftError(w, appengine.ErrInvalid)
			return
		}
		result, err := h.operations.Read(r.Context(), k, authority)
		if err != nil {
			gridcraftError(w, err)
			return
		}
		writeJSON(w, 200, result)
	case "apply":
		// An agent is one writer per mandate; client sessions are a human concept.
		if body.Request == nil || body.Request.Command.Kind != "cell.set" || body.Request.SessionID != "" || body.MutationID != "" {
			// An agent never imports; on a shared workbook that change would
			// need a proposal.
			if body.Request != nil && body.Request.Command.Kind == "import" && h.agentSharedWorkbook(r.Context(), a, k) {
				gridcraftError(w, errWorkbookProposalRequired)
				return
			}
			gridcraftError(w, appengine.ErrInvalid)
			return
		}
		result, err := h.operations.Apply(r.Context(), k, *body.Request, authority)
		var precondition *appengine.PreconditionError
		if errors.As(err, &precondition) {
			h.writePrecondition(w, r, precondition)
			return
		}
		if err != nil {
			gridcraftError(w, err)
			return
		}
		writeJSON(w, 200, result)
		h.published(k.PageID)
	case "commits":
		// The agent reads the same feed under its mandate's current authority;
		// its own revisions are those of this mandate.
		if body.Request != nil || body.MutationID != "" || body.Since == nil {
			gridcraftError(w, appengine.ErrInvalid)
			return
		}
		limit := body.Limit
		if limit == 0 {
			limit = appengine.DefaultFeedLimit
		}
		if !h.editorOn(r.Context(), h.pages.db, a.WorkspaceID) {
			editorError(w, errGridCraftEditorDisabled)
			return
		}
		feed, err := h.store.Commits(r.Context(), k, *body.Since, limit, authority)
		var gone *appengine.HistoryGoneError
		if errors.As(err, &gone) {
			writeHistoryGone(w, gone)
			return
		}
		if err != nil {
			gridcraftError(w, err)
			return
		}
		writeJSON(w, 200, h.renderFeed(r.Context(), feed))
	case "status":
		if body.Request != nil || body.MutationID == "" {
			gridcraftError(w, appengine.ErrInvalid)
			return
		}
		result, err := h.store.Status(r.Context(), k, body.MutationID, authority)
		if err != nil {
			gridcraftError(w, err)
			return
		}
		writeJSON(w, 200, result)
	default:
		gridcraftError(w, appengine.ErrInvalid)
	}
}
