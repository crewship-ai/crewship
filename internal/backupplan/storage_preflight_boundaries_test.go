package backupplan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/backup/offsite"
)

func TestBackupDiskPreflightUsesExistingParentForNewDirectory(t *testing.T) {
	root := t.TempDir()
	free, total, err := diskSpace(filepath.Join(root, "not-created", "backups"))
	if err != nil || total == 0 || free > total {
		t.Fatalf("new backup directory has no disk measurement: %d %d %v", free, total, err)
	}
	_, parentTotal, err := diskSpace(root)
	if err != nil || parentTotal != total {
		t.Fatalf("wrong parent filesystem: %d %d %v", total, parentTotal, err)
	}
	if _, _, err := diskSpace(""); err == nil {
		t.Fatal("empty storage path accepted")
	}
	db := busyFixtureDB(t)
	service := New(db, nil)
	t.Setenv("CREWSHIP_DATA_DIR", root)
	if got := service.backupsDir(); got != filepath.Join(root, "backups") {
		t.Fatalf("default backup path escaped the instance directory: %s", got)
	}
	before := time.Now()
	now := service.now()
	if now.Before(before) || now.After(time.Now()) || now.Location() != time.UTC {
		t.Fatalf("default clock not current UTC: %s", now)
	}
	service.Concurrency = func(context.Context) int { return 0 }
	if got := service.concurrency(t.Context()); got != DefaultConcurrency {
		t.Fatalf("nonpositive concurrency disabled backups: %d", got)
	}
	service.BackupsDir = func() (string, error) { return "", errors.New("storage unavailable") }
	if got := service.backupsDir(); got != "" {
		t.Fatalf("invented unavailable directory: %s", got)
	}
}

func TestOffsiteFetchRefusesUnavailableStorageAndPreservesExistingFiles(t *testing.T) {
	ctx := t.Context()
	db := busyFixtureDB(t)
	dst := newMemDest()
	open := func(context.Context, string) (offsite.Destination, *Destination, error) {
		return dst, &Destination{ID: "dest"}, nil
	}
	sentinel := errors.New("vault unavailable")
	denied := func(context.Context, string) (offsite.Destination, *Destination, error) { return nil, nil, sentinel }
	if path, _, err := FetchOffsiteCopy(ctx, db, denied, "dest", "instance/archive.tar.zst", t.TempDir()); !errors.Is(err, sentinel) || path != "" {
		t.Fatalf("failed destination yielded path: %s %v", path, err)
	}
	if path, _, err := FetchOffsiteCopy(ctx, db, nil, "missing", "instance/archive.tar.zst", t.TempDir()); err == nil || path != "" {
		t.Fatalf("absent destination yielded path: %s %v", path, err)
	}
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, []byte("owned"), 0600); err != nil {
		t.Fatal(err)
	}
	if path, _, err := FetchOffsiteCopy(ctx, db, open, "dest", "instance/archive.tar.zst", filepath.Join(blocked, "child")); err == nil || path != "" {
		t.Fatalf("invalid directory accepted: %s %v", path, err)
	}
	directory := t.TempDir()
	existing := filepath.Join(directory, "archive.tar.zst")
	if err := os.WriteFile(existing, []byte("keep my backup"), 0600); err != nil {
		t.Fatal(err)
	}
	if path, _, err := FetchOffsiteCopy(ctx, db, open, "dest", "instance/archive.tar.zst", directory); err == nil || path != "" {
		t.Fatalf("overwrote existing backup: %s %v", path, err)
	}
	data, err := os.ReadFile(existing)
	if err != nil || string(data) != "keep my backup" {
		t.Fatalf("existing backup changed: %q %v", data, err)
	}
	if path, _, err := FetchOffsiteCopy(ctx, db, open, "dest", "instance/missing.tar.zst", directory); err == nil || path != "" {
		t.Fatalf("missing remote object became local receipt: %s %v", path, err)
	}
	if _, err := os.Stat(filepath.Join(directory, "missing.tar.zst")); !os.IsNotExist(err) {
		t.Fatalf("failed download left an apparent backup: %v", err)
	}
}
