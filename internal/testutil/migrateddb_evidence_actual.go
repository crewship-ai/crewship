//go:build linux && mooring_rust_integration

package testutil

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
)

func actualEvidenceEnabled() bool { return os.Getenv("MOORING_SESSION_OUTPUT_DIR") != "" }
func actualEvidenceRoot() (string, error) {
	root := os.Getenv("MOORING_SESSION_OUTPUT_DIR")
	info, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(root) || !info.IsDir() || info.Mode().Perm() != 0o700 || !fixtureRootOwned(info) {
		return "", fmt.Errorf("actual DB evidence requires an owned private directory")
	}
	return root, nil
}
func newActualEvidenceDBDir() (string, error) {
	root, err := actualEvidenceRoot()
	if err != nil {
		return "", err
	}
	return os.MkdirTemp(root, "database-")
}
func actualOwnedDB(path string) bool {
	root, err := actualEvidenceRoot()
	if err != nil {
		return false
	}
	// Each DB belongs directly to a unique directory allocated by this harness.
	return filepath.Dir(filepath.Dir(path)) == root
}
func retainActualFailure(t testing.TB, dir string) bool {
	return t.Failed() && actualOwnedDB(filepath.Join(dir, "test.db"))
}
func snapshotActualFailure(t testing.TB, path string) {
	if !t.Failed() || !actualOwnedDB(path) {
		return
	}
	// The original fixture may have reopened and closed the first handle. A
	// separate read-only connection snapshots committed WAL consistently. Never
	// copy a live DB/WAL pair and never remove the original failed fixture.
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Errorf("failure evidence: open DB: %v", err)
		return
	}
	defer db.Close()
	SnapshotActualFailure(t, db, filepath.Join(filepath.Dir(path), "after-close.sqlite"))
}

// SnapshotActualFailure is an explicit pre-close seam for shared actual
// fixtures. Cleanup still retains the original DB and an after-close snapshot;
// neither snapshot claims to capture the instant a concurrent failure occurred.
func SnapshotActualFailure(t testing.TB, db *sql.DB, destination string) {
	t.Helper()
	if !t.Failed() || !actualEvidenceEnabled() {
		return
	}
	if !actualOwnedDB(destination) {
		t.Error("failure evidence: unowned snapshot destination")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := database.SnapshotTo(ctx, db, destination); err != nil {
		t.Errorf("failure evidence: consistent snapshot failed: %v", err)
		return
	}
	if err := os.Chmod(destination, 0o600); err != nil {
		t.Errorf("failure evidence: snapshot permissions: %v", err)
	}
}

func secureActualDB(path string) error {
	if actualOwnedDB(path) {
		return os.Chmod(path, 0o600)
	}
	return nil
}

// ActualEvidenceDBPath gives FULL-durability fixtures the same ownership and
// retention rule as MigratedDB without changing their database.Open options.
func ActualEvidenceDBPath(t testing.TB, original string) string {
	t.Helper()
	if !actualEvidenceEnabled() {
		return original
	}
	dir, err := newActualEvidenceDBDir()
	if err != nil {
		t.Fatal(err)
	}
	registerTestDirCleanup(t, dir)
	path := filepath.Join(dir, filepath.Base(original))
	t.Cleanup(func() { snapshotActualFailure(t, path) })
	return path
}
