package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/appengine"
	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/mooring"
	"github.com/crewship-ai/crewship/internal/pages"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

const (
	mooringCreated         journal.EntryType = "artifact.mooring_created"
	mooringCommitted       journal.EntryType = "artifact.mooring_committed"
	mooringMandateAdmitted journal.EntryType = "artifact.mooring_mandate_admitted"
	mooringMandateRevoked  journal.EntryType = "artifact.mooring_mandate_revoked"
)

type mooringHTTP struct {
	workbook *gridcraftHTTP
	storage  artifacts.RevisionStorage[mooring.Key, mooring.Request, mooring.State, mooring.Receipt, mooring.Authorize]
}

func (r *Router) registerMooring(h *gridcraftHTTP) {
	m := &mooringHTTP{h, mooring.JSONAdapter{}}
	authed, wsCtx := r.authMw.RequireAuth, r.authMw.RequireWorkspace
	// openapi: responses 200,401,403
	r.mux.Handle("GET /api/v1/mooring/apps", authed(wsCtx(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) { writeJSON(w, 200, mooring.Applications()) }))))
	// openapi: responses 200,201,400,401,403,404,409,500,503
	r.authedMut("POST", "/api/v1/mooring/documents", roleInline, m.create)
	// openapi: responses 200,400,401,403,404,500,503
	r.mux.Handle("GET /api/v1/pages/{slug}/mooring", authed(wsCtx(http.HandlerFunc(m.read))))
	// openapi: responses 200,400,401,403,404,409,500,503
	r.authedMut("POST", "/api/v1/pages/{slug}/mooring/commands", roleInline, m.command)
	// openapi: responses 200,400,401,403,404,409,500,503
	r.authedMut("POST", "/api/v1/pages/{slug}/mooring/mandates", roleInline, m.mandate)
	// openapi: responses 200,400,401,403,404,500,503
	r.authedMut("DELETE", "/api/v1/pages/{slug}/mooring/mandates/{mandate}", roleInline, m.revoke)
	// openapi: responses 200,400,403,404,409,500,503
	r.mux.Handle("POST /api/v1/internal/mooring", r.internalHandler.requireInternal(withHostChatOrigin(http.HandlerFunc(m.agent))))
}
func mooringError(w http.ResponseWriter, e error) {
	if errors.Is(e, mooring.ErrConflict) {
		replyError(w, 409, "precondition_failed")
		return
	}
	if errors.Is(e, mooring.ErrInvalid) {
		replyError(w, 400, "invalid Mooring command")
		return
	}
	gridcraftError(w, e)
}
func (m *mooringHTTP) begin(r *http.Request) (*sql.Tx, error) {
	tx, e := m.workbook.pages.db.BeginTx(r.Context(), nil)
	if e != nil {
		return nil, e
	}
	if _, e = tx.ExecContext(r.Context(), `UPDATE gridcraft_writer_fence SET seq=seq WHERE id=1`); e != nil {
		tx.Rollback()
		return nil, e
	}
	var sync int
	if e = tx.QueryRowContext(r.Context(), `PRAGMA synchronous`).Scan(&sync); e != nil || sync < 2 {
		tx.Rollback()
		return nil, appengine.ErrInvalid
	}
	return tx, nil
}
func (m *mooringHTTP) human(r *http.Request, tx *sql.Tx, write bool) (appengine.Key, string, error) {
	k := appengine.Key{WorkspaceID: WorkspaceIDFromContext(r.Context())}
	if !gridcraftHumanCaller(r) {
		return k, "", appengine.ErrForbidden
	}
	if e := gridcraftCredential(r, tx, write); e != nil {
		return k, "", e
	}
	column, ref := "slug", r.PathValue("slug")
	if strings.HasPrefix(ref, "@") {
		column, ref = "id", strings.TrimPrefix(ref, "@")
	}
	if tx.QueryRowContext(r.Context(), `SELECT id FROM pages WHERE workspace_id=? AND `+column+`=?`, k.WorkspaceID, ref).Scan(&k.PageID) != nil {
		return k, "", appengine.ErrNotFound
	}
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	user := UserFromContext(ctx)
	rec, access, e := m.workbook.pages.workbookHumanAccess(ctx, k.WorkspaceID, k.PageID, user.ID, write)
	if e != nil || rec == nil || !rec.PrivateAudience || access != workbookOwner || rec.HasWorkbook {
		return k, "", appengine.ErrForbidden
	}
	return k, "user:" + user.ID, nil
}
func (m *mooringHTTP) create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AppID string `json:"app_id"`
		Slug  string `json:"slug"`
		Name  string `json:"name"`
	}
	if decodeGridCraftBody(w, r, &req) != nil {
		mooringError(w, mooring.ErrInvalid)
		return
	}
	manifest, e := mooring.Application(req.AppID)
	if e != nil || manifest.Format != "mooring-json" {
		mooringError(w, mooring.ErrInvalid)
		return
	}
	user := UserFromContext(r.Context())
	if user == nil || !gridcraftHumanCaller(r) {
		mooringError(w, appengine.ErrForbidden)
		return
	}
	ws := WorkspaceIDFromContext(r.Context())
	if !requireRoleOrCapabilityOrForbid(w, r, m.workbook.pages.logger, m.workbook.pages.db, ws, user.ID, RoleFromContext(r.Context()), CapabilityPageCreate, "page.create", "page:new", "create") {
		return
	}
	spec := pages.Document{APIVersion: pages.DocumentAPIVersion, Kind: pages.DocumentKind, Metadata: pages.Metadata{Slug: req.Slug, Name: req.Name}}
	if spec.ValidateEmpty() != nil || len(req.Name) > 200 {
		mooringError(w, mooring.ErrInvalid)
		return
	}
	tx, e := m.begin(r)
	if e != nil {
		mooringError(w, e)
		return
	}
	defer tx.Rollback()
	if e = gridcraftCredential(r, tx, true); e != nil {
		mooringError(w, e)
		return
	}
	var id, owner, app, name string
	e = tx.QueryRowContext(r.Context(), `SELECT p.id,p.owner_user_id,m.app_id,p.name FROM pages p LEFT JOIN mooring_documents m ON m.page_id=p.id WHERE p.workspace_id=? AND p.slug=?`, ws, req.Slug).Scan(&id, &owner, &app, &name)
	if e == nil {
		if owner != user.ID || app != req.AppID || name != req.Name {
			mooringError(w, mooring.ErrConflict)
			return
		}
		writeJSON(w, 200, map[string]string{"page_id": id, "slug": req.Slug, "name": name, "app_id": app})
		return
	}
	if !errors.Is(e, sql.ErrNoRows) {
		mooringError(w, mooring.ErrConflict)
		return
	}
	var count int
	if e = tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM pages WHERE workspace_id=?`, ws).Scan(&count); e != nil || count >= pages.MaxPagesPerWorkspace {
		mooringError(w, mooring.ErrConflict)
		return
	}
	id = generateCUID()
	stamp := tsformat.Format(time.Now())
	raw, _ := json.Marshal(spec)
	_, e = tx.ExecContext(r.Context(), `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,created_at,updated_at,private_audience) VALUES(?,?,?,?,?,?,?,?,1)`, id, ws, req.Slug, req.Name, user.ID, string(raw), stamp, stamp)
	if e == nil {
		e = insertPageVersion(r.Context(), tx, id, 1, string(raw), user.ID, stamp)
	}
	if e == nil {
		_, e = tx.ExecContext(r.Context(), `INSERT INTO mooring_documents(workspace_id,page_id,app_id,rev,generation,snapshot) VALUES(?,?,?,1,1,?)`, ws, id, req.AppID, string(manifest.InitialState))
	}
	var notify func()
	if e == nil {
		notify, e = m.audit(r, tx, ws, id, mooringCreated, user.ID, map[string]any{"app_id": req.AppID})
	}
	if e == nil {
		e = m.createAuthority(r, tx, ws, user.ID)
	}
	if e == nil {
		var valid bool
		e = tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM pages p JOIN mooring_documents d ON d.page_id=p.id AND d.workspace_id=p.workspace_id WHERE p.id=? AND p.workspace_id=? AND p.slug=? AND p.owner_user_id=? AND p.owner_crew_id IS NULL AND p.folder_id IS NULL AND p.private_audience=1 AND d.app_id=? AND d.rev=1 AND d.generation=1 AND d.snapshot=?)`, id, ws, req.Slug, user.ID, req.AppID, string(manifest.InitialState)).Scan(&valid)
		if e == nil && !valid {
			e = appengine.ErrForbidden
		}
	}
	if e == nil {
		e = tx.Commit()
	}
	if e != nil {
		mooringError(w, e)
		return
	}
	if notify != nil {
		notify()
	}
	writeJSON(w, 201, map[string]string{"page_id": id, "slug": req.Slug, "name": req.Name, "app_id": req.AppID})
}
func (m *mooringHTTP) document(ctx context.Context, tx *sql.Tx, k appengine.Key) (mooring.Manifest, mooring.State, error) {
	var app string
	if e := tx.QueryRowContext(ctx, `SELECT app_id FROM mooring_documents WHERE workspace_id=? AND page_id=?`, k.WorkspaceID, k.PageID).Scan(&app); e != nil {
		return mooring.Manifest{}, mooring.State{}, appengine.ErrNotFound
	}
	manifest, e := mooring.Application(app)
	if e != nil {
		return manifest, mooring.State{}, appengine.ErrDisabled
	}
	state, e := m.storage.Read(ctx, mooring.Key{Transaction: tx, PageID: k.PageID, Manifest: manifest}, func(context.Context, *sql.Tx) (string, error) { return "host-read", nil })
	return manifest, state, e
}
func (m *mooringHTTP) read(w http.ResponseWriter, r *http.Request) {
	tx, e := m.begin(r)
	if e != nil {
		mooringError(w, e)
		return
	}
	defer tx.Rollback()
	k, _, e := m.human(r, tx, false)
	if e != nil {
		mooringError(w, e)
		return
	}
	manifest, state, e := m.document(r.Context(), tx, k)
	if e != nil {
		mooringError(w, e)
		return
	}
	editor := map[string]string{}
	if m.workbook.editorBundle != nil {
		_, entry := m.workbook.editorBundle(r)
		if i := strings.LastIndex(entry, "/b/"); i >= 0 {
			editor["url"] = entry[:i] + manifest.Editor.Entry
			parts := strings.Split(manifest.Editor.Entry, "/")
			if len(parts) == 4 {
				editor["bundle_digest"] = parts[2]
			}
		}
	}
	writeJSON(w, 200, map[string]any{"app_id": manifest.ID, "manifest": manifest, "state": state, "editor": editor})
}
func (m *mooringHTTP) command(w http.ResponseWriter, r *http.Request) {
	var req mooring.Request
	if decodeGridCraftBody(w, r, &req) != nil {
		mooringError(w, mooring.ErrInvalid)
		return
	}
	tx, e := m.begin(r)
	if e != nil {
		mooringError(w, e)
		return
	}
	defer tx.Rollback()
	k, actor, e := m.human(r, tx, true)
	if e != nil {
		mooringError(w, e)
		return
	}
	m.commit(w, r, tx, k, actor, req, func(context.Context, *sql.Tx) (string, error) {
		currentKey, current, e := m.human(r, tx, true)
		if e == nil && currentKey != k {
			e = appengine.ErrForbidden
		}
		return current, e
	})
}
func (m *mooringHTTP) commit(w http.ResponseWriter, r *http.Request, tx *sql.Tx, k appengine.Key, actor string, req mooring.Request, authorize mooring.Authorize) {
	manifest, before, e := m.document(r.Context(), tx, k)
	if e != nil {
		mooringError(w, e)
		return
	}
	receipt, e := m.storage.Apply(r.Context(), mooring.Key{Transaction: tx, PageID: k.PageID, Manifest: manifest}, req, authorize)
	var notify func()
	actorType := journal.ActorUser
	actorID := strings.TrimPrefix(actor, "user:")
	if strings.HasPrefix(actor, "agent:") {
		actorType = journal.ActorAgent
		actorID = strings.Split(strings.TrimPrefix(actor, "agent:"), ":")[0]
	}
	if e == nil && receipt.Rev > before.Rev {
		notify, e = appendAuthorizationAudit(r.Context(), tx, m.workbook.pages.journal, journal.Entry{WorkspaceID: k.WorkspaceID, Type: mooringCommitted, ActorType: actorType, ActorID: actorID, Summary: "Mooring document committed", Payload: map[string]any{"page_id": k.PageID, "authority_id": actor, "mutation_id": req.MutationID, "rev": receipt.Rev, "payload_hash": receipt.PayloadHash}})
	}
	if e == nil {
		var current string
		current, e = authorize(r.Context(), tx)
		if e == nil && current != actor {
			e = appengine.ErrForbidden
		}
	}
	if e == nil {
		e = mooring.VerifyCommitted(r.Context(), tx, k.PageID, actor, manifest, req, receipt, before)
	}
	if e == nil {
		e = tx.Commit()
	}
	if e != nil {
		mooringError(w, e)
		return
	}
	if notify != nil {
		notify()
	}
	if m.workbook.broadcast != nil && receipt.Rev > before.Rev {
		m.workbook.broadcast("page", k.PageID, "mooring.committed", map[string]any{"artifact_id": k.PageID, "rev": receipt.Rev, "generation": req.Generation})
	}
	writeJSON(w, 200, receipt)
}

func (m *mooringHTTP) audit(r *http.Request, tx *sql.Tx, ws, page string, eventType journal.EntryType, user string, extra map[string]any) (func(), error) {
	extra["page_id"] = page
	return appendAuthorizationAudit(r.Context(), tx, m.workbook.pages.journal, journal.Entry{WorkspaceID: ws, Type: eventType, ActorType: journal.ActorUser, ActorID: user, Summary: "Mooring document " + strings.TrimPrefix(string(eventType), "artifact.mooring_"), Payload: extra})
}

func (m *mooringHTTP) createAuthority(r *http.Request, tx *sql.Tx, ws, user string) error {
	if e := gridcraftCredential(r, tx, true); e != nil {
		return e
	}
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	role, e := m.workbook.pages.liveContentHumanRole(ctx, ws, user)
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
