package backup_test

import (
	"archive/tar"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
)

type forceEntry struct {
	info     os.FileInfo
	uid, gid int
	hash     [sha256.Size]byte
}

func forceGenerationSnapshot(t *testing.T, root string) map[string]forceEntry {
	t.Helper()
	tree := make(map[string]forceEntry)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		entry := forceEntry{info: info, uid: header.Uid, gid: header.Gid}
		if info.Mode().IsRegular() {
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			entry.hash = sha256.Sum256(body)
		}
		tree[rel] = entry
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func assertOriginalGeneration(t *testing.T, dir string, before map[string]forceEntry) {
	t.Helper()
	after := forceGenerationSnapshot(t, dir)
	if len(after) != len(before) {
		t.Errorf("original entry count = %d, want %d", len(after), len(before))
	}
	for rel, old := range before {
		got, ok := after[rel]
		if !ok || !os.SameFile(old.info, got.info) || old.hash != got.hash ||
			old.info.Mode() != got.info.Mode() || old.uid != got.uid || old.gid != got.gid ||
			!old.info.ModTime().Equal(got.info.ModTime()) {
			t.Errorf("original entry changed: %s", rel)
		}
	}
}
