package backup

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
)

// migratedForTemplates opens a database with the REAL migrations applied.
// The discovery tests in this package hand-roll a miniature schema, which
// cannot answer whether a table added by a migration is reachable.
func migratedForTemplates(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/discover.db?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.Migrate(context.Background(), db,
		slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// notification_templates must be wiped by a --replace restore.
//
// Its neighbours in BackupTableIntent carry a plain workspace_id with no
// foreign key, so the reverse-FK walk from `workspaces` never surfaces them
// and they are dumped only via their explicit BackupTables entries. This one
// declares `workspace_id ... REFERENCES workspaces(id)`, so the walk DOES
// reach it — but that is a property of the schema, not of the comment block it
// happens to sit next to, and a review reading that block would reasonably
// conclude the opposite.
//
// A template that survived a replace would be worse than one that vanished:
// the restored workspace would deliver its notifications in wording from the
// instance it replaced, and nothing would say so.

func TestReplace_DiscoversNotificationTemplates(t *testing.T) {
	db := migratedForTemplates(t)

	scoped, err := DiscoverScopedTables(context.Background(), db)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	var found bool
	for _, s := range scoped {
		if s.Name == "notification_templates" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("notification_templates is not reachable from workspaces by the FK walk, " +
			"so a --replace restore would leave the previous instance's wording behind")
	}

	// And being discovered is only half of it — intent decides whether the
	// discovered table is actually cleared.
	include, _, err := CategoriseScopedTables(scoped, BackupTableIntent)
	if err != nil {
		t.Fatalf("categorise: %v", err)
	}
	for _, s := range include {
		if s.Name == "notification_templates" {
			return
		}
	}
	t.Error("notification_templates is discovered but not in the include set, so replace would skip it")
}
