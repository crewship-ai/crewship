package backup_test

import (
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"

	"filippo.io/age"
	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/quiesce"
)

func TestInstanceBackupDiscardsCopyAfterWriterOwnerLoss(t *testing.T) {
	f := newInstanceFixture(t)
	ctrl := quiesce.New()
	var lost atomic.Bool
	if err := ctrl.SetWriterOwner(func() error {
		if lost.Load() {
			return errors.New("source lease closed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	w, err := ctrl.Begin(t.Context(), quiesce.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Release()
	packed := false
	_, err = backup.CreateInstanceBackup(t.Context(), f.db, backup.InstanceOptions{
		OutputDir: f.outputDir, Actor: backup.Actor{UserID: "u_admin"}, Recipients: []age.Recipient{f.identity.Recipient()},
		Paths: f.paths, Window: w, Progress: func(phase string) {
			if phase == "copy" {
				lost.Store(true)
			}
			if phase == "pack" {
				packed = true
			}
		},
	})
	if !errors.Is(err, quiesce.ErrWriterOwnerLost) {
		t.Fatalf("backup = %v, want lost ownership", err)
	}
	if packed || ctrl.Holding() {
		t.Fatal("invalid copy was packed or kept the window held")
	}
	bundles, err := filepath.Glob(filepath.Join(f.outputDir, "*.tar.zst"))
	if err != nil || len(bundles) != 0 {
		t.Fatalf("invalid copy published: %v %v", bundles, err)
	}
}

// A caller-supplied window (the API bridge) is checked again before the copy
// starts: ownership lost after Begin must stop the backup before any copying.
func TestInstanceBackupRefusesToCopyWithoutWriterOwner(t *testing.T) {
	f := newInstanceFixture(t)
	ctrl := quiesce.New()
	var lost atomic.Bool
	if err := ctrl.SetWriterOwner(func() error {
		if lost.Load() {
			return errors.New("source lease closed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	w, err := ctrl.Begin(t.Context(), quiesce.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Release()
	lost.Store(true)
	copied := false
	_, err = backup.CreateInstanceBackup(t.Context(), f.db, backup.InstanceOptions{
		OutputDir: f.outputDir, Actor: backup.Actor{UserID: "u_admin"}, Recipients: []age.Recipient{f.identity.Recipient()},
		Paths: f.paths, Window: w, Progress: func(phase string) {
			if phase == "copy" {
				copied = true
			}
		},
	})
	if !errors.Is(err, quiesce.ErrWriterOwnerLost) {
		t.Fatalf("backup = %v, want lost ownership", err)
	}
	if copied || ctrl.Holding() {
		t.Fatalf("copied=%v holding=%v after ownership was lost before the copy", copied, ctrl.Holding())
	}
}
