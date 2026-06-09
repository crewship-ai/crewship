package conversation

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/logging"
)

// newSearchStore opens a migrated in-tree DB and returns a Store wired to
// it via WithDB so the dual-write + Search paths are exercised end-to-end
// against the real v111 schema (not a hand-rolled fixture).
func newSearchStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	db, err := database.Open("file:" + filepath.Join(dir, "search.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.Migrate(context.Background(), db.DB, logging.New("error", "json", nil)); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store := NewStore(dir, logging.New("error", "json", nil), WithDB(db.DB))
	t.Cleanup(store.Close)
	return store
}

func appendMsg(t *testing.T, s *Store, sessionID, agentID string, role Role, content, toolSummary string) {
	t.Helper()
	err := s.Append(context.Background(), sessionID, Message{
		ID:          "m_" + content[:min(len(content), 8)] + "_" + agentID + "_" + time.Now().Format("150405.000000000"),
		AgentID:     agentID,
		Role:        role,
		Content:     content,
		ToolSummary: toolSummary,
		Timestamp:   time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
}

// TestSearch_DualWriteMirror verifies Append dual-writes a searchable row
// (the JSONL file AND a conversation_messages row) and that Search finds it
// by keyword.
func TestSearch_DualWriteMirror(t *testing.T) {
	s := newSearchStore(t)
	appendMsg(t, s, "sess1", "agentA", RoleUser, "please deploy the staging pipeline tonight", "")

	// JSONL still written.
	msgs, err := s.Read(context.Background(), "sess1", 0, 0)
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Content != "please deploy the staging pipeline tonight" {
		t.Fatalf("jsonl roundtrip = %+v", msgs)
	}

	// DB mirror searchable.
	hits, err := s.Search(context.Background(), "agentA", "deploy", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %d, want 1", len(hits))
	}
	if hits[0].SessionID != "sess1" || hits[0].AgentID != "agentA" || hits[0].Role != RoleUser {
		t.Errorf("hit metadata wrong: %+v", hits[0])
	}
	if hits[0].Timestamp.IsZero() {
		t.Errorf("hit timestamp not parsed")
	}
}

// TestSearch_ToolSummaryIndexed confirms the tool_summary column is part of
// the FTS index, so an agent can recall a past turn by a tool it ran even
// when the assistant text didn't mention it.
func TestSearch_ToolSummaryIndexed(t *testing.T) {
	s := newSearchStore(t)
	appendMsg(t, s, "sess1", "agentA", RoleAssistant, "done", "ran terraform apply on cluster")

	hits, err := s.Search(context.Background(), "agentA", "terraform", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 || hits[0].ToolSummary != "ran terraform apply on cluster" {
		t.Fatalf("tool_summary not indexed: %+v", hits)
	}
}

// TestSearch_AgentIsolation is the load-bearing tenancy test: agentB's
// query must NEVER return agentA's rows even when both wrote the same
// keyword.
func TestSearch_AgentIsolation(t *testing.T) {
	s := newSearchStore(t)
	appendMsg(t, s, "sessA", "agentA", RoleUser, "the secret rollout plan for project orion", "")
	appendMsg(t, s, "sessB", "agentB", RoleUser, "unrelated chatter about lunch", "")

	// agentB searches for agentA's keyword — must get nothing.
	hits, err := s.Search(context.Background(), "agentB", "orion", 10)
	if err != nil {
		t.Fatalf("search agentB: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("agent isolation breach: agentB saw %d rows for agentA's keyword: %+v", len(hits), hits)
	}

	// agentA still finds its own row.
	hits, err = s.Search(context.Background(), "agentA", "orion", 10)
	if err != nil {
		t.Fatalf("search agentA: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("agentA should see its own row, got %d", len(hits))
	}
}

// TestSearch_FTSOperatorNeutralization confirms FTS5 operators in user
// input are treated as literal text via fts5Phrase, not as query syntax.
// A bare `pipeline*` prefix-glob or `db OR foo` must not broaden the search
// beyond a literal phrase match.
func TestSearch_FTSOperatorNeutralization(t *testing.T) {
	s := newSearchStore(t)
	appendMsg(t, s, "sess1", "agentA", RoleUser, "configure the database connection", "")
	appendMsg(t, s, "sess1", "agentA", RoleUser, "restart the pipeline runner", "")

	cases := []struct {
		name  string
		query string
		want  int
	}{
		// `OR` as a literal — wrapping in a phrase means FTS5 sees the
		// three tokens "database OR pipeline" as an adjacency phrase, not
		// a boolean union. No single message has all three adjacent, so
		// the operator is neutralised and the result is 0 rather than the
		// union of the two rows (which an unescaped `OR` would return).
		{"or_operator_neutralised", "database OR pipeline", 0},
		// Trailing `*` inside a phrase is stripped by the tokenizer to a
		// plain "pipeline" token — crucially it is NOT honoured as a
		// prefix glob, so a query like `pipe*` can't broaden the match.
		// Here "pipeline*" tokenizes to "pipeline" and matches the one row.
		{"prefix_glob_disarmed", "pipeline*", 1},
		// Plain literal phrase still matches.
		{"plain_phrase", "database connection", 1},
		// An unbalanced quote must not blow up the FTS5 parser — fts5Phrase
		// doubles internal quotes so the query stays syntactically valid.
		// "database\"" tokenizes to "database" and matches its row.
		{"unbalanced_quote_safe", `database"`, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hits, err := s.Search(context.Background(), "agentA", tc.query, 10)
			if err != nil {
				t.Fatalf("search %q: %v", tc.query, err)
			}
			if len(hits) != tc.want {
				t.Errorf("query %q: hits = %d, want %d (%+v)", tc.query, len(hits), tc.want, hits)
			}
		})
	}
}

// TestSearch_Errors covers the guard clauses: no DB mirror, empty agent,
// empty query, cancelled context, and the limit clamp.
func TestSearch_Errors(t *testing.T) {
	// No-DB store: Search must error, Append must still write JSONL.
	jsonlOnly := NewStore(t.TempDir(), logging.New("error", "json", nil))
	if err := jsonlOnly.Append(context.Background(), "s1", Message{ID: "x", Role: RoleUser, Content: "hi"}); err != nil {
		t.Fatalf("jsonl-only append: %v", err)
	}
	if _, err := jsonlOnly.Search(context.Background(), "agentA", "hi", 10); err == nil {
		t.Errorf("expected error searching without DB mirror")
	}

	s := newSearchStore(t)
	if _, err := s.Search(context.Background(), "  ", "hi", 10); err == nil {
		t.Errorf("expected error for empty agent_id")
	}
	if _, err := s.Search(context.Background(), "agentA", "   ", 10); err == nil {
		t.Errorf("expected error for empty query")
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Search(cancelled, "agentA", "hi", 10); err == nil {
		t.Errorf("expected error for cancelled context")
	}

	// Limit clamp: out-of-range limits fall back to the default 20 and
	// don't error.
	appendMsg(t, s, "sess1", "agentA", RoleUser, "alpha beta gamma keyword", "")
	if _, err := s.Search(context.Background(), "agentA", "keyword", -5); err != nil {
		t.Errorf("negative limit should clamp, got %v", err)
	}
	if _, err := s.Search(context.Background(), "agentA", "keyword", 9999); err != nil {
		t.Errorf("oversized limit should clamp, got %v", err)
	}
}

// TestSearch_MirrorWriteFailureDoesNotFailAppend verifies that a failing
// mirror insert (here: a duplicate primary key) is logged and swallowed —
// the JSONL write is the source of truth and Append still succeeds.
func TestSearch_MirrorWriteFailureDoesNotFailAppend(t *testing.T) {
	s := newSearchStore(t)
	msg := Message{ID: "dup-id", AgentID: "agentA", Role: RoleUser, Content: "first", Timestamp: time.Now().UTC()}
	if err := s.Append(context.Background(), "sess1", msg); err != nil {
		t.Fatalf("first append: %v", err)
	}
	// Same ID again → mirror INSERT hits the PK constraint, but Append must
	// still return nil because the JSONL line was written.
	msg.Content = "second"
	if err := s.Append(context.Background(), "sess1", msg); err != nil {
		t.Fatalf("duplicate-id append should not fail (JSONL is source of truth): %v", err)
	}
	// Both JSONL lines present; only the first made it into the mirror.
	msgs, err := s.Read(context.Background(), "sess1", 0, 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(msgs) != 2 {
		t.Errorf("jsonl line count = %d, want 2", len(msgs))
	}
}

// TestSearch_RFC3339NanoTimestampFallback inserts a mirror row directly with
// an RFC3339Nano timestamp (the format the API adapter emits) so Search's
// secondary timestamp parse branch is exercised.
func TestSearch_RFC3339NanoTimestampFallback(t *testing.T) {
	s := newSearchStore(t)
	if _, err := s.db.Exec(
		`INSERT INTO conversation_messages (id, session_id, agent_id, role, content, tool_summary, ts)
		 VALUES ('rfc1','sessR','agentA','user','nanostamp keyword here','', ?)`,
		"2026-06-01T10:00:00.123456789Z",
	); err != nil {
		t.Fatalf("insert rfc row: %v", err)
	}
	hits, err := s.Search(context.Background(), "agentA", "nanostamp", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 || hits[0].Timestamp.IsZero() {
		t.Fatalf("RFC3339Nano fallback not parsed: %+v", hits)
	}
}

// TestSearch_BM25Ordering verifies hits come back best-match first.
func TestSearch_BM25Ordering(t *testing.T) {
	s := newSearchStore(t)
	// A row mentioning the term repeatedly should outrank a single mention
	// under BM25.
	appendMsg(t, s, "sess1", "agentA", RoleUser, "single mention of widget here", "")
	appendMsg(t, s, "sess1", "agentA", RoleUser, "widget widget widget all widget", "")

	hits, err := s.Search(context.Background(), "agentA", "widget", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(hits))
	}
	if hits[0].Content != "widget widget widget all widget" {
		t.Errorf("BM25 ordering wrong; top hit = %q", hits[0].Content)
	}
}
