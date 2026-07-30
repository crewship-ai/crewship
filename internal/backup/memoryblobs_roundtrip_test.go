package backup_test

// Pins the fix for a silent data-loss bug: memory_versions rides every
// workspace-scope backup bundle (internal/backup/intent.go, dbdump.go),
// but the content-addressed blob files those rows point at
// ({MemoryRoot}/versions/<sha[:2]>/<sha>, see internal/memory/versions.go
// RecordVersion) were never collected. A restore landed memory_versions
// rows whose payload_ref pointed at files that do not exist on the
// target — memory history, HITL review, and memory restore all break,
// and it breaks QUIETLY because the DB rows are present and look fine
// until something tries to open the content.
//
// Reuses openMigratedDB / seedWorkspace from e2e_roundtrip_test.go (same
// package, same test binary).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/memory"
)

// TestBackupRestore_MemoryBlobsRoundTrip is the important test: it
// proves a memory version written on a source instance is actually
// READABLE after backup + restore onto a fresh target — not merely
// that a row landed in the DB.
//
// The source and target use DIFFERENT blob-root directories (as two
// real instances would — {MemoryRoot}/versions is an absolute host
// path that is not portable across installs). That detail matters:
// memory_versions.payload_ref is an absolute path baked in at write
// time, so a correct fix must rewrite it to the target's blob root,
// not just copy bytes into the same absolute location.
//
// After the restore, the source blob root is deleted entirely — so a
// test that (bug-for-bug) reads content back through the source path
// would fail loudly instead of silently passing on borrowed data.
func TestBackupRestore_MemoryBlobsRoundTrip(t *testing.T) {
	ctx := context.Background()

	source := openMigratedDB(t)
	workspaceID := seedWorkspace(t, source)

	sourceBlobRoot := filepath.Join(t.TempDir(), "source-versions")
	const content = "the crew learned something worth keeping"
	rec, err := memory.RecordVersion(ctx, source, memory.VersionRecord{
		WorkspaceID: workspaceID,
		Path:        "topics/pins.md",
		Tier:        memory.TierPins,
		Content:     []byte(content),
		WrittenBy:   "u_admin",
		BlobRoot:    sourceBlobRoot,
	})
	if err != nil {
		t.Fatalf("RecordVersion (seed): %v", err)
	}
	// Sanity: the write path did what internal/memory/versions.go docs.
	if _, err := os.Stat(rec.BlobPath); err != nil {
		t.Fatalf("seed blob missing right after RecordVersion: %v", err)
	}

	const passphrase = "memory-blob-roundtrip-passphrase-123"
	createResult, err := backup.CreateBackup(ctx, source, backup.CreateOptions{
		Scope:       backup.ScopeWorkspace,
		WorkspaceID: workspaceID,
		OutputDir:   t.TempDir(),
		Actor:       backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
		Passphrase:  passphrase,
		BlobRoot:    sourceBlobRoot,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	// Simulate real disaster recovery: the source blob store is gone.
	// Anything the restore reads must come from the bundle, not a
	// still-alive source directory.
	if err := os.RemoveAll(sourceBlobRoot); err != nil {
		t.Fatalf("simulate source loss: %v", err)
	}

	target := openMigratedDB(t)
	targetBlobRoot := filepath.Join(t.TempDir(), "target-versions")

	restoreResult, err := backup.RestoreBackup(ctx, target, backup.RestoreOptions{
		Path:       createResult.Path,
		Passphrase: passphrase,
		Actor:      backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
		BlobRoot:   targetBlobRoot,
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if restoreResult.RowsInserted <= 0 {
		t.Fatalf("RestoreBackup inserted %d rows; expected > 0", restoreResult.RowsInserted)
	}

	// The row must exist AND its payload_ref must point at the
	// TARGET's blob root (content-addressed: {targetBlobRoot}/sha[:2]/sha),
	// not the source's now-deleted path.
	var gotPayloadRef string
	if err := target.QueryRowContext(ctx,
		`SELECT payload_ref FROM memory_versions WHERE workspace_id = ? AND sha256 = ?`,
		workspaceID, rec.Sha256,
	).Scan(&gotPayloadRef); err != nil {
		t.Fatalf("query restored memory_versions row: %v", err)
	}
	wantPayloadRef := filepath.Join(targetBlobRoot, rec.Sha256[:2], rec.Sha256)
	if gotPayloadRef != wantPayloadRef {
		t.Errorf("restored payload_ref = %q, want %q (rewritten to target blob root)", gotPayloadRef, wantPayloadRef)
	}

	// The actual round-trip assertion: the content must be READABLE
	// through the normal memory.ReadVersion path against the target —
	// not just "a file happens to exist somewhere".
	got, err := memory.ReadVersion(ctx, target, workspaceID, "topics/pins.md", rec.Sha256)
	if err != nil {
		t.Fatalf("ReadVersion after restore: %v", err)
	}
	if string(got) != content {
		t.Errorf("restored memory blob content = %q, want %q", got, content)
	}
}

// TestBackupBundle_IncludesMemoryBlobs is the narrower, faster-to-debug
// sibling: it asserts the bundle's payload tar actually CARRIES the
// blob file for every memory_versions row the DB dump includes, without
// going all the way through a restore. Catches a regression closer to
// its source with a clearer failure message.
func TestBackupBundle_IncludesMemoryBlobs(t *testing.T) {
	ctx := context.Background()

	source := openMigratedDB(t)
	workspaceID := seedWorkspace(t, source)

	blobRoot := filepath.Join(t.TempDir(), "versions")
	const content = "bundle must carry this blob"
	rec, err := memory.RecordVersion(ctx, source, memory.VersionRecord{
		WorkspaceID: workspaceID,
		Path:        "AGENT.md",
		Tier:        memory.TierWorkspace,
		Content:     []byte(content),
		WrittenBy:   "u_admin",
		BlobRoot:    blobRoot,
	})
	if err != nil {
		t.Fatalf("RecordVersion (seed): %v", err)
	}

	createResult, err := backup.CreateBackup(ctx, source, backup.CreateOptions{
		Scope:       backup.ScopeWorkspace,
		WorkspaceID: workspaceID,
		OutputDir:   t.TempDir(),
		Actor:       backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
		NoEncrypt:   true, // simplifies raw payload inspection below
		BlobRoot:    blobRoot,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	if createResult.Manifest.Contents.MemoryBlobsIncluded != 1 {
		t.Errorf("manifest Contents.MemoryBlobsIncluded = %d, want 1", createResult.Manifest.Contents.MemoryBlobsIncluded)
	}
	if createResult.Manifest.Contents.MemoryBlobsMissing != 0 {
		t.Errorf("manifest Contents.MemoryBlobsMissing = %d, want 0", createResult.Manifest.Contents.MemoryBlobsMissing)
	}

	f, err := os.Open(createResult.Path)
	if err != nil {
		t.Fatalf("open bundle: %v", err)
	}
	defer f.Close()
	manifest, payload, err := backup.ReadBundle(f)
	if err != nil {
		t.Fatalf("ReadBundle: %v", err)
	}
	if manifest.Encryption.Enabled {
		t.Fatalf("expected plaintext payload (NoEncrypt), manifest says encrypted")
	}

	tr, err := backup.NewTarZstReader(payload)
	if err != nil {
		t.Fatalf("NewTarZstReader: %v", err)
	}
	defer tr.Close()

	wantEntry := "memory-blobs/" + rec.Sha256[:2] + "/" + rec.Sha256
	found := false
	var gotBody []byte
	for {
		hdr, err := tr.Next()
		if err != nil {
			break // io.EOF or read error; loop below reports if not found
		}
		if hdr.Name != wantEntry {
			continue
		}
		found = true
		buf := make([]byte, hdr.Size)
		if _, err := readFullTar(tr, buf); err != nil {
			t.Fatalf("read %s body: %v", wantEntry, err)
		}
		gotBody = buf
	}
	if !found {
		t.Fatalf("bundle payload does not contain %q — memory blob was not collected", wantEntry)
	}
	sum := sha256.Sum256(gotBody)
	if hex.EncodeToString(sum[:]) != rec.Sha256 {
		t.Errorf("bundled blob content hash = %x, want %s", sum, rec.Sha256)
	}
}

// readFullTar reads exactly len(buf) bytes from r, the same contract
// io.ReadFull provides — spelled out locally so this file doesn't need
// an extra import purely for one call.
func readFullTar(r interface{ Read([]byte) (int, error) }, buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		m, err := r.Read(buf[n:])
		n += m
		if err != nil {
			if n == len(buf) {
				return n, nil
			}
			return n, err
		}
	}
	return n, nil
}

// TestBackupCreate_MemoryVersionMissingBlobIsSkippedNotFatal pins the
// edge case the product will actually hit: a memory_versions row whose
// sha256 does NOT resolve to a blob on disk (e.g. surviving a retention
// sweep, or — the scenario that motivated this fix — a PRIOR restore
// that dropped blobs while keeping rows). Backup creation must not
// crash over this; it skips the missing blob and reports the count so
// an operator can investigate, and the rest of the bundle still lands.
func TestBackupCreate_MemoryVersionMissingBlobIsSkippedNotFatal(t *testing.T) {
	ctx := context.Background()

	source := openMigratedDB(t)
	workspaceID := seedWorkspace(t, source)

	blobRoot := filepath.Join(t.TempDir(), "versions")
	rec, err := memory.RecordVersion(ctx, source, memory.VersionRecord{
		WorkspaceID: workspaceID,
		Path:        "AGENT.md",
		Tier:        memory.TierWorkspace,
		Content:     []byte("will be orphaned"),
		WrittenBy:   "u_admin",
		BlobRoot:    blobRoot,
	})
	if err != nil {
		t.Fatalf("RecordVersion (seed): %v", err)
	}
	// Remove only the blob, leaving the memory_versions row intact —
	// exactly the shape of the bug this whole fix responds to.
	if err := os.Remove(rec.BlobPath); err != nil {
		t.Fatalf("remove blob to simulate orphan row: %v", err)
	}

	createResult, err := backup.CreateBackup(ctx, source, backup.CreateOptions{
		Scope:       backup.ScopeWorkspace,
		WorkspaceID: workspaceID,
		OutputDir:   t.TempDir(),
		Actor:       backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
		NoEncrypt:   true,
		BlobRoot:    blobRoot,
	})
	if err != nil {
		t.Fatalf("CreateBackup must not fail on an orphan memory_versions row: %v", err)
	}
	if createResult.Manifest.Contents.MemoryBlobsIncluded != 0 {
		t.Errorf("MemoryBlobsIncluded = %d, want 0 (the one row's blob is missing)", createResult.Manifest.Contents.MemoryBlobsIncluded)
	}
	if createResult.Manifest.Contents.MemoryBlobsMissing != 1 {
		t.Errorf("MemoryBlobsMissing = %d, want 1", createResult.Manifest.Contents.MemoryBlobsMissing)
	}

	// The DB row itself still rides the bundle even though its blob
	// doesn't — dropping the row too would be a second, independent
	// data-loss bug (losing the audit trail entry itself).
	f, err := os.Open(createResult.Path)
	if err != nil {
		t.Fatalf("open bundle: %v", err)
	}
	defer f.Close()
	_, payload, err := backup.ReadBundle(f)
	if err != nil {
		t.Fatalf("ReadBundle: %v", err)
	}
	tr, err := backup.NewTarZstReader(payload)
	if err != nil {
		t.Fatalf("NewTarZstReader: %v", err)
	}
	defer tr.Close()
	var dumpJSON []byte
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		if hdr.Name != "db/dump.json" {
			continue
		}
		buf := make([]byte, hdr.Size)
		if _, err := readFullTar(tr, buf); err != nil {
			t.Fatalf("read db/dump.json body: %v", err)
		}
		dumpJSON = buf
	}
	if dumpJSON == nil {
		t.Fatal("bundle has no db/dump.json section at all")
	}
	dump, err := backup.UnmarshalDump(dumpJSON)
	if err != nil {
		t.Fatalf("UnmarshalDump: %v", err)
	}
	rows := dump.Tables["memory_versions"]
	foundRow := false
	for _, row := range rows {
		if row["sha256"] == rec.Sha256 {
			foundRow = true
		}
	}
	if !foundRow {
		t.Errorf("db/dump.json memory_versions rows = %v; expected a row with sha256=%s "+
			"(the row itself must still ride the bundle even though its blob is missing)", rows, rec.Sha256)
	}
}
