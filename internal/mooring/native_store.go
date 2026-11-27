package mooring

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

var ErrNativeDisabled = errors.New("native Craft documents disabled")
var ErrNativeProfile = errors.New("native Craft profile mismatch")

// NativeAuthorize is a trusted host callback. It checks current Page rights and
// original credential/mandate epochs in the supplied transaction, then returns
// its bound connection principal. Inputs never establish principal authority.
type NativeAuthorize func(context.Context, *sql.Tx, EditLeaseScope, string) (EditLeasePrincipal, error)
type NativeStoreConfig struct {
	Enabled                                                 bool
	Application, EngineRevision, EngineSHA256, EditorBundle string
	// Format is "" for a native JSON document, or NativeFormatWorkbook: each version is the
	// xlsx/xlsm file itself (native_workbook.go). Validate then checks the archive.
	Format string
	// CompatEditorBundles are earlier editor bundles of THIS engine (same revision
	// and module digest) whose documents this editor opens unchanged: the
	// document format and the engine did not change, only the editor did. A
	// stored document keeps the pin it was written with; it is never rewritten.
	// New documents are written with the first compatible pin, so a server that
	// still runs the earlier editor opens them too.
	CompatEditorBundles []string
	Leases              *EditLeaseStore
	Authorize           NativeAuthorize
	// Validate must invoke the exact pinned application engine, outside the SQL
	// writer. It must reject lossy import/repair warnings, not silently normalize.
	Validate                 func(context.Context, []byte) error
	MaxHistoryBytes          int64
	MaxProposalBytes         int64 // trusted per-Page retained proposal budget, separate from human history
	MaxWorkspaceHistoryBytes int64 // aggregate workspace ceiling shared by every artifact family
}

// NativeStore wraps the existing manifest VersionStore. It owns no parallel
// revision ledger: a version is the entire application's native document.
type NativeStore struct {
	db       *sql.DB
	config   NativeStoreConfig
	versions *artifacts.VersionStore
	// afterProposalBase is a test seam: it runs between choosing an acceptance
	// base and publishing, where a concurrent save can land.
	afterProposalBase func()
}

// DefaultNativeHistoryBytes is a visible byte budget, never a version-count cap.
// Exhaustion preserves every version; the caller must offer recovery or a larger budget.
const DefaultNativeHistoryBytes int64 = 16 << 30

// DefaultNativeProposalBytes caps retained candidates independently of human
// history. Every state counts; rejection never replenishes this allowance.
const DefaultNativeProposalBytes int64 = 64 << 20

func NewNativeStore(db *sql.DB, c NativeStoreConfig) *NativeStore {
	if c.MaxProposalBytes <= 0 {
		c.MaxProposalBytes = DefaultNativeProposalBytes
	}
	if c.MaxHistoryBytes <= 0 {
		c.MaxHistoryBytes = DefaultNativeHistoryBytes
	}
	return &NativeStore{db: db, config: c, versions: artifacts.NewVersionStore(db, artifacts.VersionOptions{Enabled: c.Enabled, ContentType: c.Application, MaxHistoryBytes: c.MaxHistoryBytes, MaxVersionBytes: maxVersionBytes(c), MaxWorkspaceHistoryBytes: c.MaxWorkspaceHistoryBytes})}
}
func nativeHex(value string, n int) bool {
	if len(value) != n {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func (s *NativeStore) ready() error {
	if s == nil || !s.config.Enabled {
		return ErrNativeDisabled
	}
	c := s.config
	if s.db == nil || c.Leases == nil || c.Authorize == nil || c.Validate == nil || c.Application != "wordcraft" && c.Application != "gridcraft" || !nativeHex(c.EngineRevision, 40) || !nativeHex(c.EngineSHA256, 64) || !nativeHex(c.EditorBundle, 64) || !compatBundlesValid(c) || c.Format != "" && (c.Format != NativeFormatWorkbook || c.Application != "gridcraft") {
		return ErrInvalid
	}
	return nil
}
func nativeVersionKey(k EditLeaseScope) artifacts.VersionKey {
	return artifacts.VersionKey{WorkspaceID: k.WorkspaceID, PageID: k.ArtifactID}
}
func (s *NativeStore) profile(ctx context.Context, tx *sql.Tx, k EditLeaseScope) error {
	var app, revision, hash, bundle string
	var migrated, workbook bool
	if err := tx.QueryRowContext(ctx, `SELECT d.application_id,d.engine_revision,d.engine_sha256,d.editor_bundle,
			m.page_id IS NOT NULL,
			coalesce(h.content_type='gridcraft' AND (
				(json_extract(v.manifest_json,'$.entry_point')='workbook.xlsx' AND json_extract(v.manifest_json,'$.files."workbook.xlsx".media_type')=?) OR
				(json_extract(v.manifest_json,'$.entry_point')='workbook.xlsm' AND json_extract(v.manifest_json,'$.files."workbook.xlsm".media_type')=?)
			),0)
		FROM craft_native_documents d
		LEFT JOIN craft_workbook_migrations m ON m.page_id=d.page_id AND m.workspace_id=d.workspace_id
		LEFT JOIN artifact_version_heads h ON h.page_id=d.page_id AND h.workspace_id=d.workspace_id
		LEFT JOIN artifact_versions v ON v.page_id=h.page_id AND v.workspace_id=h.workspace_id AND v.version=h.latest_version
		WHERE d.page_id=? AND d.workspace_id=?`, WorkbookMediaTypeXLSX, WorkbookMediaTypeXLSM, k.ArtifactID, k.WorkspaceID).Scan(&app, &revision, &hash, &bundle, &migrated, &workbook); err != nil {
		return err
	}
	if app != s.config.Application {
		return ErrNativeProfile
	}
	// The qualified GridCraft package opens the persisted workbook format.
	// The conversion receipt still records the exact package that did the migration.
	if s.workbookStore() {
		if !workbook {
			return ErrNativeProfile
		}
	} else if migrated || workbook || revision != s.config.EngineRevision || hash != s.config.EngineSHA256 || !s.editorBundleAccepted(bundle) {
		return ErrNativeProfile
	}
	return nil
}

// editorBundleAccepted: the current editor, or an earlier one declared compatible.
func (s *NativeStore) editorBundleAccepted(bundle string) bool {
	if bundle == s.config.EditorBundle {
		return true
	}
	for _, compatible := range s.config.CompatEditorBundles {
		if bundle == compatible {
			return true
		}
	}
	return false
}

// newDocumentEditorPin is the editor pin written into a new document.
func (s *NativeStore) newDocumentEditorPin() string {
	if len(s.config.CompatEditorBundles) > 0 {
		return s.config.CompatEditorBundles[0]
	}
	return s.config.EditorBundle
}
func (s *NativeStore) authority(k EditLeaseScope, l *EditLease, intent string) artifacts.VersionAuthorize {
	return func(ctx context.Context, tx *sql.Tx) (string, error) {
		p, err := s.config.Authorize(ctx, tx, k, intent)
		if err != nil {
			return "", err
		}
		if err = s.profile(ctx, tx, k); err != nil {
			return "", err
		}
		if l != nil {
			if l.Scope != k || l.Holder != p {
				return "", ErrEditLeaseDenied
			}
			if err = s.config.Leases.CheckWrite(ctx, tx, *l); err != nil {
				return "", err
			}
		}
		return p.ActorID, nil
	}
}

// holderAuthority checks current Page rights and that the caller is the
// lease's original holder, without requiring that lease to be live. Only
// read-only proofs about that holder's own past commits may use it.
func (s *NativeStore) holderAuthority(l EditLease, intent string) artifacts.VersionAuthorize {
	return func(ctx context.Context, tx *sql.Tx) (string, error) {
		p, err := s.config.Authorize(ctx, tx, l.Scope, intent)
		if err != nil {
			return "", err
		}
		if err = s.profile(ctx, tx, l.Scope); err != nil {
			return "", err
		}
		if l.Holder != p {
			return "", ErrEditLeaseDenied
		}
		return p.ActorID, nil
	}
}
func (s *NativeStore) candidate(ctx context.Context, raw []byte) ([]byte, error) {
	if !s.workbookStore() && (len(raw) == 0 || len(raw) > 32<<20) {
		return nil, ErrInvalid
	}
	if s.workbookStore() {
		if _, err := DetectWorkbook(raw); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
		}
	} else {
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
			return nil, ErrInvalid
		}
	}
	b := append([]byte(nil), raw...)
	if err := s.config.Validate(ctx, append([]byte(nil), b...)); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNativeValidation, err)
	}
	return b, nil
}

// NativeRecord writes what goes with a new native version on its saving
// transaction (an audit row): r is the version, native the document it holds.
// Its error rolls the version back.
type NativeRecord func(ctx context.Context, tx *sql.Tx, r artifacts.VersionReceipt, native []byte) error

func (record NativeRecord) of(native []byte) func(context.Context, *sql.Tx, artifacts.VersionReceipt) error {
	if record == nil {
		return nil
	}
	return func(ctx context.Context, tx *sql.Tx, r artifacts.VersionReceipt) error {
		return record(ctx, tx, r, native)
	}
}

func nativeManifest(base int64, b []byte) artifacts.VersionRequest {
	return artifacts.VersionRequest{BaseVersion: base, EntryPoint: "document.json", Files: map[string]artifacts.VersionFile{"document.json": {Content: b, MediaType: "application/json"}}}
}

// Initialize only attaches a NEW explicitly selected profile to an authorized
// empty personal Page. The host's initialize intent checks competing custody.
// Profile, first lease and first manifest commit or roll back together.
func (s *NativeStore) Initialize(ctx context.Context, k EditLeaseScope, p EditLeasePrincipal, raw []byte) (EditLease, artifacts.VersionReceipt, error) {
	return s.InitializeRecorded(ctx, k, p, raw, nil)
}

// InitializeRecorded is Initialize with a record of the first version, on the
// transaction that attaches the profile and writes that version.
func (s *NativeStore) InitializeRecorded(ctx context.Context, k EditLeaseScope, p EditLeasePrincipal, raw []byte, record NativeRecord) (EditLease, artifacts.VersionReceipt, error) {
	if err := s.ready(); err != nil {
		return EditLease{}, artifacts.VersionReceipt{}, err
	}
	preflight := func(ctx context.Context, tx *sql.Tx) (string, error) {
		bound, err := s.config.Authorize(ctx, tx, k, "initialize")
		if err != nil {
			return "", err
		}
		if bound != p {
			return "", ErrEditLeaseDenied
		}
		return bound.ActorID, nil
	}
	if _, err := s.versions.Quota(ctx, nativeVersionKey(k), preflight); err != nil {
		return EditLease{}, artifacts.VersionReceipt{}, err
	}
	b, err := s.candidate(ctx, raw)
	if err != nil {
		return EditLease{}, artifacts.VersionReceipt{}, err
	}
	var l EditLease
	initialized := false
	a := func(ctx context.Context, tx *sql.Tx) (string, error) {
		if initialized {
			return s.authority(k, &l, "write")(ctx, tx)
		}
		actor, err := preflight(ctx, tx)
		if err != nil {
			return "", err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO craft_native_documents(page_id,workspace_id,application_id,engine_revision,engine_sha256,editor_bundle) VALUES(?,?,?,?,?,?)`, k.ArtifactID, k.WorkspaceID, s.config.Application, s.config.EngineRevision, s.config.EngineSHA256, s.newDocumentEditorPin())
		if err != nil {
			return "", err
		}
		l, err = s.config.Leases.AcquireInTransaction(ctx, tx, k, p)
		if err != nil {
			return "", err
		}
		initialized = true
		return actor, nil
	}
	request := s.manifest(0, b)
	request.Record = record.of(b)
	r, err := s.versions.Apply(ctx, nativeVersionKey(k), request, a)
	if err != nil {
		return EditLease{}, artifacts.VersionReceipt{}, err
	}
	return l, r, nil
}
func (s *NativeStore) Save(ctx context.Context, l EditLease, base int64, raw []byte) (artifacts.VersionReceipt, error) {
	if err := s.ready(); err != nil {
		return artifacts.VersionReceipt{}, err
	}
	a := s.authority(l.Scope, &l, "write")
	if _, err := s.versions.Quota(ctx, nativeVersionKey(l.Scope), a); err != nil {
		return artifacts.VersionReceipt{}, err
	}
	b, err := s.candidate(ctx, raw)
	if err != nil {
		return artifacts.VersionReceipt{}, err
	}
	return s.versions.Apply(ctx, nativeVersionKey(l.Scope), s.manifest(base, b), a)
}
func (s *NativeStore) Read(ctx context.Context, k EditLeaseScope) (artifacts.VersionState, error) {
	if err := s.ready(); err != nil {
		return artifacts.VersionState{}, err
	}
	return s.versions.Read(ctx, nativeVersionKey(k), s.authority(k, nil, "read"))
}

// Versions lists at most limit saved native versions, newest first, under
// exactly the host's "read" authority and profile check of Read. The
// bound is applied in SQL. It is metadata only: no document bytes, and never a
// write or lease side effect.
func (s *NativeStore) Versions(ctx context.Context, k EditLeaseScope, limit int) ([]artifacts.VersionInfo, bool, error) {
	if err := s.ready(); err != nil {
		return nil, false, err
	}
	return s.versions.RecentVersions(ctx, nativeVersionKey(k), limit, s.authority(k, nil, "read"))
}

// ReadVersion returns the bytes of one saved native version and the current
// head, under exactly the host's "read" authority and profile check of Read. It is read-only: no lease, no write, no change to the shared pin.
func (s *NativeStore) ReadVersion(ctx context.Context, k EditLeaseScope, version int64) (artifacts.VersionManifest, int64, error) {
	if err := s.ready(); err != nil {
		return artifacts.VersionManifest{}, 0, err
	}
	return s.versions.ReadVersionWithHead(ctx, nativeVersionKey(k), version, s.authority(k, nil, "read"))
}
func (s *NativeStore) Restore(ctx context.Context, l EditLease, base, target int64) (artifacts.VersionReceipt, error) {
	return s.RestoreRecorded(ctx, l, base, target, nil)
}

// RestoreRecorded is Restore with a record of the appended version (native is
// the restored document).
func (s *NativeStore) RestoreRecorded(ctx context.Context, l EditLease, base, target int64, record NativeRecord) (artifacts.VersionReceipt, error) {
	if err := s.ready(); err != nil {
		return artifacts.VersionReceipt{}, err
	}
	a := s.authority(l.Scope, &l, "write")
	m, err := s.versions.ReadVersion(ctx, nativeVersionKey(l.Scope), target, a)
	if err != nil {
		return artifacts.VersionReceipt{}, err
	}
	file, err := s.DocumentFile(m)
	if err != nil {
		return artifacts.VersionReceipt{}, err
	}
	if _, err = s.candidate(ctx, file.Content); err != nil {
		return artifacts.VersionReceipt{}, err
	}
	return s.versions.RestoreRecorded(ctx, nativeVersionKey(l.Scope), base, target, a, record.of(file.Content))
}

// ReadShared returns only the current shared pin. The host's preview authority
// and the immutable native profile are checked in that same fenced transaction.
// It never acquires a lease, exposes private history or falls back to latest.
func (s *NativeStore) ReadShared(ctx context.Context, k EditLeaseScope) (artifacts.VersionState, error) {
	if err := s.ready(); err != nil {
		return artifacts.VersionState{}, err
	}
	authorize := s.authority(k, nil, "preview")
	view, err := s.versions.View(ctx, nativeVersionKey(k), func(ctx context.Context, tx *sql.Tx) (string, artifacts.VersionSelection, error) {
		actor, err := authorize(ctx, tx)
		return actor, artifacts.VersionSelection{SharedOnly: true}, err
	})
	return view.State, err
}

// Share publishes an existing validated native version under the same current
// write authority, generation fence and base-version CAS as native writes.
func (s *NativeStore) Share(ctx context.Context, l EditLease, base, target int64, publish func(context.Context, *sql.Tx, string) error) error {
	if err := s.ready(); err != nil {
		return err
	}
	if base < 1 || target < 1 || publish == nil {
		return ErrInvalid
	}
	authorize := s.authority(l.Scope, &l, "write")
	published := false
	return s.versions.Share(ctx, nativeVersionKey(l.Scope), base, target, func(ctx context.Context, tx *sql.Tx) (string, error) {
		actor, err := authorize(ctx, tx)
		if err != nil {
			return "", err
		}
		if !published {
			if err = publish(ctx, tx, actor); err != nil {
				return "", err
			}
			published = true
		}
		return actor, nil
	})
}

// ReserveUpload is storage admission only; publication still checks the exact
// original credential, profile and current lease in its own transaction.
func (s *NativeStore) ReserveUpload(ctx context.Context, k EditLeaseScope, bytes int64) (*artifacts.StorageUpload, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	return s.versions.ReserveUpload(ctx, nativeVersionKey(k), bytes, s.authority(k, nil, "write"))
}

func compatBundlesValid(c NativeStoreConfig) bool {
	seen := map[string]bool{c.EditorBundle: true}
	for _, b := range c.CompatEditorBundles {
		if !nativeHex(b, 64) || seen[b] {
			return false
		}
		seen[b] = true
	}
	return true
}
