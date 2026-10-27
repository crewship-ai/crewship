package artifacts

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
)

var (
	ErrVersionNotFound = errors.New("artifact version not found")
	ErrVersionConflict = errors.New("artifact base_version conflict; reload current version before publishing")
	ErrVersionQuota    = errors.New("artifact history quota exceeded; existing history retained")
	ErrVersionInvalid  = errors.New("invalid artifact manifest")
)

type VersionKey struct{ WorkspaceID, PageID string }

// VersionAuthorize must check current authority, and any lease/mandate, in the
// supplied transaction. The returned actor is the sole persisted audit identity.
type VersionAuthorize func(context.Context, *sql.Tx) (string, error)
type VersionOptions struct {
	Enabled                          bool
	MaxHistoryBytes, MaxVersionBytes int64
	MaxFiles                         int
	WarningPercent                   int
}
type VersionFile struct {
	Content   []byte `json:"content"`
	MediaType string `json:"media_type"`
}
type VersionRequest struct {
	BaseVersion int64                  `json:"base_version"`
	EntryPoint  string                 `json:"entry_point"`
	Files       map[string]VersionFile `json:"files"`
	Message     string                 `json:"message,omitempty"`
}
type VersionReceipt struct {
	Version int64  `json:"version"`
	Actor   string `json:"actor"`
}
type VersionManifest struct {
	Version      int64                  `json:"version"`
	EntryPoint   string                 `json:"entry_point"`
	Files        map[string]VersionFile `json:"files"`
	Message      string                 `json:"message,omitempty"`
	Actor        string                 `json:"actor"`
	CreatedAt    string                 `json:"created_at"`
	RestoredFrom int64                  `json:"restored_from,omitempty"`
}
type VersionInfo struct {
	Version      int64  `json:"version"`
	EntryPoint   string `json:"entry_point"`
	Message      string `json:"message,omitempty"`
	Actor        string `json:"actor"`
	CreatedAt    string `json:"created_at"`
	RestoredFrom int64  `json:"restored_from,omitempty"`
}
type VersionQuota struct {
	UsedBytes  int64  `json:"used_bytes"`
	LimitBytes int64  `json:"limit_bytes"`
	Warning    bool   `json:"warning"`
	Retention  string `json:"retention"`
}
type VersionState struct {
	LatestVersion int64           `json:"latest_version"`
	SharedVersion int64           `json:"shared_version"`
	Manifest      VersionManifest `json:"manifest"`
	Quota         VersionQuota    `json:"quota"`
}
type VersionStore struct {
	db   *sql.DB
	opts VersionOptions
}

func NewVersionStore(db *sql.DB, opts VersionOptions) *VersionStore {
	if opts.MaxHistoryBytes <= 0 {
		opts.MaxHistoryBytes = 256 << 20
	}
	if opts.MaxVersionBytes <= 0 {
		opts.MaxVersionBytes = 32 << 20
	}
	if opts.MaxFiles <= 0 {
		opts.MaxFiles = 256
	}
	if opts.WarningPercent <= 0 || opts.WarningPercent > 100 {
		opts.WarningPercent = 80
	}
	return &VersionStore{db, opts}
}
func (s *VersionStore) transaction(ctx context.Context, k VersionKey, a VersionAuthorize, fn func(*sql.Tx, string) error) error {
	if s == nil || s.db == nil || !s.opts.Enabled {
		return ErrDisabled
	}
	if k.WorkspaceID == "" || k.PageID == "" || a == nil {
		return fmt.Errorf("%w: page, workspace and authority required", ErrVersionInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Establish the existing database writer fence before checking live authority.
	if _, err = tx.ExecContext(ctx, `UPDATE document_writer_fence SET seq=seq WHERE id=1`); err != nil {
		return err
	}
	actor, err := a(ctx, tx)
	if err != nil {
		return err
	}
	if actor == "" {
		return fmt.Errorf("%w: empty actor", ErrVersionInvalid)
	}
	var exists int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM pages WHERE id=? AND workspace_id=?`, k.PageID, k.WorkspaceID).Scan(&exists); err != nil {
		return err
	}
	if exists != 1 {
		return ErrVersionNotFound
	}
	if err = fn(tx, actor); err != nil {
		return err
	}
	return tx.Commit()
}
func versionHead(ctx context.Context, tx *sql.Tx, k VersionKey) (latest, shared int64, err error) {
	err = tx.QueryRowContext(ctx, `SELECT latest_version,shared_version FROM artifact_version_heads WHERE workspace_id=? AND page_id=?`, k.WorkspaceID, k.PageID).Scan(&latest, &shared)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return
}
func (s *VersionStore) quota(ctx context.Context, tx *sql.Tx, k VersionKey) (q VersionQuota, err error) {
	q.LimitBytes = s.opts.MaxHistoryBytes
	q.Retention = "keep-all"
	err = tx.QueryRowContext(ctx, `SELECT coalesce((SELECT sum(length(content)) FROM artifact_version_blobs WHERE workspace_id=? AND page_id=?),0)+coalesce((SELECT sum(length(CAST(manifest_json AS BLOB))+length(CAST(message AS BLOB))+length(CAST(actor AS BLOB))+length(created_at)+128) FROM artifact_versions WHERE workspace_id=? AND page_id=?),0)`, k.WorkspaceID, k.PageID, k.WorkspaceID, k.PageID).Scan(&q.UsedBytes)
	q.Warning = q.UsedBytes*100 >= q.LimitBytes*int64(s.opts.WarningPercent)
	return
}
func (s *VersionStore) Read(ctx context.Context, k VersionKey, a VersionAuthorize) (out VersionState, err error) {
	err = s.transaction(ctx, k, a, func(tx *sql.Tx, _ string) error {
		var e error
		out.LatestVersion, out.SharedVersion, e = versionHead(ctx, tx, k)
		if e != nil {
			return e
		}
		if out.LatestVersion == 0 {
			return ErrVersionNotFound
		}
		out.Manifest, e = readVersion(ctx, tx, k, out.LatestVersion)
		if e != nil {
			return e
		}
		out.Quota, e = s.quota(ctx, tx, k)
		return e
	})
	if err != nil {
		return VersionState{}, err
	}
	return
}
func (s *VersionStore) ReadVersion(ctx context.Context, k VersionKey, version int64, a VersionAuthorize) (out VersionManifest, err error) {
	err = s.transaction(ctx, k, a, func(tx *sql.Tx, _ string) error { var e error; out, e = readVersion(ctx, tx, k, version); return e })
	if err != nil {
		return VersionManifest{}, err
	}
	return
}

type storedFile struct {
	Hash      string `json:"hash"`
	MediaType string `json:"media_type"`
}
type storedManifest struct {
	EntryPoint string                `json:"entry_point"`
	Files      map[string]storedFile `json:"files"`
}

func readVersion(ctx context.Context, tx *sql.Tx, k VersionKey, version int64) (out VersionManifest, err error) {
	var raw []byte
	out.Version = version
	err = tx.QueryRowContext(ctx, `SELECT manifest_json,message,actor,created_at,restored_from FROM artifact_versions WHERE workspace_id=? AND page_id=? AND version=?`, k.WorkspaceID, k.PageID, version).Scan(&raw, &out.Message, &out.Actor, &out.CreatedAt, &out.RestoredFrom)
	if errors.Is(err, sql.ErrNoRows) {
		return VersionManifest{}, ErrVersionNotFound
	}
	if err != nil {
		return VersionManifest{}, err
	}
	var m storedManifest
	if err = json.Unmarshal(raw, &m); err != nil {
		return VersionManifest{}, err
	}
	out.EntryPoint = m.EntryPoint
	out.Files = make(map[string]VersionFile, len(m.Files))
	for name, f := range m.Files {
		var content []byte
		if err = tx.QueryRowContext(ctx, `SELECT content FROM artifact_version_blobs WHERE workspace_id=? AND page_id=? AND hash=?`, k.WorkspaceID, k.PageID, f.Hash).Scan(&content); err != nil {
			return VersionManifest{}, err
		}
		out.Files[name] = VersionFile{content, f.MediaType}
	}
	return
}
func (s *VersionStore) Apply(ctx context.Context, k VersionKey, r VersionRequest, a VersionAuthorize) (out VersionReceipt, err error) {
	err = s.transaction(ctx, k, a, func(tx *sql.Tx, actor string) error {
		var e error
		out, e = s.publish(ctx, tx, k, r, actor, 0)
		return e
	})
	if err != nil {
		return VersionReceipt{}, err
	}
	return
}
func (s *VersionStore) publish(ctx context.Context, tx *sql.Tx, k VersionKey, r VersionRequest, actor string, restored int64) (VersionReceipt, error) {
	latest, _, err := versionHead(ctx, tx, k)
	if err != nil {
		return VersionReceipt{}, err
	}
	if r.BaseVersion != latest {
		return VersionReceipt{}, ErrVersionConflict
	}
	if len(r.Files) == 0 || len(r.Files) > s.opts.MaxFiles || len(r.Message) > 4096 {
		return VersionReceipt{}, ErrVersionInvalid
	}
	if _, ok := r.Files[r.EntryPoint]; !ok {
		return VersionReceipt{}, fmt.Errorf("%w: entry point missing", ErrVersionInvalid)
	}
	m := storedManifest{r.EntryPoint, make(map[string]storedFile, len(r.Files))}
	blobs := make(map[string][]byte)
	var total int64
	for name, file := range r.Files {
		if name == "" || len(name) > 512 || path.IsAbs(name) || path.Clean(name) != name || name == "." || strings.HasPrefix(name, "../") || strings.ContainsAny(name, "\\\x00?#") || len(file.MediaType) > 255 || strings.ContainsAny(file.MediaType, "\r\n") {
			return VersionReceipt{}, fmt.Errorf("%w: unsafe file path or media type", ErrVersionInvalid)
		}
		total += int64(len(file.Content))
		if total > s.opts.MaxVersionBytes {
			return VersionReceipt{}, ErrVersionQuota
		}
		sum := sha256.Sum256(file.Content)
		hash := hex.EncodeToString(sum[:])
		m.Files[name] = storedFile{hash, file.MediaType}
		blobs[hash] = file.Content
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return VersionReceipt{}, err
	}
	q, err := s.quota(ctx, tx, k)
	if err != nil {
		return VersionReceipt{}, err
	}
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)
	additional := int64(len(raw) + len(r.Message) + len(actor) + len(createdAt) + 128)
	for hash, content := range blobs {
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM artifact_version_blobs WHERE workspace_id=? AND page_id=? AND hash=?`, k.WorkspaceID, k.PageID, hash).Scan(&count); err != nil {
			return VersionReceipt{}, err
		}
		if count == 0 {
			additional += int64(len(content))
		}
	}
	if q.UsedBytes+additional > s.opts.MaxHistoryBytes {
		return VersionReceipt{}, ErrVersionQuota
	}
	for hash, content := range blobs {
		if content == nil {
			content = []byte{}
		}
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO artifact_version_blobs(workspace_id,page_id,hash,content) VALUES(?,?,?,?)`, k.WorkspaceID, k.PageID, hash, content); err != nil {
			return VersionReceipt{}, err
		}
	}
	version := latest + 1
	if _, err = tx.ExecContext(ctx, `INSERT INTO artifact_versions(workspace_id,page_id,version,manifest_json,message,actor,created_at,restored_from) VALUES(?,?,?,?,?,?,?,?)`, k.WorkspaceID, k.PageID, version, string(raw), r.Message, actor, createdAt, restored); err != nil {
		return VersionReceipt{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO artifact_version_heads(workspace_id,page_id,latest_version,shared_version) VALUES(?,?,?,0) ON CONFLICT(workspace_id,page_id) DO UPDATE SET latest_version=excluded.latest_version`, k.WorkspaceID, k.PageID, version)
	return VersionReceipt{version, actor}, err
}
func (s *VersionStore) Restore(ctx context.Context, k VersionKey, base, target int64, a VersionAuthorize) (out VersionReceipt, err error) {
	err = s.transaction(ctx, k, a, func(tx *sql.Tx, actor string) error {
		m, e := readVersion(ctx, tx, k, target)
		if e != nil {
			return e
		}
		out, e = s.publish(ctx, tx, k, VersionRequest{base, m.EntryPoint, m.Files, fmt.Sprintf("Restore version %d", target)}, actor, target)
		return e
	})
	if err != nil {
		return VersionReceipt{}, err
	}
	return
}
func (s *VersionStore) Share(ctx context.Context, k VersionKey, base, target int64, a VersionAuthorize) error {
	return s.transaction(ctx, k, a, func(tx *sql.Tx, _ string) error {
		latest, _, err := versionHead(ctx, tx, k)
		if err != nil {
			return err
		}
		if base != latest {
			return ErrVersionConflict
		}
		var found int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM artifact_versions WHERE workspace_id=? AND page_id=? AND version=?`, k.WorkspaceID, k.PageID, target).Scan(&found); err != nil {
			return err
		}
		if found != 1 {
			return ErrVersionNotFound
		}
		_, err = tx.ExecContext(ctx, `UPDATE artifact_version_heads SET shared_version=? WHERE workspace_id=? AND page_id=?`, target, k.WorkspaceID, k.PageID)
		return err
	})
}
func listVersions(ctx context.Context, tx *sql.Tx, k VersionKey) (out []VersionInfo, err error) {
	out = []VersionInfo{}
	rows, e := tx.QueryContext(ctx, `SELECT version,json_extract(manifest_json,'$.entry_point'),message,actor,created_at,restored_from FROM artifact_versions WHERE workspace_id=? AND page_id=? ORDER BY version DESC`, k.WorkspaceID, k.PageID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var v VersionInfo
		if e = rows.Scan(&v.Version, &v.EntryPoint, &v.Message, &v.Actor, &v.CreatedAt, &v.RestoredFrom); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *VersionStore) Versions(ctx context.Context, k VersionKey, a VersionAuthorize) (out []VersionInfo, err error) {
	err = s.transaction(ctx, k, a, func(tx *sql.Tx, _ string) error { var e error; out, e = listVersions(ctx, tx, k); return e })
	if err != nil {
		return nil, err
	}
	return
}

var _ RevisionStorage[VersionKey, VersionRequest, VersionState, VersionReceipt, VersionAuthorize] = (*VersionStore)(nil)

// Quota reports history capacity under live read authority, without materializing
// current file contents. History is retained when further publishing is refused.
func (s *VersionStore) Quota(ctx context.Context, k VersionKey, a VersionAuthorize) (out VersionQuota, err error) {
	err = s.transaction(ctx, k, a, func(tx *sql.Tx, _ string) error { var e error; out, e = s.quota(ctx, tx, k); return e })
	if err != nil {
		return VersionQuota{}, err
	}
	return
}

// VersionSelection is chosen only by host authority, never directly by an
// untrusted reader. SharedOnly additionally enforces the live shared pin.
type VersionSelection struct {
	Version                    int64
	SharedOnly, IncludeHistory bool
}
type VersionViewAuthorize func(context.Context, *sql.Tx) (string, VersionSelection, error)
type VersionView struct {
	State    VersionState
	Versions []VersionInfo
}

// View releases one consistent authorized snapshot: manifest, shared/latest
// heads, quota and optional history all come from the same writer-fenced tx.
func (s *VersionStore) View(ctx context.Context, k VersionKey, a VersionViewAuthorize) (out VersionView, err error) {
	if a == nil {
		return VersionView{}, fmt.Errorf("%w: view authority required", ErrVersionInvalid)
	}
	var selection VersionSelection
	authorize := func(ctx context.Context, tx *sql.Tx) (string, error) {
		actor, chosen, e := a(ctx, tx)
		selection = chosen
		return actor, e
	}
	err = s.transaction(ctx, k, authorize, func(tx *sql.Tx, _ string) error {
		var e error
		out.State.LatestVersion, out.State.SharedVersion, e = versionHead(ctx, tx, k)
		if e != nil {
			return e
		}
		if out.State.LatestVersion == 0 {
			return ErrVersionNotFound
		}
		selected := selection.Version
		if selected < 0 {
			return ErrVersionInvalid
		}
		if selection.SharedOnly {
			if out.State.SharedVersion == 0 || selected != 0 && selected != out.State.SharedVersion {
				return ErrVersionNotFound
			}
			selected = out.State.SharedVersion
			out.State.LatestVersion = selected
			out.State.Quota = VersionQuota{Retention: "shared-version-only"}
		} else {
			if selected == 0 {
				selected = out.State.LatestVersion
			}
			out.State.Quota, e = s.quota(ctx, tx, k)
			if e != nil {
				return e
			}
		}
		out.State.Manifest, e = readVersion(ctx, tx, k, selected)
		if e != nil {
			return e
		}
		out.Versions = []VersionInfo{{Version: out.State.Manifest.Version, EntryPoint: out.State.Manifest.EntryPoint, Message: out.State.Manifest.Message, Actor: out.State.Manifest.Actor, CreatedAt: out.State.Manifest.CreatedAt, RestoredFrom: out.State.Manifest.RestoredFrom}}
		if selection.IncludeHistory && !selection.SharedOnly {
			out.Versions, e = listVersions(ctx, tx, k)
		}
		return e
	})
	if err != nil {
		return VersionView{}, err
	}
	return
}
