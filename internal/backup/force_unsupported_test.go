//go:build !linux

package backup_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/writerlease"
)

func TestForceRecoveryUnsupportedLeavesInstallationIntact(t *testing.T) {
	f := newInstanceFixture(t)
	bundle := f.create(t, true)
	parent := t.TempDir()
	target := filepath.Join(parent, "installation")
	opts := backup.RecoverOptions{BundlePath: bundle.Path, Identities: []age.Identity{f.identity}, DataDir: target}
	if _, err := backup.RecoverInstance(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "operator.txt"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	before := forceGenerationSnapshot(t, parent)
	opts.Force = true
	report, err := backup.RecoverInstance(t.Context(), opts)
	if !errors.Is(err, writerlease.ErrUnsupportedStorage) {
		t.Fatalf("forced recovery returned %v, want unsupported storage", err)
	}
	if report == nil || report.Result != backup.RestoreResultFailed || report.PreviousDataDir != "" {
		t.Fatalf("unexpected refusal report: %+v", report)
	}
	assertOriginalGeneration(t, parent, before)
}
