package database

import (
	"path/filepath"
	"testing"
)

func TestManagedWALRejectsSecondServerWriter(t *testing.T) {
	path := "file:" + filepath.Join(t.TempDir(), "writer.db")
	first, err := Open(path, WithManagedWAL())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(path, WithManagedWAL())
	if second != nil {
		second.Close()
	}
	if err == nil {
		t.Fatal("second server writer opened the same database")
	}
}
