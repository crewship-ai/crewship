package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/crewship-ai/crewship/internal/appengine"
	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/mooring"
)

type craftNativeInitializeResponse struct {
	Token         string `json:"session_token"`
	SessionID     string `json:"session_id"`
	ApplicationID string `json:"application_id"`
	PageID        string `json:"page_id"`
	Generation    string `json:"generation"`
	Version       string `json:"version"`
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
	m := d.human
	if m == nil || !m.enabled || m.clock == nil || m.pages == nil {
		craftNativeDocumentError(w, mooring.ErrNativeDisabled)
		return
	}
	source, err := craftNativeHuman(r.Context())
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	var body struct {
		ApplicationID string `json:"application_id"`
		Bytes         []byte `json:"bytes"`
	}
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
	profile, qualified := d.applications[body.ApplicationID]
	if !qualified {
		craftNativeDocumentError(w, mooring.ErrNativeDisabled)
		return
	}
	tx, err := m.pages.db.BeginTx(r.Context(), nil)
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	defer tx.Rollback()
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	rec, err := m.pages.loadPageForRead(ctx, WorkspaceIDFromContext(ctx), r.PathValue("slug"))
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	scope := mooring.EditLeaseScope{WorkspaceID: WorkspaceIDFromContext(ctx), ArtifactID: rec.ID}
	proof, err := m.initializeOwnerProof(ctx, tx, source, scope, true)
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	token, err := craftNativeOpaque()
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	id, err := craftNativeOpaque()
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	boot, now, err := m.clock.Now()
	if err != nil || boot == "" || now < 0 {
		craftNativeDocumentError(w, errCraftNativeAccess)
		return
	}
	cap := 24 * time.Hour
	if !proof.expiry.IsZero() && time.Until(proof.expiry) < cap {
		cap = time.Until(proof.expiry)
	}
	if cap <= 0 || now > time.Duration(math.MaxInt64)-cap {
		craftNativeDocumentError(w, errCraftNativeAccess)
		return
	}
	entry := &craftNativeSession{id: id, tokenHash: sha256.Sum256([]byte(token)), source: source, scope: scope, application: body.ApplicationID, workspaceEpoch: proof.workspaceEpoch, memberID: proof.memberID, memberRevision: proof.memberRevision, boot: boot, deadline: now + cap}
	if err = tx.Commit(); err != nil {
		craftNativeDocumentError(w, err)
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
	cfg.Authorize = func(ctx context.Context, tx *sql.Tx, k mooring.EditLeaseScope, intent string) (mooring.EditLeasePrincipal, error) {
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
		if p.workspaceEpoch != entry.workspaceEpoch || p.memberID != entry.memberID || p.memberRevision != entry.memberRevision {
			return mooring.EditLeasePrincipal{}, errCraftNativeAccess
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
	nativeCtx := context.WithValue(r.Context(), craftNativeContextKey{}, entry)
	lease, receipt, err := mooring.NewNativeStore(m.pages.db, cfg).Initialize(nativeCtx, scope, craftNativePrincipal(entry), body.Bytes)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = errCraftNativeAccess
		}
		craftNativeDocumentError(w, err)
		return
	}
	m.mu.Lock()
	m.sessionsByHash[entry.tokenHash] = entry
	published = true
	m.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, craftNativeInitializeResponse{Token: token, SessionID: id, ApplicationID: entry.application, PageID: scope.ArtifactID, Generation: strconv.FormatInt(lease.Generation, 10), Version: strconv.FormatInt(receipt.Version, 10)})
}
