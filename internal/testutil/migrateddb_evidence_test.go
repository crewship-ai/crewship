//go:build linux && mooring_rust_integration

package testutil

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
)

// A failing subprocess exercises the real testing cleanup order. Its failure
// must survive as failure, while both before-close and final SQL remain readable.
func TestActualFailureDBEvidenceSurvivesCleanup(t *testing.T) {
	if os.Getenv("MOORING_EVIDENCE_RED_CHILD") == "1" {
		fullPath := ActualEvidenceDBPath(t, filepath.Join(t.TempDir(), "full.db"))
		if err := copyFileForTest(MigratedTemplatePath(t), fullPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(fullPath, 0o600); err != nil {
			t.Fatal(err)
		}
		full, err := database.Open("file:" + fullPath)
		if err != nil {
			t.Fatal(err)
		}
		defer full.Close()
		if _, err = full.Exec("CREATE TABLE evidence_full(value INTEGER); INSERT INTO evidence_full VALUES(3)"); err != nil {
			t.Fatal(err)
		}
		db := MigratedDB(t)
		if _, err := db.DB.Exec("CREATE TABLE evidence_probe(value INTEGER); INSERT INTO evidence_probe VALUES(1)"); err != nil {
			t.Fatal(err)
		}
		defer func() { _, _ = db.DB.Exec("UPDATE evidence_probe SET value=2") }()
		defer func() { SnapshotActualFailure(t, db.DB, filepath.Join(filepath.Dir(db.Path()), "before-close.sqlite")) }()
		t.Fatal("synthetic original failure")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOORING_SESSION_OUTPUT_DIR", root)

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestActualFailureDBEvidenceSurvivesCleanup$", "-test.count=1")
	cmd.Env = append(os.Environ(), "MOORING_EVIDENCE_RED_CHILD=1")
	if output, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("red fixture unexpectedly passed: %s", output)
	}
	files, err := filepath.Glob(filepath.Join(root, "database-*", "test.db"))
	if err != nil || len(files) != 1 {
		t.Fatalf("original failed DB missing: %v %v", files, err)
	}
	fullFiles, err := filepath.Glob(filepath.Join(root, "database-*", "full.db"))
	if err != nil || len(fullFiles) != 1 {
		t.Fatalf("FULL fixture missing: %v %v", fullFiles, err)
	}
	for _, name := range []string{"full.db", "after-close.sqlite"} {
		full, err := sql.Open("sqlite", "file:"+filepath.Join(filepath.Dir(fullFiles[0]), name)+"?mode=ro")
		if err != nil {
			t.Fatal(err)
		}
		var value int
		err = full.QueryRow("SELECT value FROM evidence_full").Scan(&value)
		_ = full.Close()
		if err != nil || value != 3 {
			t.Fatalf("FULL evidence %s lost: %d %v", name, value, err)
		}
	}
	dir := filepath.Dir(files[0])
	for _, item := range []struct {
		name string
		want int
	}{{"test.db", 2}, {"before-close.sqlite", 1}, {"after-close.sqlite", 2}} {
		path := filepath.Join(dir, item.name)
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("private evidence missing %s: %v", item.name, err)
		}
		db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
		if err != nil {
			t.Fatal(err)
		}
		var got int
		err = db.QueryRowContext(context.Background(), "SELECT value FROM evidence_probe").Scan(&got)
		_ = db.Close()
		if err != nil || got != item.want {
			t.Fatalf("%s: value=%d want=%d err=%v", item.name, got, item.want, err)
		}
	}
}
