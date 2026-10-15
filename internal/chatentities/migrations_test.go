package chatentities

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
)

// Exercise the real migration graph: trigger column names and dependencies
// must exist in a fresh product database, not merely in a reduced fixture.
func TestMigrationsOnProductDatabase(t *testing.T) {
	db, e := database.Open(filepath.Join(t.TempDir(), "product.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if e = database.Migrate(context.Background(), db.DB, slog.New(slog.NewTextHandler(io.Discard, nil))); e != nil {
		t.Fatal(e)
	}
	var r, a int64
	if e = db.QueryRow(`SELECT COUNT(revision),COUNT(aggregate_revision) FROM missions`).Scan(&r, &a); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`SELECT event_id FROM chat_entity_refs LIMIT 1`); e != nil {
		t.Fatal(e)
	}
}
