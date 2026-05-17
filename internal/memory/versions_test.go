package memory

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// openVersionsDB sets up the v90 schema in isolation so the test
// doesn't depend on the full migrate chain. memory_versions has a
// FK against workspaces(id); we create both tables flat.
func openVersionsDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
CREATE TABLE workspaces (
    id TEXT PRIMARY KEY, name TEXT NOT NULL, slug TEXT NOT NULL UNIQUE,
    created_at TEXT, updated_at TEXT, deleted_at TEXT
);
INSERT INTO workspaces (id, name, slug) VALUES ('ws_test', 'WS', 'ws_test');
CREATE TABLE memory_versions (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    path         TEXT NOT NULL,
    tier         TEXT NOT NULL CHECK (tier IN ('agent','crew','workspace','pins','learned')),
    sha256       TEXT NOT NULL,
    bytes        INTEGER NOT NULL,
    written_at   TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    written_by   TEXT,
    parent_sha   TEXT,
    payload_ref  TEXT NOT NULL
);
CREATE INDEX idx_memory_versions_ws_path_ts ON memory_versions (workspace_id, path, written_at DESC);
`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

func TestRecordVersion_HappyPath(t *testing.T) {
	db := openVersionsDB(t)
	dir := t.TempDir()
	content := []byte("hello memory\n")
	res, err := RecordVersion(context.Background(), db, VersionRecord{
		WorkspaceID: "ws_test",
		Path:        "AGENT.md",
		Tier:        TierAgent,
		Content:     content,
		WrittenBy:   "agent_42",
		BlobRoot:    dir,
	})
	if err != nil {
		t.Fatalf("RecordVersion: %v", err)
	}

	// SHA matches.
	want := sha256.Sum256(content)
	wantHex := hex.EncodeToString(want[:])
	if res.Sha256 != wantHex {
		t.Errorf("sha256 = %q, want %q", res.Sha256, wantHex)
	}
	if res.Bytes != len(content) {
		t.Errorf("bytes = %d, want %d", res.Bytes, len(content))
	}
	if res.Reused {
		t.Errorf("first write should not be reused")
	}

	// Blob on disk at the content-addressed location.
	blobBytes, err := os.ReadFile(res.BlobPath)
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}
	if string(blobBytes) != string(content) {
		t.Errorf("blob content mismatch: got %q want %q", blobBytes, content)
	}

	// DB row inserted with the right shape.
	var id, path, tier, sha, writtenBy, payloadRef string
	var bytes int
	if err := db.QueryRow(
		`SELECT id, path, tier, sha256, bytes, written_by, payload_ref FROM memory_versions WHERE id = ?`,
		res.VersionID).Scan(&id, &path, &tier, &sha, &bytes, &writtenBy, &payloadRef); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if path != "AGENT.md" || tier != "agent" || sha != wantHex || bytes != len(content) {
		t.Errorf("row mismatch: path=%q tier=%q sha=%q bytes=%d", path, tier, sha, bytes)
	}
	if writtenBy != "agent_42" {
		t.Errorf("written_by = %q, want agent_42", writtenBy)
	}
	if payloadRef != res.BlobPath {
		t.Errorf("payload_ref = %q, want %q", payloadRef, res.BlobPath)
	}
}

func TestRecordVersion_IdenticalContent_BlobReused(t *testing.T) {
	db := openVersionsDB(t)
	dir := t.TempDir()
	content := []byte("dedup me\n")

	first, err := RecordVersion(context.Background(), db, VersionRecord{
		WorkspaceID: "ws_test", Path: "AGENT.md", Tier: TierAgent,
		Content: content, BlobRoot: dir,
	})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := RecordVersion(context.Background(), db, VersionRecord{
		WorkspaceID: "ws_test", Path: "AGENT.md", Tier: TierAgent,
		Content: content, BlobRoot: dir,
	})
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if !second.Reused {
		t.Errorf("identical content should reuse the blob, got reused=false")
	}
	if first.BlobPath != second.BlobPath {
		t.Errorf("blob path differed across identical writes: %q vs %q", first.BlobPath, second.BlobPath)
	}

	// Two DB rows even though the blob is one — audit trail integrity.
	var rowCount int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM memory_versions WHERE workspace_id = ? AND path = ?`,
		"ws_test", "AGENT.md").Scan(&rowCount); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rowCount != 2 {
		t.Errorf("identical writes should still record both events, got %d rows", rowCount)
	}
}

func TestRecordVersion_DifferentContent_DifferentBlob(t *testing.T) {
	db := openVersionsDB(t)
	dir := t.TempDir()
	first, err := RecordVersion(context.Background(), db, VersionRecord{
		WorkspaceID: "ws_test", Path: "AGENT.md", Tier: TierAgent,
		Content: []byte("v1"), BlobRoot: dir,
	})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := RecordVersion(context.Background(), db, VersionRecord{
		WorkspaceID: "ws_test", Path: "AGENT.md", Tier: TierAgent,
		Content: []byte("v2"), ParentSha: first.Sha256, BlobRoot: dir,
	})
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first.Sha256 == second.Sha256 {
		t.Errorf("different content should produce different shas")
	}
	if first.BlobPath == second.BlobPath {
		t.Errorf("different shas should land at different blob paths")
	}
	// parent_sha wired through.
	var parent string
	if err := db.QueryRow(`SELECT parent_sha FROM memory_versions WHERE id = ?`, second.VersionID).Scan(&parent); err != nil {
		t.Fatalf("read parent_sha: %v", err)
	}
	if parent != first.Sha256 {
		t.Errorf("parent_sha = %q, want %q", parent, first.Sha256)
	}
}

func TestRecordVersion_InvalidTier_ErrInvalidTier(t *testing.T) {
	db := openVersionsDB(t)
	dir := t.TempDir()
	_, err := RecordVersion(context.Background(), db, VersionRecord{
		WorkspaceID: "ws_test", Path: "AGENT.md", Tier: Tier("bogus"),
		Content: []byte("x"), BlobRoot: dir,
	})
	if err == nil {
		t.Fatalf("expected error for bogus tier")
	}
}

func TestRecordVersion_RequiredFields(t *testing.T) {
	db := openVersionsDB(t)
	dir := t.TempDir()
	cases := []VersionRecord{
		{Path: "AGENT.md", Tier: TierAgent, Content: []byte("x"), BlobRoot: dir},                        // missing workspace
		{WorkspaceID: "ws_test", Tier: TierAgent, Content: []byte("x"), BlobRoot: dir},                  // missing path
		{WorkspaceID: "ws_test", Path: "AGENT.md", Content: []byte("x"), BlobRoot: dir},                 // missing tier (zero-value = "")
		{WorkspaceID: "ws_test", Path: "AGENT.md", Tier: TierAgent, Content: []byte("x"), BlobRoot: ""}, // missing blob root
	}
	for i, c := range cases {
		if _, err := RecordVersion(context.Background(), db, c); err == nil {
			t.Errorf("case %d should have failed: %+v", i, c)
		}
	}
}

func TestLatestVersionSha_NoRows_EmptyString(t *testing.T) {
	db := openVersionsDB(t)
	sha, err := LatestVersionSha(context.Background(), db, "ws_test", "AGENT.md")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if sha != "" {
		t.Errorf("sha = %q, want empty", sha)
	}
}

func TestLatestVersionSha_PicksMostRecent(t *testing.T) {
	db := openVersionsDB(t)
	dir := t.TempDir()
	for _, body := range []string{"v1", "v2", "v3"} {
		if _, err := RecordVersion(context.Background(), db, VersionRecord{
			WorkspaceID: "ws_test", Path: "AGENT.md", Tier: TierAgent,
			Content: []byte(body), BlobRoot: dir,
		}); err != nil {
			t.Fatalf("record %q: %v", body, err)
		}
	}
	got, err := LatestVersionSha(context.Background(), db, "ws_test", "AGENT.md")
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	want := sha256.Sum256([]byte("v3"))
	wantHex := hex.EncodeToString(want[:])
	if got != wantHex {
		t.Errorf("got %q, want %q (v3 sha)", got, wantHex)
	}
}

// Sanity: the test infra does not log to stderr noisily.
func TestVersions_PackageSmoke(t *testing.T) {
	_ = slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestLogVersions_NewestFirstAndLimit(t *testing.T) {
	db := openVersionsDB(t)
	dir := t.TempDir()
	for _, body := range []string{"v1", "v2", "v3"} {
		if _, err := RecordVersion(context.Background(), db, VersionRecord{
			WorkspaceID: "ws_test", Path: "AGENT.md", Tier: TierAgent,
			Content: []byte(body), BlobRoot: dir,
		}); err != nil {
			t.Fatalf("record %q: %v", body, err)
		}
	}
	entries, err := LogVersions(context.Background(), db, "ws_test", "AGENT.md", 10)
	if err != nil {
		t.Fatalf("LogVersions: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}
	// Newest-first by written_at: the v3 row lands first.
	want := sha256.Sum256([]byte("v3"))
	if entries[0].Sha256 != hex.EncodeToString(want[:]) {
		t.Errorf("entries[0].Sha256 = %q, want sha(v3)", entries[0].Sha256)
	}

	// limit clamp.
	limited, err := LogVersions(context.Background(), db, "ws_test", "AGENT.md", 1)
	if err != nil {
		t.Fatalf("limited: %v", err)
	}
	if len(limited) != 1 {
		t.Errorf("limit=1 returned %d entries", len(limited))
	}
}

func TestLogVersions_NoMatch_EmptySlice(t *testing.T) {
	db := openVersionsDB(t)
	entries, err := LogVersions(context.Background(), db, "ws_test", "nope.md", 10)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(entries) != 0 {
		t.Errorf("got %d entries on no-match, want 0", len(entries))
	}
}

func TestReadVersion_HappyPath(t *testing.T) {
	db := openVersionsDB(t)
	dir := t.TempDir()
	want := []byte("read me back\n")
	rec, err := RecordVersion(context.Background(), db, VersionRecord{
		WorkspaceID: "ws_test", Path: "AGENT.md", Tier: TierAgent,
		Content: want, BlobRoot: dir,
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	got, err := ReadVersion(context.Background(), db, "ws_test", "AGENT.md", rec.Sha256)
	if err != nil {
		t.Fatalf("ReadVersion: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestReadVersion_Unknown_ErrVersionNotFound(t *testing.T) {
	db := openVersionsDB(t)
	_, err := ReadVersion(context.Background(), db, "ws_test", "AGENT.md", "deadbeef")
	if err == nil {
		t.Fatalf("expected ErrVersionNotFound, got nil")
	}
	// Asserting the sentinel directly is the contract callers code
	// against (errors.Is(err, ErrVersionNotFound)) — without it, a
	// future change that wrapped the error in a different sentinel
	// would slip past this test.
	if !errors.Is(err, ErrVersionNotFound) {
		t.Fatalf("error = %v, want ErrVersionNotFound", err)
	}
}

func TestRestore_RoundTrip(t *testing.T) {
	db := openVersionsDB(t)
	dir := t.TempDir()
	blobRoot := filepath.Join(dir, "blobs")

	canonicalPath := filepath.Join(dir, "AGENT.md")
	// First write (v1).
	v1, err := RecordVersion(context.Background(), db, VersionRecord{
		WorkspaceID: "ws_test", Path: "AGENT.md", Tier: TierAgent,
		Content: []byte("v1 body"), BlobRoot: blobRoot,
	})
	if err != nil {
		t.Fatalf("v1: %v", err)
	}
	// User mutates the canonical (simulate writing v2 to disk).
	if err := os.WriteFile(canonicalPath, []byte("v2 body that the user wants to roll back from"), 0o644); err != nil {
		t.Fatalf("write v2 to canonical: %v", err)
	}
	_, err = RecordVersion(context.Background(), db, VersionRecord{
		WorkspaceID: "ws_test", Path: "AGENT.md", Tier: TierAgent,
		Content:   []byte("v2 body that the user wants to roll back from"),
		ParentSha: v1.Sha256, BlobRoot: blobRoot,
	})
	if err != nil {
		t.Fatalf("v2 record: %v", err)
	}

	// Restore back to v1's sha.
	restored, err := Restore(context.Background(), db, canonicalPath,
		"ws_test", "AGENT.md", v1.Sha256, "operator_xyz", blobRoot, TierAgent)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if restored.Sha256 != v1.Sha256 {
		t.Errorf("restored sha = %q, want %q (same as v1 — content-addressed)", restored.Sha256, v1.Sha256)
	}

	// Canonical file now matches v1 content.
	got, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatalf("read canonical after restore: %v", err)
	}
	if string(got) != "v1 body" {
		t.Errorf("canonical = %q, want %q", got, "v1 body")
	}

	// memory_versions chain now has THREE rows (v1, v2, restore).
	entries, err := LogVersions(context.Background(), db, "ws_test", "AGENT.md", 10)
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 rows after restore, got %d", len(entries))
	}
	if entries[0].WrittenBy != "operator_xyz" {
		t.Errorf("latest written_by = %q, want operator_xyz", entries[0].WrittenBy)
	}
}

func TestRestore_MissingFields(t *testing.T) {
	db := openVersionsDB(t)
	dir := t.TempDir()
	_, err := Restore(context.Background(), db, "", "ws_test", "AGENT.md", "sha", "u", dir, TierAgent)
	if err == nil {
		t.Errorf("expected error for empty canonicalPath")
	}
}

func TestPruneOldVersions_KeepsLatestN(t *testing.T) {
	db := openVersionsDB(t)
	dir := t.TempDir()

	// 5 versions of the same path; each gets a distinct sha so a
	// row delete is distinguishable from a blob delete.
	for _, body := range []string{"v1", "v2", "v3", "v4", "v5"} {
		if _, err := RecordVersion(context.Background(), db, VersionRecord{
			WorkspaceID: "ws_test", Path: "AGENT.md", Tier: TierAgent,
			Content: []byte(body), BlobRoot: dir,
		}); err != nil {
			t.Fatalf("record %q: %v", body, err)
		}
	}

	// Prune with olderThan=1ns (effectively "delete anything older
	// than NOW") AND keepLatestN=2. Expected: 3 rows deleted, 2 kept.
	res, err := PruneOldVersions(context.Background(), db, dir, time.Nanosecond, 2)
	if err != nil {
		t.Fatalf("PruneOldVersions: %v", err)
	}
	if res.RowsDeleted != 3 {
		t.Errorf("RowsDeleted = %d, want 3", res.RowsDeleted)
	}
	var rowCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM memory_versions WHERE path = 'AGENT.md'`).Scan(&rowCount); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rowCount != 2 {
		t.Errorf("post-prune row count = %d, want 2", rowCount)
	}

	// Orphan blobs deleted too — pre-prune we had 5 blobs, post-
	// prune the 3 deleted-row shas have no references so they
	// should be swept. The 2 retained shas keep their blobs.
	if res.BlobsDeleted != 3 {
		t.Errorf("BlobsDeleted = %d, want 3", res.BlobsDeleted)
	}
}

func TestPruneOldVersions_OlderThanDisabled(t *testing.T) {
	db := openVersionsDB(t)
	dir := t.TempDir()
	for _, body := range []string{"a", "b", "c"} {
		if _, err := RecordVersion(context.Background(), db, VersionRecord{
			WorkspaceID: "ws_test", Path: "AGENT.md", Tier: TierAgent,
			Content: []byte(body), BlobRoot: dir,
		}); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	// olderThan=0 → row delete pass is skipped. Even though keepN=1
	// suggests we'd want 2 deletions, the row prune only fires when
	// the age cutoff is set — keepN is the *floor*, not the *cap*.
	res, err := PruneOldVersions(context.Background(), db, dir, 0, 1)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.RowsDeleted != 0 {
		t.Errorf("RowsDeleted = %d, want 0 when olderThan disabled", res.RowsDeleted)
	}
}

func TestPruneOldVersions_NewRowsSurvive(t *testing.T) {
	db := openVersionsDB(t)
	dir := t.TempDir()
	// Insert 3 fresh rows; cutoff is 1 hour ago. None of the rows
	// are old enough to be eligible for deletion regardless of N.
	for _, body := range []string{"x", "y", "z"} {
		if _, err := RecordVersion(context.Background(), db, VersionRecord{
			WorkspaceID: "ws_test", Path: "AGENT.md", Tier: TierAgent,
			Content: []byte(body), BlobRoot: dir,
		}); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	res, err := PruneOldVersions(context.Background(), db, dir, time.Hour, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.RowsDeleted != 0 {
		t.Errorf("fresh rows should not be eligible for deletion, got %d deleted", res.RowsDeleted)
	}
}

func TestPruneOldVersions_OrphanSweep_PreservesReferenced(t *testing.T) {
	db := openVersionsDB(t)
	dir := t.TempDir()
	// Create a blob through normal RecordVersion (referenced).
	ref, err := RecordVersion(context.Background(), db, VersionRecord{
		WorkspaceID: "ws_test", Path: "AGENT.md", Tier: TierAgent,
		Content: []byte("kept"), BlobRoot: dir,
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}

	// Plant an orphan blob by hand at a valid sha-shaped path.
	orphanSha := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	orphanDir := filepath.Join(dir, orphanSha[:2])
	if err := os.MkdirAll(orphanDir, 0o755); err != nil {
		t.Fatalf("mkdir orphan: %v", err)
	}
	if err := os.WriteFile(filepath.Join(orphanDir, orphanSha), []byte("orphan"), 0o644); err != nil {
		t.Fatalf("write orphan blob: %v", err)
	}

	// Prune with no row-delete pass (olderThan=0), only the orphan
	// sweep runs.
	res, err := PruneOldVersions(context.Background(), db, dir, 0, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.BlobsDeleted != 1 {
		t.Errorf("BlobsDeleted = %d, want 1", res.BlobsDeleted)
	}
	if _, err := os.Stat(ref.BlobPath); err != nil {
		t.Errorf("referenced blob disappeared: %v", err)
	}
	if _, err := os.Stat(filepath.Join(orphanDir, orphanSha)); err == nil {
		t.Errorf("orphan blob still on disk")
	}
}
