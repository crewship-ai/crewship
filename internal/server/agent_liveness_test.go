package server

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	_ "modernc.org/sqlite"
)

func livenessDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE agents(id TEXT PRIMARY KEY, slug TEXT, deleted_at TEXT);
INSERT INTO agents VALUES('live','live',NULL),('gone','gone','2026-10-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestAgentMayRun(t *testing.T) {
	db := livenessDB(t)
	if err := agentMayRun(context.Background(), db, "live"); err != nil {
		t.Fatalf("live agent refused: %v", err)
	}
	if err := agentMayRun(context.Background(), db, "gone"); !errors.Is(err, errAgentDeleted) {
		t.Fatalf("deleted agent: %v", err)
	}
	// A missing row refuses too: starting a process is the irreversible step.
	if err := agentMayRun(context.Background(), db, "absent"); err == nil {
		t.Fatal("missing agent admitted")
	}
}

func TestDeletedAgentLookup(t *testing.T) {
	lookup := deletedAgentLookup(livenessDB(t))
	for id, want := range map[string]bool{"live": false, "gone": true, "absent": true} {
		deleted, _, err := lookup(context.Background(), id)
		if err != nil || deleted != want {
			t.Errorf("%s: deleted=%v err=%v", id, deleted, err)
		}
	}
	if _, slug, _ := lookup(context.Background(), "gone"); slug != "gone" {
		t.Errorf("slug fallback %q", slug)
	}
}
