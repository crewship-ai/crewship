package server

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/fileserver"
	"github.com/crewship-ai/crewship/internal/journal"

	_ "modernc.org/sqlite"
)

// newTestDB spins up an in-memory SQLite + a `crews` table with one row
// so emitFileWrittenEntry's workspace lookup succeeds. Mirrors the
// schema columns the real query touches; intentionally sparse — we
// don't need migrations here.
func newTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE crews (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id) VALUES (?, ?)`, "crew-1", "ws-1"); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	return db, func() { db.Close() }
}

// To avoid pulling the full server bootstrap into a unit test, we test
// the projection logic via a small wrapper that mirrors what
// emitFileWrittenEntry does sans the Writer-specific DB() call. The
// real code path stays exercised end-to-end in the dev VM smoke; this
// suite covers shape stability.

func TestSummarizeFileEvent(t *testing.T) {
	tests := []struct {
		verb, path string
		size       int64
		want       string
	}{
		{"created", "filip/note.md", 15, "created filip/note.md (15 B)"},
		{"wrote", "filip/data.csv", 2048, "wrote filip/data.csv (2 KB)"},
		{"deleted", "filip/.tmp.x", 0, "deleted filip/.tmp.x "},
		{"wrote", strings.Repeat("a", 200), 1, "wrote …" + strings.Repeat("a", 79) + " (1 B)"},
	}
	for _, tc := range tests {
		name := tc.verb + "/" + tc.path
		if len(name) > 30 {
			name = name[:30]
		}
		t.Run(name, func(t *testing.T) {
			got := summarizeFileEvent(tc.verb, tc.path, tc.size)
			if got != tc.want {
				t.Errorf("got %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestFormatSizeBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, ""},
		{-5, ""},
		{1, "(1 B)"},
		{1023, "(1023 B)"},
		{1024, "(1 KB)"},
		{1536, "(1.5 KB)"},
		{1024 * 1024, "(1 MB)"},
		{int64(2.5 * 1024 * 1024), "(2.5 MB)"},
	}
	for _, c := range cases {
		if got := formatSizeBytes(c.in); got != c.want {
			t.Errorf("formatSizeBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestEmitFileWrittenEntry_Projection(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()

	// Use the real journal.Writer so the DB() + LookupWorkspaceForCrew
	// path is exercised. Tiny queue + immediate flush keeps the test
	// snappy without raciness.
	w := journal.NewWriter(db, slog.Default(), journal.WriterOptions{
		QueueSize: 4, FlushSize: 1, FlushInterval: 5_000_000, // 5ms
	})
	defer w.Close()

	// Seed a journal_entries table the writer needs to actually persist.
	// The shape mirrors migrate_consts; we only need columns the writer
	// touches in its INSERT.
	mustExec(t, db, `CREATE TABLE journal_entries (
		id TEXT PRIMARY KEY,
		workspace_id TEXT NOT NULL,
		crew_id TEXT,
		agent_id TEXT,
		mission_id TEXT,
		ts TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
		entry_type TEXT NOT NULL,
		severity TEXT NOT NULL DEFAULT 'info',
		actor_type TEXT NOT NULL,
		actor_id TEXT,
		summary TEXT NOT NULL,
		payload TEXT NOT NULL DEFAULT '{}',
		refs TEXT NOT NULL DEFAULT '{}',
		trace_id TEXT,
		span_id TEXT,
		expires_at TEXT,
		priority TEXT NOT NULL DEFAULT 'normal'
	)`)

	emitFileWrittenEntry(w, "crew-1", fileserver.FileEvent{
		Event: "file_created",
		Path:  "filip/note.md",
		Agent: "filip",
		Size:  15,
	}, slog.Default())

	if err := w.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	var (
		count    int
		summary  string
		entryT   string
		actorTyp string
		actorID  string
	)
	mustQueryRow(t, db, `SELECT count(*) FROM journal_entries`).Scan(&count)
	if count != 1 {
		t.Fatalf("expected 1 row written, got %d", count)
	}
	mustQueryRow(t, db, `SELECT summary, entry_type, actor_type, COALESCE(actor_id,'') FROM journal_entries LIMIT 1`).Scan(&summary, &entryT, &actorTyp, &actorID)
	if entryT != string(journal.EntryFileWritten) {
		t.Errorf("entry_type = %q, want %q", entryT, journal.EntryFileWritten)
	}
	if actorTyp != string(journal.ActorAgent) {
		t.Errorf("actor_type = %q, want agent", actorTyp)
	}
	if actorID != "filip" {
		t.Errorf("actor_id = %q, want filip", actorID)
	}
	if !strings.Contains(summary, "created filip/note.md") {
		t.Errorf("summary missing verb+path: %q", summary)
	}
}

func TestEmitFileWrittenEntry_UnknownOpSkipped(t *testing.T) {
	// Ops that toFileEvent should never produce — verify defensive skip.
	w := journal.NewWriter(nil, slog.Default(), journal.WriterOptions{})
	defer w.Close()
	// Should not panic on nil DB because the unknown-op branch returns
	// before any DB access.
	emitFileWrittenEntry(w, "crew-1", fileserver.FileEvent{
		Event: "file_chmoded", // not in the switch
		Path:  "filip/x",
		Agent: "filip",
	}, slog.Default())
}

func TestEmitFileWrittenEntry_NilJournal(t *testing.T) {
	// nil writer is the "journal not yet wired" case (the closure in
	// server.go fires before the journal pointer is stored). MUST be a
	// silent no-op — panicking would crash the fsnotify goroutine.
	emitFileWrittenEntry(nil, "crew-1", fileserver.FileEvent{
		Event: "file_created",
		Path:  "filip/x",
		Agent: "filip",
	}, slog.Default())
}

// mustExec lives in routes_more_test.go; we just reuse it.

func mustQueryRow(t *testing.T, db *sql.DB, q string, args ...any) *sql.Row {
	t.Helper()
	return db.QueryRow(q, args...)
}
