//go:build linux

package database

import (
	"bytes"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestRestoreWriteFailureLeavesOnlyOriginalFiles(t *testing.T) {
	// The limit belongs only to this serial test process and is restored before
	// assertions, test cleanup or coverage output. No shared volume is filled.
	var original syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &original); err != nil {
		t.Fatal(err)
	}
	signal.Ignore(syscall.SIGXFSZ)
	defer signal.Reset(syscall.SIGXFSZ)
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "staging", true: "rollback copy"}[existing], func(t *testing.T) {
			dir := t.TempDir()
			live := filepath.Join(dir, "crewship.db")
			snapshot := live + ".pre-migrate-v1-to-v2-20261003T000000Z.bak"
			source := migrationBoundaryDB(t, `CREATE TABLE preserved(value TEXT);INSERT INTO preserved VALUES('snapshot')`)
			var sourcePath string
			var seq int
			var schema string
			if err := source.QueryRow(`PRAGMA database_list`).Scan(&seq, &schema, &sourcePath); err != nil {
				t.Fatal(err)
			}
			if err := source.Close(); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(sourcePath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(snapshot, data, 0o600); err != nil {
				t.Fatal(err)
			}
			before := []byte("live database must survive refused backup")
			if existing {
				if err := os.WriteFile(live, before, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			failure := func() error {
				limited := original
				limited.Cur = 0
				if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limited); err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &original); err != nil {
						t.Errorf("restore process file-size limit: %v", err)
					}
				}()
				return RestoreSnapshot(live, snapshot)
			}()
			if !errors.Is(failure, syscall.EFBIG) {
				t.Fatalf("failed write was not reported: %v", failure)
			}
			want := "stage restored db"
			if existing {
				want = "stash current db"
			}
			if !strings.Contains(failure.Error(), want) {
				t.Fatalf("wrong failure stage: %v", failure)
			}
			after, err := os.ReadFile(live)
			if existing {
				if err != nil || !bytes.Equal(after, before) {
					t.Fatalf("live file changed: %v", err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("partial live file remains: %v", err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if entry.Name() != filepath.Base(live) && entry.Name() != filepath.Base(snapshot) {
					t.Fatalf("partial restore artifact survived: %s", entry.Name())
				}
			}
		})
	}
}

func TestRestoreTempRejectsUnusableParent(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(parent, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := writeRestoreTemp(parent, "restore-*", []byte("replacement")); err == nil {
		t.Fatal("file parent accepted")
	}
	got, err := os.ReadFile(parent)
	if err != nil || string(got) != "preserve" {
		t.Fatalf("parent changed: %q %v", got, err)
	}
}
