//go:build unix

package consolidate

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/memory"
)

func TestRecordCanonicalVersionRefusesSymlinkedSource(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	if _, err := db.Exec(memoryVersionsSchema); err != nil {
		t.Fatalf("schema: %v", err)
	}

	victim := filepath.Join(t.TempDir(), "outside-secret.md")
	if err := os.WriteFile(victim, []byte("outside secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	canonical := filepath.Join(t.TempDir(), "pins.md")
	if err := os.Symlink(victim, canonical); err != nil {
		t.Fatalf("plant canonical symlink: %v", err)
	}

	c := &Consolidator{DB: db, Logger: quietLogger()}
	c.recordCanonicalVersion(context.Background(), Config{
		WorkspaceID: "ws_test",
		BlobRoot:    t.TempDir(),
	}, canonical, memory.TierPins, "crew:crew_test/pins.md")

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM memory_versions`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("symlinked canonical source created %d version row(s), want 0", count)
	}
}
