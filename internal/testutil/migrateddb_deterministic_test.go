package testutil

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/testutil/migratedtemplate"
)

// Every fixture path (the normal helper image, the in-process template and
// each private copy) is a byte copy of one full Migrate run, while production
// runs Migrate on its own database. Both are only equivalent when the
// migration chain itself is deterministic: a migration that writes a random
// or per-installation value would make every test copy share one value and
// make fresh images differ (#5163, document_database_identity). Values that
// must be unique per installation are created on first use, not by a
// migration. Only the enumerated migration clock columns are normalized.
func TestFreshMigrationChainIsDeterministic(t *testing.T) {
	var snapshots []map[string][]string
	for _, name := range []string{"first.db", "second.db"} {
		path := filepath.Join(t.TempDir(), name)
		if err := migratedtemplate.Build(t.Context(), path); err != nil {
			t.Fatal(err)
		}
		db, err := database.Open("file:" + path)
		if err != nil {
			t.Fatal(err)
		}
		snapshots = append(snapshots, snapshotFixtureRows(t, db.DB))
		_ = db.Close()
	}
	if !reflect.DeepEqual(snapshots[0], snapshots[1]) {
		for table, rows := range snapshots[0] {
			if !reflect.DeepEqual(rows, snapshots[1][table]) {
				t.Errorf("two full migration runs differ in table %s (first=%d second=%d)", table, len(rows), len(snapshots[1][table]))
			}
		}
	}
}
