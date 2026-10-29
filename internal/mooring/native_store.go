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
	Leases                                                  *EditLeaseStore
	Authorize                                               NativeAuthorize
	// Validate must invoke the exact pinned application engine, outside the SQL
	// writer. It must reject lossy import/repair warnings, not silently normalize.
	Validate        func(context.Context, []byte) error
	MaxHistoryBytes int64
}

// NativeStore wraps the existing manifest VersionStore. It owns no parallel
// revision ledger: a version is the entire application's native document.
type NativeStore struct {
	db       *sql.DB
	config   NativeStoreConfig
	versions *artifacts.VersionStore
}

// DefaultNativeHistoryBytes is a visible byte budget, never a version-count cap.
// Exhaustion preserves every version; the caller must offer recovery or a larger budget.
const DefaultNativeHistoryBytes int64 = 16 << 30

func NewNativeStore(db *sql.DB, c NativeStoreConfig) *NativeStore {
	if c.MaxHistoryBytes <= 0 {
		c.MaxHistoryBytes = DefaultNativeHistoryBytes
	}
	return &NativeStore{db: db, config: c, versions: artifacts.NewVersionStore(db, artifacts.VersionOptions{Enabled: c.Enabled, ContentType: c.Application, MaxHistoryBytes: c.MaxHistoryBytes})}
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
	_, _, known := NativeDocumentFormat(c.Application)
	if s.db == nil || c.Leases == nil || c.Authorize == nil || c.Validate == nil || !known || !nativeHex(c.EngineRevision, 40) || !nativeHex(c.EngineSHA256, 64) || !nativeHex(c.EditorBundle, 64) {
		return ErrInvalid
	}
	return nil
}
func nativeVersionKey(k EditLeaseScope) artifacts.VersionKey {
	return artifacts.VersionKey{WorkspaceID: k.WorkspaceID, PageID: k.ArtifactID}
}
func (s *NativeStore) profile(ctx context.Context, tx *sql.Tx, k EditLeaseScope) error {
	var app, revision, hash, bundle string
	if err := tx.QueryRowContext(ctx, `SELECT application_id,engine_revision,engine_sha256,editor_bundle FROM craft_native_documents WHERE page_id=? AND workspace_id=?`, k.ArtifactID, k.WorkspaceID).Scan(&app, &revision, &hash, &bundle); err != nil {
		return err
	}
	if app != s.config.Application || revision != s.config.EngineRevision || hash != s.config.EngineSHA256 || bundle != s.config.EditorBundle {
		return ErrNativeProfile
	}
	return nil
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
func (s *NativeStore) candidate(ctx context.Context, raw []byte) ([]byte, error) {
	if len(raw) == 0 || len(raw) > 32<<20 {
		return nil, ErrInvalid
	}
	b := append([]byte(nil), raw...)
	trimmed := bytes.TrimSpace(b)
	if s.config.Application != "deckcraft" && (len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(b)) {
		return nil, ErrInvalid
	}
	if err := s.config.Validate(ctx, append([]byte(nil), b...)); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNativeValidation, err)
	}
	return b, nil
}
func (s *NativeStore) nativeManifest(base int64, b []byte) artifacts.VersionRequest {
	name, media, _ := NativeDocumentFormat(s.config.Application)
	return artifacts.VersionRequest{BaseVersion: base, EntryPoint: name, Files: map[string]artifacts.VersionFile{name: {Content: b, MediaType: media}}}
}

// Initialize only attaches a NEW explicitly selected profile to an authorized
// empty personal Page. The host's initialize intent checks competing custody.
// Profile, first lease and first manifest commit or roll back together.
func (s *NativeStore) Initialize(ctx context.Context, k EditLeaseScope, p EditLeasePrincipal, raw []byte) (EditLease, artifacts.VersionReceipt, error) {
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
		_, err = tx.ExecContext(ctx, `INSERT INTO craft_native_documents(page_id,workspace_id,application_id,engine_revision,engine_sha256,editor_bundle) VALUES(?,?,?,?,?,?)`, k.ArtifactID, k.WorkspaceID, s.config.Application, s.config.EngineRevision, s.config.EngineSHA256, s.config.EditorBundle)
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
	r, err := s.versions.Apply(ctx, nativeVersionKey(k), s.nativeManifest(0, b), a)
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
	return s.versions.Apply(ctx, nativeVersionKey(l.Scope), s.nativeManifest(base, b), a)
}
func (s *NativeStore) Read(ctx context.Context, k EditLeaseScope) (artifacts.VersionState, error) {
	if err := s.ready(); err != nil {
		return artifacts.VersionState{}, err
	}
	return s.versions.Read(ctx, nativeVersionKey(k), s.authority(k, nil, "read"))
}
func (s *NativeStore) Restore(ctx context.Context, l EditLease, base, target int64) (artifacts.VersionReceipt, error) {
	if err := s.ready(); err != nil {
		return artifacts.VersionReceipt{}, err
	}
	a := s.authority(l.Scope, &l, "write")
	m, err := s.versions.ReadVersion(ctx, nativeVersionKey(l.Scope), target, a)
	if err != nil {
		return artifacts.VersionReceipt{}, err
	}
	name, media, _ := NativeDocumentFormat(s.config.Application)
	if m.EntryPoint != name || len(m.Files) != 1 {
		return artifacts.VersionReceipt{}, ErrNativeProfile
	}
	file, ok := m.Files[name]
	if !ok || file.MediaType != media {
		return artifacts.VersionReceipt{}, ErrNativeProfile
	}
	if _, err = s.candidate(ctx, file.Content); err != nil {
		return artifacts.VersionReceipt{}, err
	}
	return s.versions.Restore(ctx, nativeVersionKey(l.Scope), base, target, a)
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
