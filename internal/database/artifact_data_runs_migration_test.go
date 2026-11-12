package database

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

// A late integration must advance the schema beyond already shipped main.
// Inserting this migration before main's last version would also leave an old
// binary's version-skew guard unable to distinguish the new schema by its max.
func TestArtifactDataRunsMigrationUpgradesShippedMain(t *testing.T) {
	const shippedMainVersion = 20261111104000 // 7c8d0bef, pipeline_execution_agent_wait
	var producerVersion int
	for _, m := range migrations {
		if m.name == "artifact_data_runs" {
			producerVersion = m.version
		}
	}
	if producerVersion <= shippedMainVersion {
		t.Fatalf("producer migration v%d must append after shipped main v%d", producerVersion, shippedMainVersion)
	}

	db, err := Open(filepath.Join(t.TempDir(), "producer-upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := MigrateBefore(ctx, db.DB, logger, "artifact_data_runs"); err != nil {
		t.Fatal(err)
	}
	var before int
	if err := db.QueryRow(`SELECT MAX(version) FROM _migrations`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before != shippedMainVersion {
		t.Fatalf("upgrade fixture is at v%d; want shipped main v%d", before, shippedMainVersion)
	}
	var tables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name='artifact_data_runs'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 0 {
		t.Fatal("producer table exists before its migration")
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := Migrate(ctx, db.DB, logger); err != nil {
			t.Fatalf("migration attempt %d: %v", attempt+1, err)
		}
	}
	var applied, version int
	if err := db.QueryRow(`SELECT COUNT(*), MAX(version) FROM _migrations WHERE name='artifact_data_runs'`).Scan(&applied, &version); err != nil {
		t.Fatal(err)
	}
	if applied != 1 || version != producerVersion {
		t.Fatalf("producer migration ledger: count=%d version=%d; want one v%d", applied, version, producerVersion)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM artifact_data_runs`).Scan(&tables); err != nil {
		t.Fatalf("upgraded producer table unavailable: %v", err)
	}
}
