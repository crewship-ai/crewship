package main

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/crashreport"
	"github.com/crewship-ai/crewship/internal/database"
)

// TestSetOptIn_FreshDB_DoesNotPanic guards the bug found in critical
// review of feat/beta-release-infrastructure: openLocalDB previously
// skipped Migrate, so `crewship telemetry on` before the first
// `crewship start` crashed with "no such table: app_settings".
//
// We can't call the real openLocalDB() from a test because it resolves
// the data dir under the user's HOME — but we can exercise the exact
// invariant that fix relies on: Open + Migrate + SetOptIn on a
// previously-unused DB file must succeed.
func TestSetOptIn_FreshDB_DoesNotPanic(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fresh.db")
	db, err := database.Open("file:" + dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := database.Migrate(context.Background(), db.DB, silent); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	on, id, err := crashreport.SetOptIn(context.Background(), db.DB, true)
	if err != nil {
		t.Fatalf("SetOptIn on fresh+migrated DB: %v", err)
	}
	if !on || id == "" {
		t.Errorf("expected enabled=true with non-empty install ID, got %v %q", on, id)
	}
}
