package database

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

// TestChatOriginRoutineBackfill — the upgrade path for the conversations
// column's new mode+origin partition.
//
// A NULL origin classifies as `direct`, deliberately: an origin nobody has
// thought of yet must leave its row VISIBLE rather than hiding it. That
// default is right for every future row and wrong for exactly one population —
// every routine step ever run before the runner learned to stamp an origin.
// Without this backfill an instance upgrades, is told its conversations are
// separated now, and sees the same mixed list it had yesterday.
//
// The two guards are the point of the test. The title match alone would be a
// heuristic over a user-editable column; paired with `created_by IS NULL` it
// can only fire on a row that is BOTH system-created and titled in the exact
// shape `fmt.Sprintf("Pipeline %s · step %s", ...)` produced.
func TestChatOriginRoutineBackfill(t *testing.T) {
	db := openChatOriginDB(t)

	// Seeded AFTER migration so the rows exist in their pre-backfill state,
	// then the statement is applied on its own. Running Migrate twice would
	// skip it — it is already in schema_migrations — which would test nothing.
	seedChat(t, db, "keep-human", "Pipeline notes · step two", strptr("user-1"), nil)
	seedChat(t, db, "keep-titled", "Deploy rollback", strptr("user-1"), nil)
	seedChat(t, db, "keep-stamped", "Pipeline x · step y", nil, strptr("AGENT"))
	seedChat(t, db, "keep-shapeless", "Some background job", nil, nil)
	seedChat(t, db, "hit-1", "Pipeline pln_cmtem1pwz000d3e744992 · step summarize", nil, nil)
	seedChat(t, db, "hit-2", "Pipeline summarize:grader · step summarize:grader", nil, nil)

	if _, err := db.Exec(migrationChatOriginRoutine); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	want := map[string]string{
		// A person's chat titled to look like a routine keeps its identity:
		// created_by is what makes the title match safe.
		"keep-human":  "",
		"keep-titled": "",
		// Already stamped — the write is `origin IS NULL`, so it never
		// overwrites provenance somebody else recorded.
		"keep-stamped": "AGENT",
		// System-created but not in the runner's shape. Left alone rather
		// than guessed at.
		"keep-shapeless": "",
		"hit-1":          "ROUTINE",
		"hit-2":          "ROUTINE",
	}
	for id, expect := range want {
		var got sql.NullString
		if err := db.QueryRow(`SELECT origin FROM chats WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if got.String != expect {
			t.Errorf("%s origin = %q, want %q", id, got.String, expect)
		}
	}
}

func TestChatOriginRoutineBackfillIsIdempotent(t *testing.T) {
	// `origin IS NULL` is consumed by the write, so a second application is a
	// no-op. Worth pinning: the restore path can replay a migration onto rows
	// that already have it.
	db := openChatOriginDB(t)
	seedChat(t, db, "hit", "Pipeline p · step s", nil, nil)

	for i := 0; i < 2; i++ {
		if _, err := db.Exec(migrationChatOriginRoutine); err != nil {
			t.Fatalf("pass %d: %v", i, err)
		}
	}
	var origin string
	if err := db.QueryRow(`SELECT origin FROM chats WHERE id = 'hit'`).Scan(&origin); err != nil {
		t.Fatal(err)
	}
	if origin != "ROUTINE" {
		t.Errorf("origin = %q, want ROUTINE", origin)
	}
}

func openChatOriginDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open("file:" + filepath.Join(t.TempDir(), "chat_origin.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(context.Background(), db.DB, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db.DB
}

func seedChat(t *testing.T, db *sql.DB, id, title string, createdBy, origin *string) {
	t.Helper()
	// The FK targets are minted lazily and shared: this test only cares about
	// chats, and every row hangs off one workspace and one agent. Errors are
	// checked rather than ignored — an INSERT OR IGNORE that fails on a
	// column name leaves the parent missing, and the FK violation then
	// surfaces on the chat insert as a mystery about chats.
	for _, stmt := range []string{
		`INSERT OR IGNORE INTO users (id, email, full_name) VALUES ('user-1', 'u@example.com', 'U')`,
		`INSERT OR IGNORE INTO workspaces (id, name, slug) VALUES ('ws-1', 'W', 'w')`,
		`INSERT OR IGNORE INTO crews (id, workspace_id, name, slug) VALUES ('crew-1', 'ws-1', 'C', 'c')`,
		`INSERT OR IGNORE INTO agents (id, workspace_id, crew_id, name, slug, agent_role)
		 VALUES ('ag-1', 'ws-1', 'crew-1', 'A', 'a', 'AGENT')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed parent: %v\n%s", err, stmt)
		}
	}
	if _, err := db.Exec(`INSERT INTO chats (id, agent_id, workspace_id, created_by, title, mode, status, origin)
		VALUES (?, 'ag-1', 'ws-1', ?, ?, 'CHAT', 'ACTIVE', ?)`, id, createdBy, title, origin); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

func strptr(s string) *string { return &s }
