package featureflags

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// schemaSQL mirrors the feature_flags/feature_flag_overrides tables from
// internal/database/migrate_consts_v01_init.go. Self-contained in-memory
// schema, same convention as internal/journal's openTestDB.
const schemaSQL = `
CREATE TABLE workspaces (id TEXT PRIMARY KEY);
INSERT INTO workspaces (id) VALUES ('ws_a'), ('ws_b');

CREATE TABLE feature_flags (
	id TEXT PRIMARY KEY,
	key TEXT NOT NULL UNIQUE,
	description TEXT,
	enabled INTEGER NOT NULL DEFAULT 0,
	percentage INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE feature_flag_overrides (
	id TEXT PRIMARY KEY,
	flag_id TEXT NOT NULL REFERENCES feature_flags(id) ON DELETE CASCADE,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	enabled INTEGER NOT NULL,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	UNIQUE(flag_id, workspace_id)
);
`

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), schemaSQL); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

func insertFlag(t *testing.T, db *sql.DB, id, key string, enabled bool) {
	t.Helper()
	enabledInt := 0
	if enabled {
		enabledInt = 1
	}
	if _, err := db.Exec(
		`INSERT INTO feature_flags (id, key, enabled) VALUES (?, ?, ?)`,
		id, key, enabledInt,
	); err != nil {
		t.Fatalf("insert flag: %v", err)
	}
}

func insertOverride(t *testing.T, db *sql.DB, flagID, workspaceID string, enabled bool) {
	t.Helper()
	enabledInt := 0
	if enabled {
		enabledInt = 1
	}
	if _, err := db.Exec(
		`INSERT INTO feature_flag_overrides (id, flag_id, workspace_id, enabled) VALUES (?, ?, ?, ?)`,
		"ov_"+flagID+"_"+workspaceID, flagID, workspaceID, enabledInt,
	); err != nil {
		t.Fatalf("insert override: %v", err)
	}
}

func TestIsEnabled_InstanceDefaultNoOverride(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	insertFlag(t, db, "flag_1", "run_verdict_summaries", true)

	got, err := IsEnabled(context.Background(), db, "ws_a", "run_verdict_summaries")
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Errorf("got false, want true (instance default)")
	}
}

func TestIsEnabled_OverrideWinsOverInstanceDefault(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	insertFlag(t, db, "flag_1", "run_verdict_summaries", true)
	insertOverride(t, db, "flag_1", "ws_a", false)

	got, err := IsEnabled(context.Background(), db, "ws_a", "run_verdict_summaries")
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Errorf("got true, want false (workspace override disables it)")
	}

	// A different workspace with no override still sees the instance default.
	gotOther, err := IsEnabled(context.Background(), db, "ws_b", "run_verdict_summaries")
	if err != nil {
		t.Fatal(err)
	}
	if !gotOther {
		t.Errorf("ws_b: got false, want true (no override, inherits instance default)")
	}
}

func TestIsEnabled_OverrideEnablesWhenInstanceDefaultOff(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	insertFlag(t, db, "flag_1", "run_verdict_summaries", false)
	insertOverride(t, db, "flag_1", "ws_a", true)

	got, err := IsEnabled(context.Background(), db, "ws_a", "run_verdict_summaries")
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Errorf("got false, want true (workspace override enables it)")
	}
}

func TestIsEnabled_UndefinedKeyIsFalse(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	got, err := IsEnabled(context.Background(), db, "ws_a", "no_such_flag")
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Errorf("got true, want false (undefined flag key)")
	}
}
