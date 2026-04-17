package journal

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// schemaSQL mirrors migration 52's journal_entries table. The test opens a
// fresh in-memory DB and applies this directly instead of pulling in the
// whole migrate package — keeps the unit test fast and self-contained.
const schemaSQL = `
CREATE TABLE workspaces (id TEXT PRIMARY KEY);
INSERT INTO workspaces (id) VALUES ('ws_test');

CREATE TABLE journal_entries (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    crew_id TEXT,
    agent_id TEXT,
    mission_id TEXT,
    ts TEXT NOT NULL,
    entry_type TEXT NOT NULL,
    severity TEXT NOT NULL DEFAULT 'info',
    actor_type TEXT NOT NULL,
    actor_id TEXT,
    summary TEXT NOT NULL,
    payload TEXT NOT NULL DEFAULT '{}',
    refs TEXT NOT NULL DEFAULT '{}',
    trace_id TEXT,
    span_id TEXT,
    expires_at TEXT
);
CREATE INDEX idx_journal_ws_ts ON journal_entries(workspace_id, ts DESC);
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

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestEmitAndList(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 2, FlushInterval: 10 * time.Millisecond})
	defer w.Close()

	ctx := context.Background()
	id, err := w.Emit(ctx, Entry{
		WorkspaceID: "ws_test",
		Type:        EntryPeerConversation,
		ActorType:   ActorAgent,
		ActorID:     "agent_a",
		Summary:     "agent_a asked agent_b a question",
		Payload:     map[string]any{"question": "How do I deploy?", "confidence": 0.9},
	})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if id == "" {
		t.Fatal("expected id")
	}

	// Give the batcher a moment to flush.
	time.Sleep(50 * time.Millisecond)

	entries, _, err := List(ctx, db, Query{WorkspaceID: "ws_test"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	got := entries[0]
	if got.Type != EntryPeerConversation {
		t.Errorf("type: got %q want %q", got.Type, EntryPeerConversation)
	}
	if got.Payload["question"] != "How do I deploy?" {
		t.Errorf("payload roundtrip: %v", got.Payload)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		entry   Entry
		wantErr bool
	}{
		{"missing workspace", Entry{Type: EntryPeerConversation, ActorType: ActorAgent, Summary: "x"}, true},
		{"missing type", Entry{WorkspaceID: "w", ActorType: ActorAgent, Summary: "x"}, true},
		{"missing actor", Entry{WorkspaceID: "w", Type: EntryPeerConversation, Summary: "x"}, true},
		{"missing summary", Entry{WorkspaceID: "w", Type: EntryPeerConversation, ActorType: ActorAgent}, true},
		{"valid", Entry{WorkspaceID: "w", Type: EntryPeerConversation, ActorType: ActorAgent, Summary: "x"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.entry.Validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("want err=%v got %v", tc.wantErr, err)
			}
		})
	}
}

func TestFilters(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	ctx := context.Background()
	base := Entry{WorkspaceID: "ws_test", ActorType: ActorAgent, ActorID: "a1", Summary: "s"}
	for i, spec := range []struct {
		kind EntryType
		sev  Severity
		crew string
	}{
		{EntryPeerConversation, SeverityInfo, "crew_a"},
		{EntryKeeperDecision, SeverityWarn, "crew_a"},
		{EntryMissionStatus, SeverityInfo, "crew_b"},
	} {
		e := base
		e.Type = spec.kind
		e.Severity = spec.sev
		e.CrewID = spec.crew
		_, err := w.Emit(ctx, e)
		if err != nil {
			t.Fatalf("emit %d: %v", i, err)
		}
	}
	_ = w.Flush(ctx)
	time.Sleep(50 * time.Millisecond)

	byCrew, _, err := List(ctx, db, Query{WorkspaceID: "ws_test", CrewID: "crew_a"})
	if err != nil {
		t.Fatalf("list by crew: %v", err)
	}
	if len(byCrew) != 2 {
		t.Errorf("expected 2 in crew_a, got %d", len(byCrew))
	}

	bySev, _, err := List(ctx, db, Query{WorkspaceID: "ws_test", Severities: []Severity{SeverityWarn}})
	if err != nil {
		t.Fatalf("list by sev: %v", err)
	}
	if len(bySev) != 1 {
		t.Errorf("expected 1 warn, got %d", len(bySev))
	}

	byType, _, err := List(ctx, db, Query{WorkspaceID: "ws_test", Types: []EntryType{EntryMissionStatus}})
	if err != nil {
		t.Fatalf("list by type: %v", err)
	}
	if len(byType) != 1 {
		t.Errorf("expected 1 mission, got %d", len(byType))
	}
}

func TestWorkspaceIsolation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO workspaces (id) VALUES ('ws_other')`); err != nil {
		t.Fatal(err)
	}

	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	ctx := context.Background()
	_, _ = w.Emit(ctx, Entry{WorkspaceID: "ws_test", Type: EntryPeerConversation, ActorType: ActorAgent, Summary: "mine"})
	_, _ = w.Emit(ctx, Entry{WorkspaceID: "ws_other", Type: EntryPeerConversation, ActorType: ActorAgent, Summary: "theirs"})
	_ = w.Flush(ctx)
	time.Sleep(50 * time.Millisecond)

	entries, _, _ := List(ctx, db, Query{WorkspaceID: "ws_test"})
	if len(entries) != 1 || entries[0].Summary != "mine" {
		t.Errorf("workspace leak: %+v", entries)
	}
}

func TestTraceResolver(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	SetTraceResolver(func(ctx context.Context) (string, string, bool) {
		return "trace123", "span456", true
	})
	defer SetTraceResolver(nil)

	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	ctx := context.Background()
	_, err := w.Emit(ctx, Entry{
		WorkspaceID: "ws_test",
		Type:        EntryLLMCall,
		ActorType:   ActorSystem,
		Summary:     "llm call",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Flush(ctx)
	time.Sleep(50 * time.Millisecond)

	entries, _, _ := List(ctx, db, Query{WorkspaceID: "ws_test"})
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].TraceID != "trace123" || entries[0].SpanID != "span456" {
		t.Errorf("trace not injected: trace=%q span=%q", entries[0].TraceID, entries[0].SpanID)
	}
}

func TestPagination(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_, err := w.Emit(ctx, Entry{
			WorkspaceID: "ws_test",
			Type:        EntryPeerConversation,
			ActorType:   ActorAgent,
			Summary:     "msg",
			TS:          time.Now().UTC().Add(-time.Duration(5-i) * time.Second),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	_ = w.Flush(ctx)
	time.Sleep(50 * time.Millisecond)

	page1, cursor, err := List(ctx, db, Query{WorkspaceID: "ws_test", Limit: 2})
	if err != nil || len(page1) != 2 {
		t.Fatalf("page1: %v len=%d", err, len(page1))
	}
	if cursor == "" {
		t.Fatal("expected cursor")
	}
	page2, _, err := List(ctx, db, Query{WorkspaceID: "ws_test", Limit: 2, Cursor: cursor})
	if err != nil || len(page2) != 2 {
		t.Fatalf("page2: %v len=%d", err, len(page2))
	}
	// No overlap across pages.
	for _, a := range page1 {
		for _, b := range page2 {
			if a.ID == b.ID {
				t.Errorf("pagination overlap on %s", a.ID)
			}
		}
	}
}

func TestCount(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		_, _ = w.Emit(ctx, Entry{WorkspaceID: "ws_test", Type: EntryPeerConversation, ActorType: ActorAgent, Summary: "s"})
	}
	_ = w.Flush(ctx)
	time.Sleep(50 * time.Millisecond)

	n, err := Count(ctx, db, Query{WorkspaceID: "ws_test"})
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("count: got %d want 3", n)
	}
}
