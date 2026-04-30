package journal

// FTS5 search tests for journal.List/Count. The base test schema in
// journal_test.go does not include the FTS5 virtual table because most
// tests don't exercise it; this file installs the FTS5 schema fixture
// alongside, so the JOIN path can be validated end-to-end.

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// ftsSchemaSQL mirrors migration 55's FTS5 setup so List(FTSQuery=...)
// has something to JOIN against. Kept identical to migrate.go so a
// regression in the live migration would surface here too.
const ftsSchemaSQL = `
CREATE VIRTUAL TABLE journal_entries_fts USING fts5(
    summary, payload,
    content='journal_entries',
    content_rowid='rowid',
    tokenize='porter ascii'
);
CREATE TRIGGER journal_entries_ai AFTER INSERT ON journal_entries BEGIN
    INSERT INTO journal_entries_fts(rowid, summary, payload) VALUES (new.rowid, new.summary, new.payload);
END;
CREATE TRIGGER journal_entries_ad AFTER DELETE ON journal_entries BEGIN
    INSERT INTO journal_entries_fts(journal_entries_fts, rowid, summary, payload) VALUES('delete', old.rowid, old.summary, old.payload);
END;
CREATE TRIGGER journal_entries_au AFTER UPDATE ON journal_entries BEGIN
    INSERT INTO journal_entries_fts(journal_entries_fts, rowid, summary, payload) VALUES('delete', old.rowid, old.summary, old.payload);
    INSERT INTO journal_entries_fts(rowid, summary, payload) VALUES (new.rowid, new.summary, new.payload);
END;
`

// openTestDBWithFTS extends openTestDB with the FTS5 virtual table so
// tests in this file can exercise the FTSQuery path. Returning the same
// *sql.DB type means callers can use the existing helpers (NewWriter,
// List, Count) without modification.
func openTestDBWithFTS(t *testing.T) *sql.DB {
	t.Helper()
	db := openTestDB(t)
	if _, err := db.ExecContext(context.Background(), ftsSchemaSQL); err != nil {
		t.Fatalf("fts schema: %v", err)
	}
	return db
}

func TestList_FTSQuery_MatchesSummary(t *testing.T) {
	db := openTestDBWithFTS(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	ctx := context.Background()
	_, _ = w.Emit(ctx, Entry{
		WorkspaceID: "ws_test",
		Type:        EntryPeerConversation,
		ActorType:   ActorAgent,
		Summary:     "deploy production webhook",
	})
	_, _ = w.Emit(ctx, Entry{
		WorkspaceID: "ws_test",
		Type:        EntryPeerConversation,
		ActorType:   ActorAgent,
		Summary:     "fix database bug",
	})
	_ = w.Flush(ctx)
	time.Sleep(50 * time.Millisecond)

	got, _, err := List(ctx, db, Query{WorkspaceID: "ws_test", FTSQuery: "deploy"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].Summary != "deploy production webhook" {
		t.Errorf("FTS by summary: %d hits, first=%v", len(got), summaries(got))
	}

	// Count must agree with List so the UI badge matches.
	n, err := Count(ctx, db, Query{WorkspaceID: "ws_test", FTSQuery: "deploy"})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("Count: %d want 1", n)
	}
}

func TestList_FTSQuery_MatchesPayload(t *testing.T) {
	db := openTestDBWithFTS(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	ctx := context.Background()
	_, _ = w.Emit(ctx, Entry{
		WorkspaceID: "ws_test",
		Type:        EntryExecCommand,
		ActorType:   ActorAgent,
		Summary:     "container died",
		Payload:     map[string]any{"error": "OOMKilled exit 137"},
	})
	_, _ = w.Emit(ctx, Entry{
		WorkspaceID: "ws_test",
		Type:        EntryExecCommand,
		ActorType:   ActorAgent,
		Summary:     "container died",
		Payload:     map[string]any{"error": "SIGTERM normal shutdown"},
	})
	_ = w.Flush(ctx)
	time.Sleep(50 * time.Millisecond)

	got, _, err := List(ctx, db, Query{WorkspaceID: "ws_test", FTSQuery: "OOMKilled"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("FTS by payload: got %d hits, want 1", len(got))
	}
	if v, _ := got[0].Payload["error"].(string); v != "OOMKilled exit 137" {
		t.Errorf("wrong row matched: %v", got[0].Payload)
	}
}

func TestList_FTSQuery_RespectsWorkspace(t *testing.T) {
	db := openTestDBWithFTS(t)
	defer db.Close()
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO workspaces (id) VALUES ('ws_other')`); err != nil {
		t.Fatal(err)
	}
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	ctx := context.Background()
	_, _ = w.Emit(ctx, Entry{
		WorkspaceID: "ws_test",
		Type:        EntryPeerConversation,
		ActorType:   ActorAgent,
		Summary:     "shared keyword zoltar mine",
	})
	_, _ = w.Emit(ctx, Entry{
		WorkspaceID: "ws_other",
		Type:        EntryPeerConversation,
		ActorType:   ActorAgent,
		Summary:     "shared keyword zoltar theirs",
	})
	_ = w.Flush(ctx)
	time.Sleep(50 * time.Millisecond)

	got, _, err := List(ctx, db, Query{WorkspaceID: "ws_test", FTSQuery: "zoltar"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].Summary != "shared keyword zoltar mine" {
		t.Errorf("workspace leak via FTS: %v", summaries(got))
	}
}

// SanitizesQuotes verifies that user-supplied double quotes don't break
// the FTS5 phrase parser. The phrase-quoting in fts5Phrase doubles
// internal quotes per the FTS5 grammar.
func TestList_FTSQuery_SanitizesQuotes(t *testing.T) {
	db := openTestDBWithFTS(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	ctx := context.Background()
	_, _ = w.Emit(ctx, Entry{
		WorkspaceID: "ws_test",
		Type:        EntryPeerConversation,
		ActorType:   ActorAgent,
		Summary:     `the foo bar baz line`,
	})
	_ = w.Flush(ctx)
	time.Sleep(50 * time.Millisecond)

	// Each input here would error or behave unexpectedly without
	// proper escaping. We just need List to not return an error and
	// to return a stable result set.
	for _, query := range []string{
		`foo"bar`,
		`a "b`,
		`""`,
		`foo NEAR bar`, // operator must be neutralised by phrase quoting
		`*`,
	} {
		_, _, err := List(ctx, db, Query{WorkspaceID: "ws_test", FTSQuery: query})
		if err != nil {
			t.Errorf("List with FTSQuery=%q errored: %v", query, err)
		}
	}
}

// EmptyFTSQuery is a no-op — falls through to the regular indexed path
// without joining the FTS shadow table. Whitespace-only is treated the
// same so a trimmed empty input doesn't suddenly drop all rows.
func TestList_FTSQuery_EmptyIsNoop(t *testing.T) {
	db := openTestDBWithFTS(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		_, _ = w.Emit(ctx, Entry{
			WorkspaceID: "ws_test",
			Type:        EntryPeerConversation,
			ActorType:   ActorAgent,
			Summary:     "anything",
		})
	}
	_ = w.Flush(ctx)
	time.Sleep(50 * time.Millisecond)

	for _, q := range []string{"", "   ", "\t"} {
		got, _, err := List(ctx, db, Query{WorkspaceID: "ws_test", FTSQuery: q})
		if err != nil {
			t.Fatalf("FTSQuery=%q: %v", q, err)
		}
		if len(got) != 3 {
			t.Errorf("empty FTSQuery should be no-op; got %d rows want 3", len(got))
		}
	}
}

// summaries is a tiny helper for readable failure messages.
func summaries(in []Entry) []string {
	out := make([]string, len(in))
	for i, e := range in {
		out[i] = e.Summary
	}
	return out
}
