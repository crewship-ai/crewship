package pagebuild

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B11 (G4): the writer must not produce a record the reader refuses.
func TestWriteBuildImageRecordRespectsReaderLimit(t *testing.T) {
	dir := t.TempDir()
	rec := &BuildImageRecord{Images: map[string]RecordedImage{}}
	id := "sha256:" + strings.Repeat("a", 64)
	for i := 0; i < 600; i++ {
		fp := fmt.Sprintf("%064d", i)
		rec.Images[fp] = RecordedImage{ID: id, Tag: ImageTag(fp)}
	}
	if err := WriteBuildImageRecord(dir, rec); err == nil {
		// If it was written, the reader must accept it.
		if _, rerr := ReadBuildImageRecord(dir); rerr != nil {
			t.Fatalf("writer produced a record the reader rejects: %v", rerr)
		}
		t.Fatal("oversized record was written")
	}
	// A refused write leaves nothing behind that the reader would choke on.
	if _, err := ReadBuildImageRecord(dir); err == nil {
		t.Fatal("record exists after refused write")
	}
}

// The lock file is handed to the directory's owner like the record (sudo
// enable followed by the server account's disable).
func TestLockBuildImageRecordMatchesDirOwner(t *testing.T) {
	dir := t.TempDir()
	var gotPath, gotDir string
	saved := matchLockOwner
	matchLockOwner = func(f *os.File, d string) error { gotPath, gotDir = f.Name(), d; return nil }
	defer func() { matchLockOwner = saved }()
	unlock, err := LockBuildImageRecord(dir)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	if gotPath != filepath.Join(dir, BuildImageRecordLockFile) || gotDir != dir {
		t.Fatalf("owner matched for %q in %q", gotPath, gotDir)
	}
}

// Codex #4685 round 3: a lock path planted as a symlink must not be followed,
// so a root `page disable` can never hand the symlink's target to the
// directory owner.
func TestLockBuildImageRecordRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "shadow")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, BuildImageRecordLockFile)); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	called := false
	saved := matchLockOwner
	matchLockOwner = func(*os.File, string) error { called = true; return nil }
	defer func() { matchLockOwner = saved }()
	if unlock, err := LockBuildImageRecord(dir); err == nil {
		unlock()
		t.Fatal("locked through a symlink")
	}
	if called {
		t.Fatal("owner matching ran on a symlinked lock path")
	}
}
