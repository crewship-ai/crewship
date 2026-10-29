package mooring

import (
	"database/sql"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestCheckpointCreatorGenerationSurvivesTakeover(t *testing.T) {
	store, _, owners, clock, first := batchFixture(t)
	clock.advance(6 * time.Second)
	second, err := owners.Acquire(t.Context(), first.Key, "replacement")
	if err != nil {
		t.Fatal(err)
	}
	if second.Generation != first.Generation+1 {
		t.Fatal("owner generation did not advance")
	}
	change := testBatchChange(t, first.Key, "new-owner-change", "", testBatchOp("new-owner-op"))
	result, err := store.Commit(t.Context(), BatchRequest{second, "0", []BatchChange{change}})
	if err != nil || result.Revision != "1" {
		t.Fatal(result, err)
	}
	for revision, want := range map[string]int64{"0": first.Generation, "1": second.Generation} {
		var generation sql.NullString
		if err := store.db.QueryRow(`SELECT checkpoint_generation FROM mooring_collab_checkpoints WHERE page_id=? AND revision=?`, first.Key.ArtifactID, revision).Scan(&generation); err != nil {
			t.Fatal(err)
		}
		if !generation.Valid || generation.String != strconv.FormatInt(want, 10) {
			t.Fatalf("revision %s provenance=%v want %d", revision, generation, want)
		}
	}
	if _, err := store.db.Exec(`UPDATE mooring_collab_checkpoints SET checkpoint_generation=? WHERE page_id=? AND revision='0'`, strconv.FormatInt(second.Generation, 10), first.Key.ArtifactID); err == nil {
		t.Fatal("checkpoint creator provenance rewritten")
	}
	// Portable legacy records retain unknown provenance; they cannot be guessed
	// from whichever owner happens to be current at restore time.
	if _, err := store.db.Exec(`INSERT INTO mooring_collab_checkpoints(page_id,workspace_id,revision,checkpoint_base64,decoded_bytes) VALUES(?,?,'2','MA==',1)`, first.Key.ArtifactID, first.Key.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE mooring_collab_checkpoints SET checkpoint_generation='2' WHERE page_id=? AND revision='2'`, first.Key.ArtifactID); err == nil {
		t.Fatal("unknown legacy provenance backfilled without qualification")
	}
}

func TestCheckpointGenerationMigrationRetainsUnknownLegacy(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`CREATE TABLE mooring_collab_checkpoints(revision TEXT PRIMARY KEY); INSERT INTO mooring_collab_checkpoints VALUES('old')`); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../database/migrations/20261024022200_mooring_checkpoint_provenance.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	var legacy sql.NullString
	if err = db.QueryRow(`SELECT checkpoint_generation FROM mooring_collab_checkpoints WHERE revision='old'`).Scan(&legacy); err != nil || legacy.Valid {
		t.Fatal("legacy provenance guessed", legacy, err)
	}
	for i, value := range []any{nil, "1", "18446744073709551615"} {
		if _, err = db.Exec(`INSERT INTO mooring_collab_checkpoints VALUES(?,?)`, strconv.Itoa(i), value); err != nil {
			t.Fatal(value, err)
		}
	}
	for _, value := range []string{"0", "01", "-1", "1.0", "18446744073709551616", "x"} {
		if _, err = db.Exec(`INSERT INTO mooring_collab_checkpoints VALUES('invalid',?)`, value); err == nil {
			t.Fatalf("invalid provenance accepted: %q", value)
		}
	}
	if _, err = db.Exec(`UPDATE mooring_collab_checkpoints SET checkpoint_generation='1' WHERE revision='old'`); err == nil {
		t.Fatal("legacy provenance relabeled")
	}
}
