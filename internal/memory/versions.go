package memory

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Tier is the discriminator stored in memory_versions.tier. The five
// values match the CHECK constraint added in migration v90 — adding a
// new tier requires a schema change so a typo can't quietly write
// rows the UI does not know how to render.
type Tier string

const (
	TierAgent     Tier = "agent"
	TierCrew      Tier = "crew"
	TierWorkspace Tier = "workspace"
	TierPins      Tier = "pins"
	TierLearned   Tier = "learned"
)

// ValidTier returns true when t is one of the five documented values.
// Callers building a VersionRecord from untrusted input should validate
// before RecordVersion to surface the bad value at the boundary rather
// than at the DB CHECK.
func ValidTier(t Tier) bool {
	switch t {
	case TierAgent, TierCrew, TierWorkspace, TierPins, TierLearned:
		return true
	}
	return false
}

// VersionRecord is what callers pass to RecordVersion. Content is the
// full bytes about to be (or just) persisted to the canonical path;
// the function will compute its sha256, write the content-addressed
// blob under BlobRoot/<sha[:2]>/<sha> (idempotent — same sha skips
// the write), and insert one memory_versions row.
//
// Caller convention: Path is the human-meaningful identifier (e.g.
// "AGENT.md", "daily/2026-05-17.md", "topics/alpha-crew/pins.md").
// The actual on-disk location of the canonical file is independent —
// versions can reference content that lives anywhere; the audit
// trail is the row + blob pair, not a duplicate of the canonical
// path.
type VersionRecord struct {
	WorkspaceID string
	Path        string
	Tier        Tier
	Content     []byte
	WrittenBy   string
	ParentSha   string
	BlobRoot    string // {memoryRoot}/versions on disk
}

// RecordResult is what RecordVersion returns to its caller.
type RecordResult struct {
	VersionID string // 'mv_' + sha[:16]
	Sha256    string
	Bytes     int
	BlobPath  string // absolute path to the on-disk blob
	Reused    bool   // true when the blob existed and was not re-written
}

// ErrInvalidTier is returned when a VersionRecord carries a Tier value
// outside the documented set. Mapping to HTTP 400 is the caller's
// concern.
var ErrInvalidTier = errors.New("invalid memory tier")

// RecordVersion writes the content-addressed blob (if not already on
// disk) and inserts one memory_versions row. Returns ErrInvalidTier if
// rec.Tier is not in the documented set; surfaces filesystem and SQL
// errors verbatim so the caller can decide how to roll back. The
// canonical-file write is the caller's responsibility — versioning is
// a side-effect on top of that, not a replacement for it.
//
// Idempotence: a content-addressed blob whose path already exists is
// not re-written, so two RecordVersion calls with identical Content
// share the blob. The DB row is always new (audit-trail requirement —
// every write event must be traceable, even when the content
// duplicates a prior version).
func RecordVersion(ctx context.Context, db *sql.DB, rec VersionRecord) (*RecordResult, error) {
	if rec.WorkspaceID == "" {
		return nil, fmt.Errorf("workspace_id required")
	}
	if rec.Path == "" {
		return nil, fmt.Errorf("path required")
	}
	if !ValidTier(rec.Tier) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidTier, rec.Tier)
	}
	if rec.BlobRoot == "" {
		return nil, fmt.Errorf("blob_root required")
	}

	sum := sha256.Sum256(rec.Content)
	sha := hex.EncodeToString(sum[:])

	blobPath := filepath.Join(rec.BlobRoot, sha[:2], sha)
	reused, err := writeBlobIfMissing(blobPath, rec.Content)
	if err != nil {
		return nil, fmt.Errorf("write blob: %w", err)
	}

	// versionID embeds the content sha prefix for human-friendly
	// log output AND a random suffix so two writes of identical
	// content (legitimate audit-trail events) get distinct row
	// identities. PRIMARY KEY on the table catches the collision
	// if random ever repeats — should never happen with crypto/rand.
	var rnd [4]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return nil, fmt.Errorf("rand for version id: %w", err)
	}
	versionID := "mv_" + sha[:12] + "_" + hex.EncodeToString(rnd[:])
	var parentSha any
	if rec.ParentSha != "" {
		parentSha = rec.ParentSha
	}
	var writtenBy any
	if rec.WrittenBy != "" {
		writtenBy = rec.WrittenBy
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO memory_versions (
			id, workspace_id, path, tier, sha256, bytes,
			written_at, written_by, parent_sha, payload_ref
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		versionID, rec.WorkspaceID, rec.Path, string(rec.Tier),
		sha, len(rec.Content),
		time.Now().UTC().Format(time.RFC3339Nano),
		writtenBy, parentSha, blobPath,
	); err != nil {
		// DB insert failed AFTER the blob landed — the blob is harmless
		// orphan that a retention sweep will reap. Return the error so
		// the caller can log; do not delete the blob (a concurrent
		// caller may have legitimately created the same sha).
		return nil, fmt.Errorf("insert memory_version: %w", err)
	}

	return &RecordResult{
		VersionID: versionID,
		Sha256:    sha,
		Bytes:     len(rec.Content),
		BlobPath:  blobPath,
		Reused:    reused,
	}, nil
}

// writeBlobIfMissing writes content to blobPath atomically iff the
// path does not already exist. Returns (reused=true, nil) when the
// blob was already on disk. Concurrent callers racing on the same
// sha both succeed: one wins the rename, the other's tempfile is
// dropped via os.IsExist on Rename retry semantics, but because we
// use Stat-then-skip first the second caller usually short-circuits
// before any I/O.
func writeBlobIfMissing(blobPath string, content []byte) (bool, error) {
	if _, err := os.Stat(blobPath); err == nil {
		return true, nil
	}
	if err := os.MkdirAll(filepath.Dir(blobPath), 0o755); err != nil {
		return false, err
	}
	// Atomic write: tempfile in same dir → rename. Same pattern as
	// WriteFile's canonical-write path; the lock isn't needed here
	// because the on-disk path is content-addressed (two writers with
	// the same sha can race; the rename is the synchronisation point).
	tmpPath := blobPath + ".tmp"
	if err := os.WriteFile(tmpPath, content, 0o644); err != nil {
		return false, err
	}
	if err := os.Rename(tmpPath, blobPath); err != nil {
		_ = os.Remove(tmpPath)
		// Rename can fail benignly if another goroutine got there
		// first; re-check existence to decide whether to surface.
		if _, statErr := os.Stat(blobPath); statErr == nil {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

// VersionEntry is a single memory_versions row enriched for log output.
// JSON tags align with the CLI / API serialisation surface so the same
// struct round-trips through both.
type VersionEntry struct {
	ID         string `json:"id"`
	Path       string `json:"path"`
	Tier       string `json:"tier"`
	Sha256     string `json:"sha256"`
	Bytes      int    `json:"bytes"`
	WrittenAt  string `json:"written_at"`
	WrittenBy  string `json:"written_by"`
	ParentSha  string `json:"parent_sha,omitempty"`
	PayloadRef string `json:"payload_ref"`
}

// LogVersions returns the version chain for (workspaceID, path)
// newest-first. limit is hard-clamped to [1, 1000] so a CLI typo can't
// pull the whole table. Empty result + nil error when no rows match.
func LogVersions(ctx context.Context, db *sql.DB, workspaceID, path string, limit int) ([]VersionEntry, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := db.QueryContext(ctx, `
		SELECT id, path, tier, sha256, bytes, written_at,
		       COALESCE(written_by, ''), COALESCE(parent_sha, ''), payload_ref
		  FROM memory_versions
		 WHERE workspace_id = ? AND path = ?
		 ORDER BY written_at DESC
		 LIMIT ?`,
		workspaceID, path, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query memory_versions: %w", err)
	}
	defer rows.Close()
	var out []VersionEntry
	for rows.Next() {
		var v VersionEntry
		if err := rows.Scan(&v.ID, &v.Path, &v.Tier, &v.Sha256, &v.Bytes,
			&v.WrittenAt, &v.WrittenBy, &v.ParentSha, &v.PayloadRef); err != nil {
			return nil, fmt.Errorf("scan memory_versions: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ReadVersion returns the content of the version identified by
// (workspaceID, path, sha256). Looks up the payload_ref then reads
// the on-disk blob — content-addressed, so two rows with the same
// sha256 yield identical bytes. Returns ErrVersionNotFound when no
// row matches; surfaces filesystem errors verbatim if the blob is
// missing (retention sweep leaked a row).
func ReadVersion(ctx context.Context, db *sql.DB, workspaceID, path, sha string) ([]byte, error) {
	var payloadRef string
	err := db.QueryRowContext(ctx, `
		SELECT payload_ref FROM memory_versions
		 WHERE workspace_id = ? AND path = ? AND sha256 = ?
		 LIMIT 1`,
		workspaceID, path, sha).Scan(&payloadRef)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrVersionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lookup memory_version: %w", err)
	}
	content, err := os.ReadFile(payloadRef)
	if err != nil {
		return nil, fmt.Errorf("read blob %s: %w", payloadRef, err)
	}
	return content, nil
}

// Restore writes the historical content at sha back to canonicalPath
// atomically and records a fresh memory_versions row whose sha256
// matches the restored content. The new row's parent_sha is the
// previous latest (so the chain stays forward-only); writtenBy is
// the user performing the restore so the audit trail distinguishes
// it from a normal append.
//
// blobRoot must point at the same content-addressed store the
// original write used — content-addressed dedup makes this safe:
// the existing blob is reused when its sha already matches.
//
// Returns the new RecordResult on success. The blob path inside it
// will match the historical blob path (Reused=true).
func Restore(
	ctx context.Context,
	db *sql.DB,
	canonicalPath string,
	workspaceID, auditPath, sha, restoredBy, blobRoot string,
	tier Tier,
) (*RecordResult, error) {
	if canonicalPath == "" || workspaceID == "" || auditPath == "" || sha == "" || blobRoot == "" {
		return nil, fmt.Errorf("restore: missing required field")
	}
	content, err := ReadVersion(ctx, db, workspaceID, auditPath, sha)
	if err != nil {
		return nil, err
	}
	// Atomic replace of the canonical file. We bypass the WriteConfig
	// scrubber / cap on restore — the content already passed those
	// checks the first time it was written. Operators restoring stale
	// content know what they're doing.
	if err := atomicRestoreWrite(canonicalPath, content); err != nil {
		return nil, fmt.Errorf("restore write: %w", err)
	}
	parent, _ := LatestVersionSha(ctx, db, workspaceID, auditPath)
	return RecordVersion(ctx, db, VersionRecord{
		WorkspaceID: workspaceID,
		Path:        auditPath,
		Tier:        tier,
		Content:     content,
		WrittenBy:   restoredBy,
		ParentSha:   parent,
		BlobRoot:    blobRoot,
	})
}

// atomicRestoreWrite is the small fs-only sibling of WriteFile used
// by Restore. We do NOT take a flock because the caller is the only
// writer to canonicalPath at this point in the lifecycle (operator
// command, not a background goroutine), and reusing WriteFile would
// pull in scrubber+cap policy that defeats the "restore historical
// content" intent.
func atomicRestoreWrite(canonicalPath string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(canonicalPath), 0o755); err != nil {
		return err
	}
	tmp := canonicalPath + ".restore.tmp"
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, canonicalPath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// ErrVersionNotFound is returned by ReadVersion when (workspaceID,
// path, sha256) does not match any row. Callers wanting to map it to
// HTTP 404 / CLI exit 1 should errors.Is against this sentinel.
var ErrVersionNotFound = errors.New("memory version not found")

// PruneResult is what PruneOldVersions returns to its caller. Counts
// are best-effort: a per-row delete failure is logged and skipped,
// then surfaced via Errors so the daily sweep doesn't abort on one
// stuck blob.
type PruneResult struct {
	RowsDeleted  int64
	BlobsDeleted int64
	Errors       []error
}

// PruneOldVersions enforces the retention policy on memory_versions
// and sweeps orphan blobs. Two rules, applied in this order:
//
//  1. **Keep latest N per (workspace_id, path) regardless of age.**
//     A path with N=3 retains the 3 newest rows even if they're 90
//     days old. Matches Anthropic Managed Agents' "always keep the
//     last N versions" guarantee — operators always have at least N
//     restore targets per file.
//
//  2. **Delete rows older than cutoff.** Anything older than
//     (now - olderThan) that did NOT survive rule 1 is removed.
//
// After the row delete pass, any blob under blobRoot whose sha256
// is no longer referenced by ANY remaining row is deleted from
// disk. The sha256 index makes the existence check O(1) per blob.
//
// Zero / negative values for keepLatestN and olderThan act as
// disable signals: keepLatestN <= 0 means "keep every row newer
// than cutoff"; olderThan <= 0 disables the age-based delete
// entirely (the keep-N rule still applies).
func PruneOldVersions(
	ctx context.Context,
	db *sql.DB,
	blobRoot string,
	olderThan time.Duration,
	keepLatestN int,
) (*PruneResult, error) {
	out := &PruneResult{}
	if keepLatestN < 0 {
		keepLatestN = 0
	}

	cutoff := ""
	if olderThan > 0 {
		cutoff = time.Now().Add(-olderThan).UTC().Format(time.RFC3339Nano)
	}

	// Row prune: find (workspace_id, path) groups + delete the rows
	// that are both older than cutoff AND outside the keep-N window.
	// SQLite's `ROW_NUMBER() OVER (PARTITION BY ... ORDER BY ...)` does
	// this cleanly in one DELETE.
	if cutoff != "" {
		res, err := db.ExecContext(ctx, `
			DELETE FROM memory_versions
			WHERE id IN (
				SELECT id FROM (
					SELECT id, written_at,
					       ROW_NUMBER() OVER (
					         PARTITION BY workspace_id, path
					         ORDER BY written_at DESC
					       ) AS rn
					FROM memory_versions
				) ranked
				WHERE rn > ? AND written_at < ?
			)`,
			keepLatestN, cutoff,
		)
		if err != nil {
			out.Errors = append(out.Errors, fmt.Errorf("delete old rows: %w", err))
		} else {
			out.RowsDeleted, _ = res.RowsAffected()
		}
	}

	// Orphan blob sweep: build the set of still-referenced shas, walk
	// the blob dir, delete files whose sha isn't in the set. Skipped
	// when blobRoot is empty (caller has no on-disk store to sweep).
	if blobRoot == "" {
		return out, nil
	}
	deleted, sweepErrs := sweepOrphanBlobs(ctx, db, blobRoot)
	out.BlobsDeleted = deleted
	out.Errors = append(out.Errors, sweepErrs...)
	return out, nil
}

// sweepOrphanBlobs walks blobRoot's two-level sharded directory and
// removes any file whose name (the content sha) isn't referenced by
// a memory_versions row. The dir layout is blobRoot/{sha[:2]}/{sha};
// non-matching entries are ignored so concurrent writes don't trip
// the sweep.
func sweepOrphanBlobs(ctx context.Context, db *sql.DB, blobRoot string) (int64, []error) {
	var errs []error
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT sha256 FROM memory_versions`)
	if err != nil {
		return 0, []error{fmt.Errorf("list referenced shas: %w", err)}
	}
	defer rows.Close()
	referenced := make(map[string]struct{}, 256)
	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			errs = append(errs, fmt.Errorf("scan sha: %w", err))
			continue
		}
		referenced[sha] = struct{}{}
	}

	var deleted int64
	walkErr := filepath.Walk(blobRoot, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			// A missing blobRoot itself is fine — nothing to sweep.
			if os.IsNotExist(walkErr) {
				return nil
			}
			errs = append(errs, fmt.Errorf("walk %s: %w", path, walkErr))
			return nil
		}
		if info.IsDir() {
			return nil
		}
		name := info.Name()
		// Skip .tmp files from in-flight writeBlobIfMissing calls.
		// File names are content shas (lowercase hex, 64 chars) — anything
		// else is either temp scaffolding or operator-placed scratch and
		// shouldn't be touched.
		if len(name) != 64 {
			return nil
		}
		if _, ok := referenced[name]; ok {
			return nil
		}
		if err := os.Remove(path); err != nil {
			errs = append(errs, fmt.Errorf("delete orphan blob %s: %w", path, err))
			return nil
		}
		deleted++
		return nil
	})
	if walkErr != nil && !os.IsNotExist(walkErr) {
		errs = append(errs, walkErr)
	}
	return deleted, errs
}

// LatestVersionSha returns the most recent sha256 recorded for
// (workspaceID, path) — used by WriteFile callers as the parent_sha
// for the next write. Returns empty string + nil error when no prior
// version exists.
func LatestVersionSha(ctx context.Context, db *sql.DB, workspaceID, path string) (string, error) {
	var sha string
	err := db.QueryRowContext(ctx, `
		SELECT sha256 FROM memory_versions
		WHERE workspace_id = ? AND path = ?
		ORDER BY written_at DESC LIMIT 1`,
		workspaceID, path).Scan(&sha)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return sha, nil
}
