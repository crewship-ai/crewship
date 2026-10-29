package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/mooring"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

const journalCraftNativePreviewShared journal.EntryType = "craft.native_preview_shared"

var errCraftNativeShareAudit = errors.New("native preview audit unavailable")

type craftNativePreviewResponse struct {
	Version       string `json:"version"`
	Bytes         []byte `json:"bytes"`
	ApplicationID string `json:"applicationId"`
	EnginePin     string `json:"enginePin"`
	EditorBundle  string `json:"editorBundle"`
	EditorURL     string `json:"editorURL"`
}

func (d *craftNativeDocumentsHTTP) preview(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if d == nil || d.human == nil || !d.human.enabled {
		craftNativeDocumentError(w, mooring.ErrNativeDisabled)
		return
	}
	h := d.human
	source, err := craftNativeHuman(r.Context())
	if err != nil {
		craftNativeHTTPError(w, err)
		return
	}
	scope := mooring.EditLeaseScope{WorkspaceID: WorkspaceIDFromContext(r.Context())}
	tx, err := h.pages.db.BeginTx(r.Context(), nil)
	if err != nil {
		craftNativeHTTPError(w, err)
		return
	}
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	rec, err := h.pages.loadPageForRead(ctx, scope.WorkspaceID, r.PathValue("slug"))
	if err != nil {
		_ = tx.Rollback()
		craftNativeHTTPError(w, err)
		return
	}
	scope.ArtifactID = rec.ID
	proof, err := h.pageAuthorityForView(ctx, tx, source, scope, false, true)
	_ = tx.Rollback()
	if err != nil {
		craftNativeHTTPError(w, err)
		return
	}
	profile, ok := d.applications[proof.application]
	if !ok || profile.Store.Application != proof.application {
		craftNativeDocumentError(w, mooring.ErrNativeDisabled)
		return
	}
	cfg := profile.Store
	cfg.Enabled = true
	cfg.Leases = h.leases
	cfg.Authorize = func(ctx context.Context, tx *sql.Tx, k mooring.EditLeaseScope, intent string) (mooring.EditLeasePrincipal, error) {
		if k != scope || intent != "preview" {
			return mooring.EditLeasePrincipal{}, errCraftNativeAccess
		}
		current, err := h.pageAuthorityForView(ctx, tx, source, k, false, true)
		if err != nil {
			return mooring.EditLeasePrincipal{}, err
		}
		if current.application != proof.application || current.workspaceEpoch != proof.workspaceEpoch || current.memberID != proof.memberID || current.memberRevision != proof.memberRevision {
			return mooring.EditLeasePrincipal{}, errCraftNativeAccess
		}
		return mooring.EditLeasePrincipal{ActorID: "user:" + source.UserID}, nil
	}
	store := mooring.NewNativeStore(h.pages.db, cfg)
	state, err := store.ReadShared(r.Context(), scope)
	if errors.Is(err, artifacts.ErrVersionNotFound) {
		replyError(w, 404, "shared native preview unavailable")
		return
	}
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	entryPoint, mediaType, supported := mooring.NativeDocumentFormat(proof.application)
	file, ok := state.Manifest.Files[entryPoint]
	if !supported || !ok || len(state.Manifest.Files) != 1 || state.Manifest.EntryPoint != entryPoint || file.MediaType != mediaType {
		craftNativeDocumentError(w, mooring.ErrNativeProfile)
		return
	}
	writeJSON(w, 200, craftNativePreviewResponse{Version: strconv.FormatInt(state.Manifest.Version, 10), Bytes: file.Content, ApplicationID: proof.application, EnginePin: store.EnginePin(), EditorBundle: cfg.EditorBundle, EditorURL: profile.EditorURL})
}

type craftNativeShareBody struct {
	WorkspaceID   string `json:"workspaceId"`
	PageID        string `json:"pageId"`
	ApplicationID string `json:"applicationId"`
	EnginePin     string `json:"enginePin"`
	SessionID     string `json:"sessionId"`
	Generation    string `json:"generation"`
	BaseVersion   string `json:"baseVersion"`
	TargetVersion string `json:"targetVersion"`
}

func (d *craftNativeDocumentsHTTP) shareSnapshot(w http.ResponseWriter, r *http.Request) {
	r, entry, store, _, err := d.bind(r)
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	var body craftNativeShareBody
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	var trailing any
	if decoder.Decode(&body) != nil || decoder.Decode(&trailing) != io.EOF {
		replyError(w, 400, "invalid native share request")
		return
	}
	generation, err := craftNativeGeneration(body.Generation)
	base, baseErr := craftNativeVersion(body.BaseVersion)
	target, targetErr := craftNativeVersion(body.TargetVersion)
	if err != nil || baseErr != nil || targetErr != nil || body.WorkspaceID != entry.scope.WorkspaceID || body.PageID != entry.scope.ArtifactID || body.ApplicationID != entry.application || body.SessionID != entry.id || body.EnginePin != store.EnginePin() {
		replyError(w, 400, "native share binding mismatch")
		return
	}
	lease := mooring.EditLease{Scope: entry.scope, Holder: craftNativePrincipal(entry), Generation: generation}
	var notify func()
	err = store.Share(r.Context(), lease, base, target, func(ctx context.Context, tx *sql.Tx, actor string) error {
		if actor != craftNativePrincipal(entry).ActorID {
			return errCraftNativeAccess
		}
		user := entry.source.UserID
		if _, e := tx.ExecContext(ctx, `INSERT INTO audit_logs(id,workspace_id,user_id,action,entity_type,entity_id,metadata,created_at) VALUES(?,?,?,?,?,?,?,?)`, generateCUID(), entry.scope.WorkspaceID, user, string(journalCraftNativePreviewShared), "page", entry.scope.ArtifactID, fmt.Sprintf(`{"version":%d}`, target), tsformat.Format(d.human.pages.clock.Now())); e != nil {
			return fmt.Errorf("%w: %w", errCraftNativeShareAudit, e)
		}
		var e error
		notify, e = appendAuthorizationAudit(ctx, tx, d.human.pages.journal, journal.Entry{WorkspaceID: entry.scope.WorkspaceID, Type: journalCraftNativePreviewShared, ActorType: journal.ActorUser, ActorID: user, Summary: "Native Craft shared preview selected", Payload: map[string]any{"page_id": entry.scope.ArtifactID, "version": target, "application": entry.application}})
		if e != nil {
			return fmt.Errorf("%w: %w", errCraftNativeShareAudit, e)
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, errCraftNativeShareAudit) {
			replyError(w, 500, "native preview publication audit unavailable")
		} else {
			craftNativeDocumentError(w, err)
		}
		return
	}
	if notify != nil {
		notify()
	}
	writeJSON(w, 200, craftNativeRestoreResponse{Version: body.TargetVersion, Durability: "node", IssuedBy: "crewship", Phase: "shared"})
}
