package backup

// The environment store: content-addressed blobs shared by every bundle that
// carries a complete container environment, plus the reference counts that
// keep retention from deleting a blob another bundle still needs.
//
//	<backups dir>/environments/
//	  blobs/sha256/<hex>     one file per distinct blob (layer, config, manifest)
//	  index/<env id>.json    the environment record, for work without the bundle
//	  tmp/                   blobs being written
//
// Blobs are written to tmp/, hashed while written, and renamed into place, so
// a blob file under blobs/ always has the content its name says.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/crewship-ai/crewship/internal/memory"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// EnvironmentStoreDirName is the store's directory inside the backups dir.
const EnvironmentStoreDirName = "environments"

// pendingRefPrefix marks refs of a capture whose bundle is not finished.
const pendingRefPrefix = "pending:"

// stalePendingAfter: a pending ref this old belongs to a capture that died.
const stalePendingAfter = 24 * time.Hour

// orphanBlobAfter: a blob file with no row and no ref is deleted only when
// it is at least this old, so a capture between writing the file and
// recording it is never raced.
const orphanBlobAfter = time.Hour

// EnvironmentStore is one store directory.
type EnvironmentStore struct {
	Dir string
}

// EnvInlineEnv names the server setting that makes new bundles carry their
// environment layers inline (self-contained) instead of referencing the
// shared store: CREWSHIP_BACKUP_ENV_INLINE=1.
const EnvInlineEnv = "CREWSHIP_BACKUP_ENV_INLINE"

// EnvironmentInlineDefault reads EnvInlineEnv.
func EnvironmentInlineDefault() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvInlineEnv))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// EnvironmentStoreFor is the store beside a backups directory.
func EnvironmentStoreFor(backupsDir string) *EnvironmentStore {
	return &EnvironmentStore{Dir: filepath.Join(backupsDir, EnvironmentStoreDirName)}
}

// storeLocks serialises blob ingest against collection, per store dir.
var storeLocks sync.Map // dir -> *sync.Mutex

func (s *EnvironmentStore) lock() func() {
	v, _ := storeLocks.LoadOrStore(filepath.Clean(s.Dir), &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// ErrBadDigest: a digest string is not sha256:<64 hex>.
var ErrBadDigest = errors.New("backup: invalid blob digest")

// ErrBlobMissing: the store does not hold a blob.
var ErrBlobMissing = errors.New("backup: environment blob missing")

// digestHex validates "sha256:<hex>" and returns the hex part.
func digestHex(d string) (string, error) {
	h, ok := strings.CutPrefix(d, "sha256:")
	if !ok || len(h) != 64 {
		return "", fmt.Errorf("%w: %q", ErrBadDigest, d)
	}
	if _, err := hex.DecodeString(h); err != nil {
		return "", fmt.Errorf("%w: %q", ErrBadDigest, d)
	}
	return h, nil
}

// BlobPath is where a blob lives.
func (s *EnvironmentStore) BlobPath(digest string) (string, error) {
	h, err := digestHex(digest)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.Dir, "blobs", "sha256", h), nil
}

// Has reports whether the store holds digest.
func (s *EnvironmentStore) Has(digest string) bool {
	p, err := s.BlobPath(digest)
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

// Open opens a blob.
func (s *EnvironmentStore) Open(_ context.Context, digest string) (io.ReadCloser, int64, error) {
	p, err := s.BlobPath(digest)
	if err != nil {
		return nil, 0, err
	}
	f, err := os.Open(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, fmt.Errorf("%w: %s", ErrBlobMissing, digest)
	}
	if err != nil {
		return nil, 0, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, err
	}
	return f, st.Size(), nil
}

// Put writes r into the store and returns its digest. When want is set the
// content must hash to it. A blob already present is kept and r drained.
func (s *EnvironmentStore) Put(r io.Reader, want string) (digest string, size int64, err error) {
	tmpDir := filepath.Join(s.Dir, "tmp")
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return "", 0, err
	}
	f, err := os.CreateTemp(tmpDir, "blob-*")
	if err != nil {
		return "", 0, err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()
	h := sha256.New()
	size, err = io.Copy(io.MultiWriter(f, h), r)
	if err == nil {
		// Durable before it gets its final name: a blob that exists under
		// its digest is trusted and never rewritten (Put keeps a present
		// blob), so a rename that outran the data after a power cut would
		// leave a torn layer every later backup silently reuses.
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", 0, fmt.Errorf("backup: write environment blob: %w", err)
	}
	digest = "sha256:" + hex.EncodeToString(h.Sum(nil))
	if want != "" && want != digest {
		return "", 0, fmt.Errorf("%w: content hashes to %s, expected %s", ErrBadDigest, digest, want)
	}
	dst, err := s.BlobPath(digest)
	if err != nil {
		return "", 0, err
	}
	if _, statErr := os.Stat(dst); statErr == nil {
		_ = os.Remove(tmp)
		return digest, size, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return "", 0, err
	}
	if err := os.Chmod(tmp, 0o400); err != nil {
		return "", 0, err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return "", 0, err
	}
	if err := syncDir(filepath.Dir(dst)); err != nil {
		return "", 0, err
	}
	return digest, size, nil
}

// syncDir fsyncs a directory so a rename into it survives a power cut.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	serr := d.Sync()
	if cerr := d.Close(); serr == nil {
		serr = cerr
	}
	return serr
}

// WriteIndex stores the environment record under index/<id>.json.
func (s *EnvironmentStore) WriteIndex(env *Environment) error {
	b, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Join(s.Dir, "index")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return memory.WriteFileDurable(filepath.Join(dir, filepath.Base(env.ID)+".json"), b, 0o600)
}

// ReadIndex reads an environment record by id.
func (s *EnvironmentStore) ReadIndex(id string) (*Environment, error) {
	b, err := os.ReadFile(filepath.Join(s.Dir, "index", filepath.Base(id)+".json"))
	if err != nil {
		return nil, err
	}
	var env Environment
	if err := json.Unmarshal(b, &env); err != nil {
		return nil, err
	}
	return &env, nil
}

// ── Reference counts ───────────────────────────────────────────────────────

// newPendingRef is the ref a capture records until its bundle exists.
func newPendingRef(now time.Time) string {
	return pendingRefPrefix + newEnvironmentID(now)
}

// recordEnvironmentRefs records blobs and the ref that needs them, in one
// transaction. No-op without a database.
func recordEnvironmentRefs(ctx context.Context, db *sql.DB, ref string, sizes map[string]int64) error {
	if db == nil || len(sizes) == 0 {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for d, n := range sizes {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO environment_blobs (digest, size) VALUES (?, ?)`, d, n); err != nil {
			return fmt.Errorf("backup: record environment blob: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO bundle_environment_refs (bundle_ref, digest) VALUES (?, ?)`, ref, d); err != nil {
			return fmt.Errorf("backup: record environment ref: %w", err)
		}
	}
	return tx.Commit()
}

// AddEnvironmentRefs records that bundleRef needs digests (sizes read from
// the store when known). Used when a bundle's refs are rebuilt, e.g. after
// a download.
func AddEnvironmentRefs(ctx context.Context, db *sql.DB, store *EnvironmentStore, bundleRef string, digests []string) error {
	sizes := map[string]int64{}
	for _, d := range digests {
		var n int64
		if store != nil {
			if p, err := store.BlobPath(d); err == nil {
				if st, err := os.Stat(p); err == nil {
					n = st.Size()
				}
			}
		}
		sizes[d] = n
	}
	return recordEnvironmentRefs(ctx, db, bundleRef, sizes)
}

// commitEnvironmentRefs moves a capture's pending refs onto its finished
// bundle.
func commitEnvironmentRefs(ctx context.Context, db *sql.DB, pending, bundleRef string) error {
	if db == nil || pending == "" {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO bundle_environment_refs (bundle_ref, digest, created_at)
		SELECT ?, digest, created_at FROM bundle_environment_refs WHERE bundle_ref = ?`, bundleRef, pending); err != nil {
		return fmt.Errorf("backup: commit environment refs: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM bundle_environment_refs WHERE bundle_ref = ?`, pending); err != nil {
		return fmt.Errorf("backup: commit environment refs: %w", err)
	}
	return tx.Commit()
}

// ReleaseEnvironmentRefs drops every ref a bundle held — call it when the
// bundle file is deleted. The blobs themselves go at the next
// CollectEnvironmentGarbage, and only when no other bundle needs them.
func ReleaseEnvironmentRefs(ctx context.Context, db *sql.DB, bundleRef string) error {
	if db == nil || bundleRef == "" {
		return nil
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM bundle_environment_refs WHERE bundle_ref = ?`, bundleRef); err != nil {
		return fmt.Errorf("backup: release environment refs: %w", err)
	}
	return nil
}

// reconcileBundleRefs re-records the refs of every bundle file beside the
// store whose manifest lists environment blobs. The database is not the
// only witness: a recovered or re-created database, or a crash between a
// bundle's rename and its ref commit, leaves bundles on disk the ref table
// does not know, and a blob one of them names must never be collected. A
// manifest is read without the key, so this needs none.
func reconcileBundleRefs(ctx context.Context, db *sql.DB, store *EnvironmentStore) error {
	dir := filepath.Dir(filepath.Clean(store.Dir))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".zst" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		m, err := Inspect(ctx, path)
		if err != nil || m == nil || len(m.Contents.Environments) == 0 {
			continue // unreadable manifests name nothing we could protect
		}
		for _, d := range BundleEnvironmentBlobs(m) {
			if _, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO bundle_environment_refs (bundle_ref, digest) VALUES (?, ?)`, path, d); err != nil {
				return fmt.Errorf("backup: reconcile environment refs: %w", err)
			}
		}
	}
	return nil
}

// EnvironmentGC reports one collection.
type EnvironmentGC struct {
	Removed    []string `json:"removed"`
	FreedBytes int64    `json:"freed_bytes"`
	Kept       int      `json:"kept"`
}

// CollectEnvironmentGarbage deletes blobs no bundle references. It never
// deletes a blob with a ref, including a pending ref younger than a day
// (a capture in progress), and never deletes an unrecorded blob file
// younger than an hour. Stale pending refs are dropped first.
func CollectEnvironmentGarbage(ctx context.Context, db *sql.DB, store *EnvironmentStore, now time.Time) (EnvironmentGC, error) {
	out := EnvironmentGC{Removed: []string{}}
	if db == nil || store == nil {
		return out, nil
	}
	unlock := store.lock()
	defer unlock()

	cutoff := now.Add(-stalePendingAfter).UTC().Format("2006-01-02T15:04:05Z")
	if _, err := db.ExecContext(ctx, `DELETE FROM bundle_environment_refs WHERE bundle_ref LIKE 'pending:%' AND created_at < ?`, cutoff); err != nil {
		return out, fmt.Errorf("backup: drop stale environment refs: %w", err)
	}
	if err := reconcileBundleRefs(ctx, db, store); err != nil {
		return out, err
	}
	rows, err := db.QueryContext(ctx, `SELECT b.digest, b.size FROM environment_blobs b
		WHERE NOT EXISTS (SELECT 1 FROM bundle_environment_refs r WHERE r.digest = b.digest)`)
	if err != nil {
		return out, fmt.Errorf("backup: list unreferenced blobs: %w", err)
	}
	type blob struct {
		digest string
		size   int64
	}
	var dead []blob
	for rows.Next() {
		var b blob
		if err := rows.Scan(&b.digest, &b.size); err != nil {
			_ = rows.Close()
			return out, err
		}
		dead = append(dead, b)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	for _, b := range dead {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		p, err := store.BlobPath(b.digest)
		if err == nil {
			if st, statErr := os.Stat(p); statErr == nil {
				if err := os.Remove(p); err != nil {
					return out, fmt.Errorf("backup: remove blob %s: %w", b.digest, err)
				}
				out.FreedBytes += st.Size()
			}
		}
		// The row goes only if the digest is still unreferenced: a capture
		// may have referenced it between the query and here.
		if _, err := db.ExecContext(ctx, `DELETE FROM environment_blobs WHERE digest = ?
			AND NOT EXISTS (SELECT 1 FROM bundle_environment_refs WHERE digest = ?)`, b.digest, b.digest); err != nil {
			return out, err
		}
		out.Removed = append(out.Removed, b.digest)
	}

	// Blob files nothing records: a capture that crashed between writing
	// the file and recording it.
	dir := filepath.Join(store.Dir, "blobs", "sha256")
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return out, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		d := "sha256:" + e.Name()
		var n int
		if err := db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM environment_blobs WHERE digest = ?) + (SELECT COUNT(*) FROM bundle_environment_refs WHERE digest = ?)`, d, d).Scan(&n); err != nil {
			return out, err
		}
		if n > 0 {
			out.Kept++
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) < orphanBlobAfter {
			out.Kept++
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err == nil {
			out.FreedBytes += info.Size()
			out.Removed = append(out.Removed, d)
		}
	}
	// Leftover temp files of dead captures.
	if tmp, err := os.ReadDir(filepath.Join(store.Dir, "tmp")); err == nil {
		for _, e := range tmp {
			if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) > stalePendingAfter {
				_ = os.Remove(filepath.Join(store.Dir, "tmp", e.Name()))
			}
		}
	}
	return out, nil
}
