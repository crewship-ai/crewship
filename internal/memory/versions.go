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
