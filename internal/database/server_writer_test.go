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

func TestManagedWALCloseKeepsOwnershipOfDedicatedConnections(t *testing.T) {
	path := "file:" + filepath.Join(t.TempDir(), "dedicated.db")
	first, err := Open(path, WithManagedWAL())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	conn, err := first.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = first.Close()
	second, err := Open(path, WithManagedWAL())
	if second != nil {
		second.Close()
	}
	if err == nil {
		t.Fatal("ownership released while a dedicated SQLite connection can still write")
	}
	if _, err = conn.ExecContext(t.Context(), `CREATE TABLE still_writable(id INTEGER)`); err != nil {
		t.Fatal("test requires a still-live dedicated connection", err)
	}
	if err = conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := Open(path, WithManagedWAL())
	if err != nil {
		t.Fatal(err)
	}
	next.Close()
}
