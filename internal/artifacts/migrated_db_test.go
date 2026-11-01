package artifacts

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// durableMigratedDB copies the shared migrated template and opens it with the
// production pragmas. Artifact versions refuse anything below FULL synchronous
// durability, which testutil.MigratedDB relaxes for speed; migrating a fresh
// database per test instead took the package past the 35m race bound (#4711).
func durableMigratedDB(t *testing.T) *database.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "artifacts.db")
	src, err := os.Open(testutil.MigratedTemplatePath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.Copy(dst, src); err != nil {
		_ = dst.Close()
		t.Fatal(err)
	}
	if err = dst.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
