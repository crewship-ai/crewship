package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// Qualified profiles are host-owned. Requests cannot select executable paths,
// validator callbacks, compatibility pins, or publication principals.
type craftNativeApplication struct {
	Store                mooring.NativeStoreConfig
	EditorURL            string
	EngineBuild          string
	ChatSheetPreviewOnly bool
}
type craftNativeDocumentsHTTP struct {
	human *craftNativeHTTP
	// mu guards applications and profiles: an application installed after
	// startup (#5315) is added while requests are served.
	mu                       sync.RWMutex
	applications             map[string]craftNativeApplication
	profiles                 map[string]CraftNativeDocumentProfile
	retainedWord             map[craftNativeDocumentPin]qualifiedNativeDocumentProfile
	requireWordCompatibility bool
	// imports holds the xlsx conversions in flight (craft_native_import.go).
	imports *craftNativeImports
	// computed records a document the server's engine computed (CraftNativeHTTPConfig.SheetComputed).
	computed func(CraftNativeDocumentProfile, []byte)
	// trusted vouches for a Sheet the server computed (CraftNativeHTTPConfig.SheetTrusted).
	trusted func(CraftNativeDocumentProfile, []byte) bool
}

func newCraftNativeDocumentsHTTP(h *craftNativeHTTP, applications map[string]craftNativeApplication) *craftNativeDocumentsHTTP {
	profiles := make(map[string]craftNativeApplication, len(applications))
	for app, p := range applications {
		profiles[app] = p
	}
	return &craftNativeDocumentsHTTP{human: h, applications: profiles, profiles: map[string]CraftNativeDocumentProfile{}, retainedWord: map[craftNativeDocumentPin]qualifiedNativeDocumentProfile{}}
}

func (d *craftNativeDocumentsHTTP) application(app string) (craftNativeApplication, bool) {
	if d == nil {
		return craftNativeApplication{}, false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	a, ok := d.applications[app]
	return a, ok
}

func (d *craftNativeDocumentsHTTP) profile(app string) (CraftNativeDocumentProfile, bool) {
	if d == nil {
		return CraftNativeDocumentProfile{}, false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	p, ok := d.profiles[app]
	return p, ok
}

// creationProfile chooses the current default for a new document. During an
// upgrade with the new package still to install, a single retained package
// remains the Word creation default until activation succeeds.
func (d *craftNativeDocumentsHTTP) creationProfile(app string) (craftNativeApplication, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if current, ok := d.applications[app]; ok {
		return current, true
	}
	if app == "wordcraft" && len(d.retainedWord) == 1 {
		for _, retained := range d.retainedWord {
			return retained.application, true
		}
	}
	return craftNativeApplication{}, false
}

// resolveDocument selects a trusted runtime for an already authorized document.
// Word pins are exact, including the editor bundle. Sheets retain their workbook
// format and migration rules in NativeStore.
func (d *craftNativeDocumentsHTTP) resolveDocument(app string, pin craftNativeDocumentPin) (qualifiedNativeDocumentProfile, bool) {
	if d == nil {
		return qualifiedNativeDocumentProfile{}, false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	application, ok := d.applications[app]
	if app == "wordcraft" && (!ok || application.documentPin() != pin) {
		retained, found := d.retainedWord[pin]
		return retained, found
	}
	if !ok || application.Store.Application != app {
		return qualifiedNativeDocumentProfile{}, false
	}
	return qualifiedNativeDocumentProfile{application: application, profile: d.profiles[app]}, true
}

// A Word upgrade cannot start creating documents on the new default while
// any existing Word profile is unavailable. This runs under the same writer
// fence as creation, including when a restore added documents since startup.
func (d *craftNativeDocumentsHTTP) checkWordCreation(ctx context.Context, tx *sql.Tx, app string) error {
	if app != "wordcraft" || !d.requireWordCompatibility {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT engine_revision,engine_sha256,editor_bundle
		FROM craft_native_documents WHERE application_id='wordcraft'`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var pin craftNativeDocumentPin
		if err := rows.Scan(&pin.revision, &pin.sha256, &pin.bundle); err != nil {
			return err
		}
		if _, ok := d.resolveDocument(app, pin); !ok {
			return errWordDocumentProfileUnavailable
		}
	}
	return rows.Err()
}

// activate qualifies one profile exactly like a startup profile and serves
// it from now on. An application that already runs is not replaced: its
// documents are pinned to the engine they were written with.
func (d *craftNativeDocumentsHTTP) activate(p CraftNativeDocumentProfile) error {
	if d == nil || d.human == nil || !d.human.enabled {
		return errors.New("native Craft documents are not available on this server")
	}
	applications, profiles, err := qualifyCraftNativeProfiles(map[string]CraftNativeDocumentProfile{p.Application: p}, d.trusted)
	if err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, running := d.applications[p.Application]; running {
		return errCraftAppRunning
	}
	d.applications[p.Application] = applications[p.Application]
	d.profiles[p.Application] = profiles[p.Application]
	return nil
}

var errCraftAppRunning = errors.New("the application already runs; a new version needs a restart")

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
	qualified, ok := d.resolveDocument(entry.application, entry.pin)
	if !ok {
		return nil, nil, nil, zero, missingNativeDocumentProfile(entry.application)
	}
	profile := qualified.application
	cfg := profile.Store
	cfg.Enabled = d.human.enabled
	cfg.Leases = d.human.leases
	cfg.Authorize = d.human.authorizeNative
	return bound, entry, mooring.NewNativeStore(d.human.pages.db, cfg), profile, nil
}

var errWordDocumentProfileUnavailable = errors.New("the qualified WordCraft package for this document is unavailable")

func missingNativeDocumentProfile(app string) error {
	if app == "wordcraft" {
		return errWordDocumentProfileUnavailable
	}
	return mooring.ErrNativeDisabled
}

func craftNativeDocumentError(w http.ResponseWriter, err error) {
	if artifactStorageRefusal(w, err) {
		return
	}
	if craftWorkbookError(w, err) {
		return
	}
	switch {
	case errors.Is(err, errWordDocumentProfileUnavailable):
		replyCode(w, http.StatusServiceUnavailable, "word_profile_unavailable", err.Error())
	case errors.Is(err, mooring.ErrNativeDisabled):
		replyError(w, 503, "native documents unavailable")
	case errors.Is(err, artifacts.ErrVersionQuota):
		replyError(w, 413, "native history quota reached; existing versions retained")
	case errors.Is(err, mooring.ErrNativeAttemptAbsent):
		replyError(w, 410, "native save attempt did not commit and never will; save the current draft again")
	case errors.Is(err, mooring.ErrEditLeaseBusy), errors.Is(err, mooring.ErrEditLeaseStale), errors.Is(err, artifacts.ErrVersionConflict), errors.Is(err, mooring.ErrNativeUnconfirmed), errors.Is(err, mooring.ErrNativeAttemptRetired), errors.Is(err, mooring.ErrNativeRetirementLimit):
		replyCode(w, 409, "conflict", "native document or lease changed; keep the local draft")
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
	if errors.Is(err, mooring.ErrNativeProfile) && s.Workbook() {
		source, sourceErr := s.LegacyWorkbook(r.Context(), entry.scope, false)
		if sourceErr == nil {
			if source.WorkbookVersion == 0 {
				w.Header().Set("Cache-Control", "no-store")
				writeJSON(w, 409, map[string]string{"code": "legacy_workbook", "error": "This older Sheet needs to be converted to Excel."})
				return
			}
			state, err = s.Read(r.Context(), entry.scope)
		}
	}
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	if !d.auditRead(w, r, entry) {
		return
	}
	file, err := s.DocumentFile(state.Manifest)
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, craftNativeDocumentResponse{Version: strconv.FormatInt(state.LatestVersion, 10), Bytes: file.Content, Format: craftWorkbookResponseFormat(file), LegacySource: s.HasWorkbookMigration(r.Context(), entry.scope), ApplicationID: entry.application, EnginePin: s.EnginePin(), EngineBuild: profile.EngineBuild, EditorURL: profile.EditorURL, EditorBundle: profile.Store.EditorBundle, Quota: craftNativeDocumentQuotaResponse{UsedBytes: strconv.FormatInt(state.Quota.UsedBytes, 10), LimitBytes: strconv.FormatInt(state.Quota.LimitBytes, 10), Warning: state.Quota.Warning, Level: state.Quota.Level, Retention: state.Quota.Retention}})
}

// craftSheetRecord writes the audit row of a new revision on its saving
// transaction: artifact.sheet_saved for a Sheet, artifact.document_saved for a
// Document.
func craftSheetRecord(scope mooring.EditLeaseScope, application, userID, source string) mooring.NativeRecord {
	return func(ctx context.Context, tx *sql.Tx, r artifacts.VersionReceipt, native []byte) error {
		return recordCraftSheetSaved(ctx, tx, craftSheetSaved{WorkspaceID: scope.WorkspaceID, PageID: scope.ArtifactID, Version: r.Version, Source: source, UserID: userID, Application: application, Native: native})
	}
}

// craftProposalRecord is craftSheetRecord for a person accepting an agent's proposal.
func craftProposalRecord(scope mooring.EditLeaseScope, application, userID string) func(mooring.NativeProposal) mooring.NativeRecord {
	return func(p mooring.NativeProposal) mooring.NativeRecord {
		return func(ctx context.Context, tx *sql.Tx, r artifacts.VersionReceipt, native []byte) error {
			return recordCraftSheetSaved(ctx, tx, craftSheetSaved{WorkspaceID: scope.WorkspaceID, PageID: scope.ArtifactID, Version: r.Version, Source: craftSheetSavedByAcceptedProposal, UserID: userID,
				AgentID: strings.TrimPrefix(p.AuthorActorID, "agent:"), RequestID: p.RequestID, Application: application, Native: native})
		}
	}
}

func (d *craftNativeDocumentsHTTP) mutateSnapshot(w http.ResponseWriter, r *http.Request, operation string) {
	d.mutateSnapshotFrom(w, r, operation, craftSheetSavedByEditor)
}

// mutateSnapshotFrom is mutateSnapshot whose new revision is audited as source.
func (d *craftNativeDocumentsHTTP) mutateSnapshotFrom(w http.ResponseWriter, r *http.Request, operation, source string) {
	r, entry, s, _, err := d.bind(r)
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	if !d.human.apps.allows(r.Context(), entry.application, craftSnapshotOperation(operation)) {
		d.human.apps.refuse(w, r.Context(), entry.application)
		return
	}
	bodyReader, reserve, bodyErr := craftNativeSnapshotBodyReader(w, r, 45<<20)
	if bodyErr != nil {
		replyError(w, 400, "invalid native snapshot request")
		return
	}
	if operation == "save" {
		bytes := reserve
		if entry.chatID != "" && source == craftSheetSavedByEditor {
			// A merge may keep more bytes from the head than were uploaded.
			bytes = max(bytes, 32<<20)
			r = r.WithContext(context.WithValue(r.Context(), craftRealtimeWriteKey{}, true))
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
	decoder := json.NewDecoder(bodyReader)
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
	if len(body.Bytes) > 32<<20 {
		replyCode(w, 413, "too_large", "document file exceeds 32 MiB")
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
	if entry.chatID != "" && (operation == "reconcile" || operation == "save" && source == craftSheetSavedByEditor) {
		d.mutateRealtimeWorkbook(w, r, entry, s, lease, base, body, operation)
		return
	}
	switch operation {
	case "save":
		if s.Workbook() {
			d.saveWorkbook(w, r, entry, s, lease, base, body, source)
			return
		}
		receipt, e := s.SaveAttemptRecorded(r.Context(), lease, base, body.EditorRevision, body.ContentHash, body.Bytes, body.AttemptID, craftSheetRecord(entry.scope, entry.application, entry.source.UserID, source))
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
		receipt, e := s.RestoreRecorded(r.Context(), lease, base, target, craftSheetRecord(entry.scope, entry.application, entry.source.UserID, craftSheetSavedByRestore))
		if errors.Is(e, artifacts.ErrVersionNotFound) {
			// Write and lease authority ran first, so only the holder learns this.
			replyError(w, 404, "native version not found")
			return
		}
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
	d.mutateSnapshotFrom(w, r, "save", craftSheetSavedByImport)
}
func (d *craftNativeDocumentsHTTP) reconcileSnapshot(w http.ResponseWriter, r *http.Request) {
	d.mutateSnapshot(w, r, "reconcile")
}
func (d *craftNativeDocumentsHTTP) restoreSnapshot(w http.ResponseWriter, r *http.Request) {
	d.mutateSnapshot(w, r, "restore")
}

func craftWorkbookResponseFormat(file mooring.NativeFile) string {
	if file.Format == "native" {
		return ""
	}
	return file.Format
}
