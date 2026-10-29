package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/featureflags"
)

func (h *PageHandler) artifactDataEnabled(ctx context.Context) bool {
	if !h.artifactProjectEnabled(ctx) {
		return false
	}
	if os.Getenv("CREWSHIP_ARTIFACT_DASHBOARD_DATA_ENABLED") == "true" {
		return true
	}
	enabled, err := featureflags.IsEnabledWith(ctx, h.pageAuthReader(ctx), WorkspaceIDFromContext(ctx), "artifact_dashboard_data")
	return err == nil && enabled
}

type artifactDataSelection struct {
	owner       bool
	editor      bool
	collections []string
}

func (h *PageHandler) artifactDataAuthority(r *http.Request, k artifacts.VersionKey, collection string, version int64, write bool) (artifacts.VersionAuthorize, *artifactDataSelection) {
	authority := h.artifactProjectAuthority(r, k, write, false)
	if write {
		authority = h.artifactProjectOwnerMutationAuthority(r, k, version)
	}
	selected := &artifactDataSelection{}
	checked := false
	return func(ctx context.Context, tx *sql.Tx) (string, error) {
		ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
		if !h.artifactDataEnabled(ctx) {
			return "", artifacts.ErrDisabled
		}
		actor, err := authority(ctx, tx)
		if err != nil {
			return "", err
		}
		var owner string
		var latest, shared int64
		if err := tx.QueryRowContext(ctx, `SELECT p.owner_user_id,h.latest_version,h.shared_version FROM pages p JOIN artifact_version_heads h ON h.workspace_id=p.workspace_id AND h.page_id=p.id WHERE p.workspace_id=? AND p.id=?`, k.WorkspaceID, k.PageID).Scan(&owner, &latest, &shared); err != nil {
			return "", err
		}
		user := UserFromContext(ctx)
		isOwner := user != nil && user.ID == owner
		if run, _ := ctx.Value(artifactRunKey{}).(*artifactRunAuthority); run != nil {
			isOwner = run.HumanUserID == owner
		}
		isEditor := false
		if user != nil && !isOwner && ctx.Value(artifactRunKey{}) == nil {
			rec, e := h.loadPageForRead(ctx, k.WorkspaceID, "@"+k.PageID)
			if e != nil {
				return "", e
			}
			viewer, e := h.loadViewer(ctx, k.WorkspaceID, user.ID)
			if e != nil {
				return "", e
			}
			isEditor = h.artifactProjectHumanEditor(ctx, k, rec, viewer)
		}
		if write && latest != version || !isOwner && ((!isEditor && shared != version) || (isEditor && latest != version)) {
			return "", artifacts.ErrVersionConflict
		}
		collections, err := artifacts.DashboardCollectionsTx(ctx, tx, k, version)
		if err != nil {
			return "", err
		}
		if collection != "" && !slices.Contains(collections, collection) {
			return "", artifacts.ErrVersionNotFound
		}
		if checked && (selected.owner != isOwner || selected.editor != isEditor || !slices.Equal(selected.collections, collections)) {
			return "", artifacts.ErrVersionAuthority
		}
		selected.owner = isOwner
		selected.editor = isEditor
		selected.collections = collections
		checked = true
		return actor, nil
	}, selected
}

func (h *PageHandler) artifactDataStart(w http.ResponseWriter, r *http.Request) (artifacts.VersionKey, bool) {
	artifactCommentHeaders(w)
	if !h.artifactDataEnabled(r.Context()) {
		replyError(w, 404, "dashboard data is disabled")
		return artifacts.VersionKey{}, false
	}
	return h.artifactProjectStart(w, r)
}
func artifactDataError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, artifacts.ErrDataConflict):
		writeJSON(w, 409, map[string]string{"error": "A document changed. Read its version before retrying; nothing was overwritten.", "code": "artifact_data_conflict"})
	case errors.Is(err, artifacts.ErrDataQuota):
		writeJSON(w, 413, map[string]string{"error": "Dataset limit exceeded. Existing data was retained.", "code": "artifact_data_quota"})
	case errors.Is(err, artifacts.ErrDataInvalid):
		replyError(w, 400, "invalid bounded dashboard data request")
	default:
		artifactProjectError(w, err)
	}
}

type artifactDataDocumentResponse struct {
	ID          string          `json:"id"`
	Version     int64           `json:"version"`
	Value       json.RawMessage `json:"value"`
	UpdatedAt   string          `json:"updated_at"`
	SourceLabel string          `json:"source_label"`
	SourceKind  string          `json:"source_kind"`
}
type artifactDataCollectionsResponse struct {
	CodeVersion int64                      `json:"code_version"`
	Collections []artifacts.DataCollection `json:"collections"`
}

type artifactDataResponse struct {
	Collection  string                         `json:"collection"`
	CodeVersion int64                          `json:"code_version"`
	Documents   []artifactDataDocumentResponse `json:"documents"`
}

// This projection is identical for owners and shared readers. Raw provenance
// includes private account, agent and run identifiers and never enters the wire.
func projectArtifactData(r *http.Request, collection string, version int64, documents []artifacts.DataDocument) artifactDataResponse {
	out := artifactDataResponse{Collection: collection, CodeVersion: version, Documents: []artifactDataDocumentResponse{}}
	user := UserFromContext(r.Context())
	for _, doc := range documents {
		label, kind := "Contributor", "human"
		if strings.HasPrefix(doc.UpdatedBy, "agent:") {
			label, kind = "Agent", "agent"
		} else if doc.UpdatedBy == "erased" || doc.UpdatedBy == "" {
			label, kind = "Former contributor", "unknown"
		} else if user != nil && (doc.UpdatedBy == user.ID || doc.UpdatedBy == "user:"+user.ID) {
			label = "You"
		}
		out.Documents = append(out.Documents, artifactDataDocumentResponse{doc.ID, doc.Version, doc.Value, doc.UpdatedAt, label, kind})
	}
	return out
}
func writeArtifactData(w http.ResponseWriter, value any) {
	body, err := artifacts.MarshalDataJSON(value)
	if err != nil {
		replyError(w, 500, "cannot encode dataset response")
		return
	}
	if len(body) > artifacts.MaxDataResponseBytes {
		artifactDataError(w, artifacts.ErrDataQuota)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(200)
	_, _ = w.Write(body)
}
func dataCodeVersion(w http.ResponseWriter, r *http.Request) (int64, bool) {
	version, err := strconv.ParseInt(r.URL.Query().Get("version"), 10, 64)
	if err != nil || version < 1 || version > artifacts.MaxDataVersion {
		artifactDataError(w, artifacts.ErrDataInvalid)
		return 0, false
	}
	return version, true
}
func (h *PageHandler) GetArtifactData(w http.ResponseWriter, r *http.Request) {
	k, ok := h.artifactDataStart(w, r)
	if !ok {
		return
	}
	if err := h.artifactProjectCheck(r, k, false); err != nil {
		artifactDataError(w, err)
		return
	}
	version, ok := dataCodeVersion(w, r)
	if !ok {
		return
	}
	collection := r.PathValue("collection")
	auth, _ := h.artifactDataAuthority(r, k, collection, version, false)
	documents, err := artifacts.NewDataStore(h.db, artifacts.DataOptions{Enabled: true}).Read(r.Context(), k, collection, auth)
	if err != nil {
		artifactDataError(w, err)
		return
	}
	writeArtifactData(w, projectArtifactData(r, collection, version, documents))
}
func (h *PageHandler) ListArtifactData(w http.ResponseWriter, r *http.Request) {
	k, ok := h.artifactDataStart(w, r)
	if !ok {
		return
	}
	if err := h.artifactProjectCheck(r, k, false); err != nil {
		artifactDataError(w, err)
		return
	}
	version, ok := dataCodeVersion(w, r)
	if !ok {
		return
	}
	auth, selection := h.artifactDataAuthority(r, k, "", version, false)
	collections, err := artifacts.NewDataStore(h.db, artifacts.DataOptions{Enabled: true}).Collections(r.Context(), k, auth)
	if err != nil {
		artifactDataError(w, err)
		return
	}
	visible := []artifacts.DataCollection{}
	for _, collection := range collections {
		if slices.Contains(selection.collections, collection.Name) {
			visible = append(visible, collection)
		}
	}
	writeArtifactData(w, artifactDataCollectionsResponse{CodeVersion: version, Collections: visible})
}
func (h *PageHandler) BatchArtifactData(w http.ResponseWriter, r *http.Request) {
	k, ok := h.artifactDataStart(w, r)
	if !ok {
		return
	}
	if err := h.artifactProjectCheck(r, k, true); err != nil {
		artifactDataError(w, err)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, artifacts.MaxDataBatchBytes))
	if err != nil {
		artifactDataError(w, artifacts.ErrDataQuota)
		return
	}
	body, err := artifacts.DecodeDataBatch(raw)
	if err != nil {
		artifactDataError(w, err)
		return
	}
	collection := r.PathValue("collection")
	auth, _ := h.artifactDataAuthority(r, k, collection, body.BaseVersion, true)
	documents, err := artifacts.NewDataStore(h.db, artifacts.DataOptions{Enabled: true}).Batch(r.Context(), k, collection, body.Documents, auth)
	if err != nil {
		artifactDataError(w, err)
		return
	}
	writeArtifactData(w, projectArtifactData(r, collection, body.BaseVersion, documents))
}
