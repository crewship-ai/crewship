package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecordVersion_BlobWriteFailure(t *testing.T) {
	db := openVersionsDB(t)
	// BlobRoot nested under a regular FILE → MkdirAll inside
	// writeBlobIfMissing fails with ENOTDIR.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := RecordVersion(context.Background(), db, VersionRecord{
		WorkspaceID: "ws_test", Path: "AGENT.md", Tier: TierAgent,
		Content: []byte("body"), BlobRoot: filepath.Join(blocker, "versions"),
	})
	if err == nil || !strings.Contains(err.Error(), "write blob") {
		t.Fatalf("expected write-blob error, got %v", err)
	}
	// No row may exist for a failed blob write.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM memory_versions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("rows = %d, want 0 after blob failure", n)
	}
}

func TestRecordVersion_InsertFailure_CancelledContext(t *testing.T) {
	db := openVersionsDB(t)
	blobRoot := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := RecordVersion(ctx, db, VersionRecord{
		WorkspaceID: "ws_test", Path: "AGENT.md", Tier: TierAgent,
		Content: []byte("body"), BlobRoot: blobRoot,
	})
	if err == nil || !strings.Contains(err.Error(), "insert memory_version") {
		t.Fatalf("expected insert error, got %v", err)
	}
	// The orphan blob is intentionally left on disk (retention sweeps it).
	sum := sha256.Sum256([]byte("body"))
	sha := hex.EncodeToString(sum[:])
	if _, statErr := os.Stat(filepath.Join(blobRoot, sha[:2], sha)); statErr != nil {
		t.Errorf("blob should remain after insert failure: %v", statErr)
	}
}

func TestWriteBlobIfMissing_TempWriteFailure_ReadOnlyDir(t *testing.T) {
	dir := t.TempDir()
	shard := filepath.Join(dir, "ab")
	if err := os.MkdirAll(shard, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shard, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(shard, 0o755) })
	_, err := writeBlobIfMissing(filepath.Join(shard, strings.Repeat("a", 64)), []byte("x"))
	if err == nil {
		t.Fatal("expected tempfile write error on read-only shard dir")
	}
}

func TestWriteBlobIfMissing_ExistingBlobShortCircuits(t *testing.T) {
	dir := t.TempDir()
	blob := filepath.Join(dir, "cd", strings.Repeat("c", 64))
	if err := os.MkdirAll(filepath.Dir(blob), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blob, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	reused, err := writeBlobIfMissing(blob, []byte("different content ignored"))
	if err != nil {
		t.Fatalf("writeBlobIfMissing: %v", err)
	}
	if !reused {
		t.Error("existing blob must report reused=true")
	}
	data, _ := os.ReadFile(blob)
	if string(data) != "original" {
		t.Errorf("existing blob rewritten to %q", data)
	}
}

func TestLogVersions_QueryError_CancelledContext(t *testing.T) {
	db := openVersionsDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := LogVersions(ctx, db, "ws_test", "AGENT.md", 10)
	if err == nil || !strings.Contains(err.Error(), "query memory_versions") {
		t.Fatalf("expected query error, got %v", err)
	}
}

func TestLogVersions_NonPositiveLimitDefaultsTo20(t *testing.T) {
	db := openVersionsDB(t)
	blobRoot := t.TempDir()
	if _, err := RecordVersion(context.Background(), db, VersionRecord{
		WorkspaceID: "ws_test", Path: "AGENT.md", Tier: TierAgent,
		Content: []byte("v1"), BlobRoot: blobRoot,
	}); err != nil {
		t.Fatal(err)
	}
	out, err := LogVersions(context.Background(), db, "ws_test", "AGENT.md", 0)
	if err != nil {
		t.Fatalf("LogVersions limit=0: %v", err)
	}
	if len(out) != 1 {
		t.Errorf("entries = %d, want 1", len(out))
	}
}

func TestAtomicRestoreWrite_TempWriteFailure_ReadOnlyDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if err := atomicRestoreWrite(filepath.Join(dir, "AGENT.md"), []byte("c")); err == nil {
		t.Fatal("expected tempfile write error in read-only dir")
	}
}

func TestLogVersions_LimitClampedTo1000(t *testing.T) {
	db := openVersionsDB(t)
	blobRoot := t.TempDir()
	if _, err := RecordVersion(context.Background(), db, VersionRecord{
		WorkspaceID: "ws_test", Path: "AGENT.md", Tier: TierAgent,
		Content: []byte("v1"), BlobRoot: blobRoot,
	}); err != nil {
		t.Fatal(err)
	}
	out, err := LogVersions(context.Background(), db, "ws_test", "AGENT.md", 99999)
	if err != nil {
		t.Fatalf("LogVersions: %v", err)
	}
	if len(out) != 1 {
		t.Errorf("entries = %d, want 1", len(out))
	}
}

func TestLogVersions_ScanError_MalformedRow(t *testing.T) {
	db := openVersionsDB(t)
	// bytes column carrying non-numeric TEXT makes Scan into *int fail.
	if _, err := db.Exec(`
		INSERT INTO memory_versions (id, workspace_id, path, tier, sha256, bytes, written_at, payload_ref)
		VALUES ('mv_bad', 'ws_test', 'AGENT.md', 'agent', 'deadbeef', 'not-a-number', '2026-01-01T00:00:00Z', '/tmp/x')`); err != nil {
		t.Fatal(err)
	}
	_, err := LogVersions(context.Background(), db, "ws_test", "AGENT.md", 10)
	if err == nil || !strings.Contains(err.Error(), "scan memory_versions") {
		t.Fatalf("expected scan error, got %v", err)
	}
}

func TestReadVersion_LookupError_CancelledContext(t *testing.T) {
	db := openVersionsDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ReadVersion(ctx, db, "ws_test", "AGENT.md", "abc")
	if err == nil || errors.Is(err, ErrVersionNotFound) {
		t.Fatalf("expected non-sentinel lookup error, got %v", err)
	}
}

func TestReadVersion_BlobMissing_SurfacesReadError(t *testing.T) {
	db := openVersionsDB(t)
	if _, err := db.Exec(`
		INSERT INTO memory_versions (id, workspace_id, path, tier, sha256, bytes, written_at, payload_ref)
		VALUES ('mv_gone', 'ws_test', 'AGENT.md', 'agent', 'cafebabe', 4, '2026-01-01T00:00:00Z', ?)`,
		filepath.Join(t.TempDir(), "missing-blob")); err != nil {
		t.Fatal(err)
	}
	_, err := ReadVersion(context.Background(), db, "ws_test", "AGENT.md", "cafebabe")
	if err == nil || !strings.Contains(err.Error(), "read blob") {
		t.Fatalf("expected read-blob error, got %v", err)
	}
}

func TestRestore_UnknownSha_PropagatesNotFound(t *testing.T) {
	db := openVersionsDB(t)
	_, err := Restore(context.Background(), db,
		filepath.Join(t.TempDir(), "AGENT.md"),
		"ws_test", "AGENT.md", "ffffffff", "operator", t.TempDir(), TierAgent)
	if !errors.Is(err, ErrVersionNotFound) {
		t.Fatalf("expected ErrVersionNotFound, got %v", err)
	}
}

func TestRestore_CanonicalPathIsDirectory_WriteFails(t *testing.T) {
	db := openVersionsDB(t)
	blobRoot := t.TempDir()
	if _, err := RecordVersion(context.Background(), db, VersionRecord{
		WorkspaceID: "ws_test", Path: "AGENT.md", Tier: TierAgent,
		Content: []byte("v1"), BlobRoot: blobRoot,
	}); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("v1"))
	sha := hex.EncodeToString(sum[:])

	canonical := filepath.Join(t.TempDir(), "AGENT.md")
	if err := os.MkdirAll(canonical, 0o755); err != nil { // rename target is a dir
		t.Fatal(err)
	}
	_, err := Restore(context.Background(), db, canonical, "ws_test", "AGENT.md", sha, "operator", blobRoot, TierAgent)
	if err == nil || !strings.Contains(err.Error(), "restore write") {
		t.Fatalf("expected restore-write error, got %v", err)
	}
}

func TestAtomicRestoreWrite_MkdirFailure(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := atomicRestoreWrite(filepath.Join(blocker, "sub", "AGENT.md"), []byte("c")); err == nil {
		t.Fatal("expected mkdir error when parent chain crosses a regular file")
	}
}

func TestAtomicRestoreWrite_RenameFailure_CleansTemp(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "AGENT.md")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := atomicRestoreWrite(target, []byte("c")); err == nil {
		t.Fatal("expected rename error when target is a directory")
	}
	if _, err := os.Stat(target + ".restore.tmp"); !os.IsNotExist(err) {
		t.Errorf("temp file leaked after failed rename")
	}
}

func TestPruneOldVersions_DeleteError_AndEmptyBlobRoot(t *testing.T) {
	db := openVersionsDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := PruneOldVersions(ctx, db, "", time.Hour, -3) // negative keepN normalised to 0
	if err != nil {
		t.Fatalf("PruneOldVersions must not hard-fail on row-delete error: %v", err)
	}
	if len(out.Errors) != 1 || !strings.Contains(out.Errors[0].Error(), "delete old rows") {
		t.Errorf("Errors = %v, want one delete-old-rows error", out.Errors)
	}
	if out.RowsDeleted != 0 || out.BlobsDeleted != 0 {
		t.Errorf("counts = %d/%d, want 0/0", out.RowsDeleted, out.BlobsDeleted)
	}
}

func TestSweepOrphanBlobs_QueryError_CancelledContext(t *testing.T) {
	db := openVersionsDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	deleted, errs := sweepOrphanBlobs(ctx, db, t.TempDir())
	if deleted != 0 || len(errs) != 1 || !strings.Contains(errs[0].Error(), "list referenced shas") {
		t.Fatalf("got deleted=%d errs=%v, want sha-list error", deleted, errs)
	}
}

func TestSweepOrphanBlobs_MissingRootIsFine(t *testing.T) {
	db := openVersionsDB(t)
	deleted, errs := sweepOrphanBlobs(context.Background(), db, filepath.Join(t.TempDir(), "no-such-root"))
	if deleted != 0 || len(errs) != 0 {
		t.Fatalf("missing blobRoot must be a no-op, got deleted=%d errs=%v", deleted, errs)
	}
}

func TestSweepOrphanBlobs_MixedTree(t *testing.T) {
	db := openVersionsDB(t)
	blobRoot := t.TempDir()

	// Referenced blob via a real RecordVersion.
	res, err := RecordVersion(context.Background(), db, VersionRecord{
		WorkspaceID: "ws_test", Path: "AGENT.md", Tier: TierAgent,
		Content: []byte("keep me"), BlobRoot: blobRoot,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Orphan with a valid 64-char sha-shaped name → deleted.
	orphan := filepath.Join(blobRoot, "ff", strings.Repeat("f", 64))
	if err := os.MkdirAll(filepath.Dir(orphan), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orphan, []byte("orphan"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Non-sha names (tmp scaffolding) are never touched.
	scratch := filepath.Join(blobRoot, "ff", "scratch.tmp")
	if err := os.WriteFile(scratch, []byte("tmp"), 0o644); err != nil {
		t.Fatal(err)
	}

	deleted, errs := sweepOrphanBlobs(context.Background(), db, blobRoot)
	if len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1", deleted)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Errorf("orphan blob survived the sweep")
	}
	if _, err := os.Stat(res.BlobPath); err != nil {
		t.Errorf("referenced blob removed: %v", err)
	}
	if _, err := os.Stat(scratch); err != nil {
		t.Errorf("non-sha scratch file removed: %v", err)
	}
}

func TestSweepOrphanBlobs_RemoveFailure_Collected(t *testing.T) {
	db := openVersionsDB(t)
	blobRoot := t.TempDir()
	shard := filepath.Join(blobRoot, "ee")
	orphan := filepath.Join(shard, strings.Repeat("e", 64))
	if err := os.MkdirAll(shard, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orphan, []byte("stuck"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shard, 0o555); err != nil { // read-only parent → unlink fails
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(shard, 0o755) })

	deleted, errs := sweepOrphanBlobs(context.Background(), db, blobRoot)
	if deleted != 0 {
		t.Errorf("deleted = %d, want 0", deleted)
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "delete orphan blob") {
		t.Errorf("errs = %v, want one delete-orphan error", errs)
	}
}

func TestSweepOrphanBlobs_UnreadableSubdir_WalkErrorCollected(t *testing.T) {
	db := openVersionsDB(t)
	blobRoot := t.TempDir()
	dark := filepath.Join(blobRoot, "dd")
	if err := os.MkdirAll(dark, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dark, 0o755) })

	deleted, errs := sweepOrphanBlobs(context.Background(), db, blobRoot)
	if deleted != 0 {
		t.Errorf("deleted = %d, want 0", deleted)
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "walk") {
		t.Errorf("errs = %v, want one walk error", errs)
	}
}

func TestLatestVersionSha_QueryError_CancelledContext(t *testing.T) {
	db := openVersionsDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := LatestVersionSha(ctx, db, "ws_test", "AGENT.md")
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}
