package backup

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// newTestBundle writes a minimal valid bundle to a temp file and
// returns the path. Used by Verify / Rotate / ForceReleaseLock tests
// so each can roundtrip a real bundle rather than mocking up the
// filesystem layout by hand.
func newTestBundle(t *testing.T, passphrase string) string {
	t.Helper()
	payload := []byte("test-payload-contents")
	manifest := newValidManifest()
	manifest.Checksums = Checksums{}
	manifest.Encryption = Encryption{}

	dir := t.TempDir()
	path := filepath.Join(dir, "crewship-workspace-test-20260101T000000Z.tar.zst")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create bundle: %v", err)
	}
	defer func() { _ = f.Close() }()

	opts := WriteBundleOptions{NoEncrypt: true}
	if passphrase != "" {
		opts = WriteBundleOptions{Passphrase: passphrase}
	}
	if err := WriteBundle(f, manifest, bytes.NewReader(payload), opts); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	return path
}

func TestVerify_ValidBundle(t *testing.T) {
	path := newTestBundle(t, "")
	res, err := Verify(t.Context(), path)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !res.Valid {
		t.Errorf("expected Valid=true, got err=%v", res.Err)
	}
	if res.Size == 0 {
		t.Error("expected non-zero size")
	}
	if res.Manifest == nil {
		t.Error("expected non-nil manifest")
	}
}

func TestVerify_TamperedBundle(t *testing.T) {
	path := newTestBundle(t, "")
	// Flip a byte in the middle of the file. The manifest itself is
	// near the start (tar header + JSON); the payload entry body is
	// further in. Pick ~70% to land in the payload region.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	data[len(data)*7/10] ^= 0xFF
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write tampered: %v", err)
	}
	res, err := Verify(t.Context(), path)
	if err != nil {
		t.Fatalf("Verify should return a result even on tamper: %v", err)
	}
	if res.Valid {
		t.Error("expected Valid=false on tampered bundle")
	}
	if res.Err == nil {
		t.Error("expected non-nil Err on tampered bundle")
	}
}

func TestVerify_MissingFile(t *testing.T) {
	_, err := Verify(t.Context(), filepath.Join(t.TempDir(), "nonexistent.tar.zst"))
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestForceReleaseLock_Idempotent(t *testing.T) {
	db := newLockTestDB(t)
	mgr := NewSQLLockManager(db)
	// Acquire a lock so we have something to release.
	release, err := mgr.AcquireWorkspaceLock(t.Context(), "ws_test", "user_1", 0)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	_ = release
	// Force-release works on a held lock.
	if err := ForceReleaseLock(t.Context(), db, "ws_test"); err != nil {
		t.Fatalf("ForceReleaseLock held: %v", err)
	}
	// And on a missing lock (idempotent).
	if err := ForceReleaseLock(t.Context(), db, "ws_test"); err != nil {
		t.Fatalf("ForceReleaseLock missing: %v", err)
	}
	// And on a different workspace (no-op).
	if err := ForceReleaseLock(t.Context(), db, "ws_other"); err != nil {
		t.Fatalf("ForceReleaseLock other: %v", err)
	}
}

func TestForceReleaseLock_RejectsEmptyInputs(t *testing.T) {
	if err := ForceReleaseLock(t.Context(), nil, "ws"); err == nil {
		t.Error("expected error for nil db")
	}
	db := newLockTestDB(t)
	if err := ForceReleaseLock(t.Context(), db, ""); err == nil {
		t.Error("expected error for empty workspace")
	}
}

func TestRotate_KeepLast(t *testing.T) {
	// Rotate needs real bundles under a directory. We produce 3, then
	// ask for keep-last=1 and verify the 2 oldest are deleted.
	dir := t.TempDir()

	// Build 3 bundles with ascending timestamps baked into the manifest.
	for i := 1; i <= 3; i++ {
		manifest := newValidManifest()
		manifest.Checksums = Checksums{}
		manifest.Encryption = Encryption{}
		manifest.Contents.Workspace.ID = "ws_scope"
		manifest.CreatedAt = manifest.CreatedAt.AddDate(0, 0, i)
		name := filepath.Join(dir, "crewship-workspace-scope-"+string(rune('a'+i-1))+".tar.zst")
		f, err := os.Create(name)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := WriteBundle(f, manifest, bytes.NewReader([]byte("x")), WriteBundleOptions{NoEncrypt: true}); err != nil {
			_ = f.Close()
			t.Fatalf("write bundle %d: %v", i, err)
		}
		_ = f.Close()
	}

	// Dry-run first — should report 2 deletions but leave disk intact.
	deleted, err := Rotate(t.Context(), dir, "ws_scope", 1 /*keepLast*/, 0 /*keepDays*/, true /*dryRun*/)
	if err != nil {
		t.Fatalf("dry rotate: %v", err)
	}
	if len(deleted) != 2 {
		t.Errorf("dry-run: expected 2 deletions, got %d: %v", len(deleted), deleted)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("dry-run should not have deleted anything, %d remain", len(entries))
	}

	// Real rotate.
	deleted, err = Rotate(t.Context(), dir, "ws_scope", 1, 0, false)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if len(deleted) != 2 {
		t.Errorf("expected 2 deletions, got %d", len(deleted))
	}
	entries, err = os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 bundle remaining, got %d", len(entries))
	}
}

func TestRotate_IgnoresOtherWorkspaces(t *testing.T) {
	dir := t.TempDir()
	// Two bundles, different workspace IDs. Every setup error is
	// fatal so a test that passes cannot hide behind a failed setup.
	for _, wsID := range []string{"ws_mine", "ws_other"} {
		manifest := newValidManifest()
		manifest.Checksums = Checksums{}
		manifest.Encryption = Encryption{}
		manifest.Contents.Workspace.ID = wsID
		name := filepath.Join(dir, "crewship-workspace-"+wsID+".tar.zst")
		f, err := os.Create(name)
		if err != nil {
			t.Fatalf("create %s: %v", wsID, err)
		}
		if err := WriteBundle(f, manifest, bytes.NewReader([]byte("x")), WriteBundleOptions{NoEncrypt: true}); err != nil {
			_ = f.Close()
			t.Fatalf("write bundle %s: %v", wsID, err)
		}
		if err := f.Close(); err != nil {
			t.Fatalf("close bundle %s: %v", wsID, err)
		}
	}
	// keep-last=1 would delete one of the two bundles if cross-workspace
	// scoping were broken; scoped to ws_mine there is only one bundle
	// in the pool and keep-last=1 leaves it alone.
	deleted, err := Rotate(t.Context(), dir, "ws_mine", 1, 0, false)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if len(deleted) != 0 {
		t.Errorf("expected 0 deletions (only 1 bundle in scope, keep-last=1), got %v", deleted)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("rotate should not touch other workspace's bundle, %d remain", len(entries))
	}
}

func TestRotate_ChecksumMismatchSurfaces(t *testing.T) {
	// Rotate's internal Inspect on a corrupt bundle should skip
	// rather than explode the whole rotate.
	dir := t.TempDir()
	// Write a garbage file with the expected suffix.
	if err := os.WriteFile(filepath.Join(dir, "crewship-workspace-corrupt.tar.zst"), []byte("not a tar"), 0o600); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}
	deleted, err := Rotate(t.Context(), dir, "ws", 1, 0, true)
	if err != nil {
		t.Fatalf("rotate should tolerate a corrupt file, got %v", err)
	}
	if len(deleted) != 0 {
		t.Errorf("corrupt file is not in scope for any workspace, got deletions: %v", deleted)
	}
}

// The errors package is referenced by the test suite elsewhere in the
// backup package; ensure our new test file remains self-contained.
var _ = errors.Is
