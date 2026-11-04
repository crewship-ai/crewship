package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// Qualified profiles are host-owned. Requests cannot select executable paths,
// validator callbacks, compatibility pins, or publication principals.
type craftNativeApplication struct {
	Store     mooring.NativeStoreConfig
	EditorURL string
}
type craftNativeDocumentsHTTP struct {
	human        *craftNativeHTTP
	applications map[string]craftNativeApplication
}

func newCraftNativeDocumentsHTTP(h *craftNativeHTTP, applications map[string]craftNativeApplication) *craftNativeDocumentsHTTP {
	profiles := make(map[string]craftNativeApplication, len(applications))
	for app, p := range applications {
		profiles[app] = p
	}
	return &craftNativeDocumentsHTTP{h, profiles}
}

type craftNativeSnapshotBody struct {
	WorkspaceID    string `json:"workspaceId"`
	PageID         string `json:"pageId"`
	ApplicationID  string `json:"applicationId"`
	EnginePin      string `json:"enginePin"`
	SessionID      string `json:"sessionId"`
	Generation     string `json:"generation"`
	BaseVersion    string `json:"baseVersion"`
	EditorRevision string `json:"editorRevision"`
	ContentHash    string `json:"contentHash"`
	Bytes          []byte `json:"bytes"`
	TargetVersion  string `json:"targetVersion"`
	// AttemptID is an optional client nonce for one save attempt; reconcile
	// with it can retire an attempt that did not commit (#4899).
	AttemptID string `json:"attemptId"`
}

func craftNativeVersion(raw string) (int64, error) {
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 1 || strconv.FormatInt(v, 10) != raw {
		return 0, mooring.ErrInvalid
	}
	return v, nil
}
func (d *craftNativeDocumentsHTTP) bind(r *http.Request) (*http.Request, *craftNativeSession, *mooring.NativeStore, craftNativeApplication, error) {
	var zero craftNativeApplication
	if d == nil || d.human == nil || !d.human.enabled {
		return nil, nil, nil, zero, mooring.ErrNativeDisabled
	}
	bound, err := d.human.boundNativeRequest(r)
	if err != nil {
		return nil, nil, nil, zero, err
	}
	bound = bound.WithContext(context.WithValue(bound.Context(), craftNativeReadCaptureKey{}, &craftNativeReadCapture{}))
	entry, _ := bound.Context().Value(craftNativeContextKey{}).(*craftNativeSession)
	profile, ok := d.applications[entry.application]
	if !ok || profile.Store.Application != entry.application {
		return nil, nil, nil, zero, mooring.ErrNativeDisabled
	}
	cfg := profile.Store
	cfg.Enabled = d.human.enabled
	cfg.Leases = d.human.leases
	cfg.Authorize = d.human.authorizeNative
	return bound, entry, mooring.NewNativeStore(d.human.pages.db, cfg), profile, nil
}
func craftNativeDocumentError(w http.ResponseWriter, err error) {
	if artifactStorageRefusal(w, err) {
		return
	}
	switch {
	case errors.Is(err, mooring.ErrNativeDisabled):
		replyError(w, 503, "native documents unavailable")
	case errors.Is(err, artifacts.ErrVersionQuota):
		replyError(w, 413, "native history quota reached; existing versions retained")
	case errors.Is(err, mooring.ErrNativeAttemptAbsent):
		replyError(w, 410, "native save attempt did not commit and never will; save the current draft again")
	case errors.Is(err, mooring.ErrEditLeaseBusy), errors.Is(err, mooring.ErrEditLeaseStale), errors.Is(err, artifacts.ErrVersionConflict), errors.Is(err, mooring.ErrNativeUnconfirmed), errors.Is(err, mooring.ErrNativeAttemptRetired), errors.Is(err, mooring.ErrNativeRetirementLimit):
		replyError(w, 409, "native document or lease changed; keep the local draft")
	case errors.Is(err, mooring.ErrNativeValidation), errors.Is(err, mooring.ErrInvalid), errors.Is(err, artifacts.ErrVersionInvalid):
		replyError(w, 422, "invalid native document")
	default:
		craftNativeHTTPError(w, err)
	}
}

// craftNativeReadCapture holds the Page record that the human read authority
// (authorizeNative, intent "read") authorized inside the engine transaction
// that read the content. bind installs one per request.
type craftNativeReadCaptureKey struct{}
type craftNativeReadCapture struct{ record *pageRecord }

// auditRead applies PRD §5's administrator read audit at the response
// boundary of a native Craft content read, outside the engine transaction:
// another custodian's private document, or an ordinary one reached only
// through the workspace OWNER/ADMIN role. The decision uses the custodian
// captured in the read's own transaction, never a later lookup: a custody
// transfer to the reader after the read must not skip the audit. A failed
// audit, or a read without a captured record, releases nothing.
func (d *craftNativeDocumentsHTTP) auditRead(w http.ResponseWriter, r *http.Request, entry *craftNativeSession) bool {
	capture, _ := r.Context().Value(craftNativeReadCaptureKey{}).(*craftNativeReadCapture)
	if capture == nil || capture.record == nil || capture.record.ID != entry.scope.ArtifactID {
		craftNativeDocumentError(w, errCraftNativeAccess)
		return false
	}
	privateReadAuditSeam(entry.scope.ArtifactID)
	return d.human.pages.auditPrivateOwnerAccess(w, r, capture.record)
}

func (d *craftNativeDocumentsHTTP) readSnapshot(w http.ResponseWriter, r *http.Request) {
	r, entry, s, profile, err := d.bind(r)
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	state, err := s.Read(r.Context(), entry.scope)
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	if !d.auditRead(w, r, entry) {
		return
	}
	file, ok := state.Manifest.Files["document.json"]
	if !ok || len(state.Manifest.Files) != 1 || state.Manifest.EntryPoint != "document.json" || file.MediaType != "application/json" {
		craftNativeDocumentError(w, mooring.ErrNativeProfile)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, craftNativeDocumentResponse{Version: strconv.FormatInt(state.LatestVersion, 10), Bytes: file.Content, ApplicationID: entry.application, EnginePin: s.EnginePin(), EditorURL: profile.EditorURL, EditorBundle: profile.Store.EditorBundle, Quota: craftNativeDocumentQuotaResponse{UsedBytes: strconv.FormatInt(state.Quota.UsedBytes, 10), LimitBytes: strconv.FormatInt(state.Quota.LimitBytes, 10), Warning: state.Quota.Warning, Level: state.Quota.Level, Retention: state.Quota.Retention}})
}
func (d *craftNativeDocumentsHTTP) mutateSnapshot(w http.ResponseWriter, r *http.Request, operation string) {
	r, entry, s, _, err := d.bind(r)
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	if operation == "save" {
		bytes := int64(45 << 20)
		if r.ContentLength >= 0 && r.ContentLength < bytes {
			bytes = r.ContentLength
		}
		bytes += 256 << 10
		upload, e := s.ReserveUpload(r.Context(), entry.scope, bytes)
		if e != nil {
			craftNativeDocumentError(w, e)
			return
		}
		defer upload.Close()
		ctx, cancel := context.WithDeadline(upload.Context(r.Context()), upload.ExpiresAt())
		defer cancel()
		controller := http.NewResponseController(w)
		_ = controller.SetReadDeadline(upload.ExpiresAt())
		defer controller.SetReadDeadline(time.Time{})
		body := r.Body
		stop := context.AfterFunc(ctx, func() {
			if body != nil {
				_ = body.Close()
			}
		})
		defer stop()
		r = r.WithContext(ctx)
	}
	var body craftNativeSnapshotBody
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 45<<20))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&body); err != nil {
		replyError(w, 400, "invalid native snapshot request")
		return
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		replyError(w, 400, "invalid native snapshot request")
		return
	}
	generation, err := craftNativeGeneration(body.Generation)
	base, baseErr := craftNativeVersion(body.BaseVersion)
	if err != nil || baseErr != nil || body.WorkspaceID != entry.scope.WorkspaceID || body.PageID != entry.scope.ArtifactID || body.ApplicationID != entry.application || body.SessionID != entry.id || body.EnginePin != s.EnginePin() {
		replyError(w, 400, "native snapshot binding mismatch")
		return
	}
	lease := mooring.EditLease{Scope: entry.scope, Holder: craftNativePrincipal(entry), Generation: generation}
	w.Header().Set("Cache-Control", "no-store")
	switch operation {
	case "save":
		receipt, e := s.SaveAttempt(r.Context(), lease, base, body.EditorRevision, body.ContentHash, body.Bytes, body.AttemptID)
		if e != nil {
			craftNativeDocumentError(w, e)
			return
		}
		writeJSON(w, 200, receipt)
	case "reconcile":
		receipt, e := s.ReconcileAttempt(r.Context(), lease, base, body.EditorRevision, body.ContentHash, body.AttemptID)
		if e != nil {
			craftNativeDocumentError(w, e)
			return
		}
		writeJSON(w, 200, receipt)
	case "restore":
		target, e := craftNativeVersion(body.TargetVersion)
		if e != nil {
			replyError(w, 400, "invalid target version")
			return
		}
		receipt, e := s.Restore(r.Context(), lease, base, target)
		if e != nil {
			craftNativeDocumentError(w, e)
			return
		}
		writeJSON(w, 200, craftNativeRestoreResponse{Version: strconv.FormatInt(receipt.Version, 10), Durability: "node", IssuedBy: "crewship", Phase: "committed"})
	}
}
func (d *craftNativeDocumentsHTTP) saveSnapshot(w http.ResponseWriter, r *http.Request) {
	d.mutateSnapshot(w, r, "save")
}
func (d *craftNativeDocumentsHTTP) importSnapshot(w http.ResponseWriter, r *http.Request) {
	d.mutateSnapshot(w, r, "save")
}
func (d *craftNativeDocumentsHTTP) reconcileSnapshot(w http.ResponseWriter, r *http.Request) {
	d.mutateSnapshot(w, r, "reconcile")
}
func (d *craftNativeDocumentsHTTP) restoreSnapshot(w http.ResponseWriter, r *http.Request) {
	d.mutateSnapshot(w, r, "restore")
}
