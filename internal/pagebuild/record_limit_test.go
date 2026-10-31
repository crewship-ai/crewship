package pagebuild

import (
	"fmt"
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
	matchLockOwner = func(path, d string) error { gotPath, gotDir = path, d; return nil }
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
