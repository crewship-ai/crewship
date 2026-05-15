package episodic

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/journal"
	_ "modernc.org/sqlite"
)

// schemaSQL mirrors the subset of migration 52 the episodic tests need.
// Kept inline so the test pulls in no migrate machinery.
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
    priority TEXT NOT NULL DEFAULT 'normal',
    actor_type TEXT NOT NULL,
    actor_id TEXT,
    summary TEXT NOT NULL,
    payload TEXT NOT NULL DEFAULT '{}',
    refs TEXT NOT NULL DEFAULT '{}',
    trace_id TEXT,
    span_id TEXT,
    expires_at TEXT
);

CREATE TABLE journal_embeddings (
    entry_id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    crew_id TEXT,
    agent_id TEXT,
    model TEXT NOT NULL,
    dim INTEGER NOT NULL,
    vector BLOB NOT NULL,
    indexed_at TEXT NOT NULL,
    importance_score REAL NOT NULL DEFAULT 0.5,
    reference_count INTEGER NOT NULL DEFAULT 0,
    last_referenced_at TEXT
);

CREATE TABLE memory_relations (
    entry_id TEXT NOT NULL,
    related_entry_id TEXT NOT NULL,
    relation_kind TEXT NOT NULL,
    score REAL NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY(entry_id, related_entry_id, relation_kind)
);
`

type stubEmbedder struct {
	model string
	dim   int
	// vectors maps an input substring to a deterministic output so tests
	// control which inputs are "similar".
	vectors map[string][]float32
}

func (s *stubEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	for k, v := range s.vectors {
		if contains(text, k) {
			return append([]float32(nil), v...), nil
		}
	}
	// Default: random-ish but deterministic per text so duplicate calls
	// return the same vector (tests rely on this for non-match cases).
	r := rand.New(rand.NewPCG(uint64(stringHash(text)), 42))
	out := make([]float32, s.dim)
	for i := range out {
		out[i] = r.Float32()*2 - 1
	}
	return out, nil
}

func (s *stubEmbedder) Dim() int      { return s.dim }
func (s *stubEmbedder) Model() string { return s.model }

func contains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func stringHash(s string) uint64 {
	var h uint64 = 14695981039346656037
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

func insertEntry(t *testing.T, db *sql.DB, e journal.Entry) {
	t.Helper()
	ts := e.TS.Format("2006-01-02T15:04:05.000Z")
	if e.TS.IsZero() {
		ts = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	}
	_, err := db.Exec(`INSERT INTO journal_entries
		(id, workspace_id, crew_id, agent_id, ts, entry_type, severity, actor_type, actor_id, summary, payload, refs)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.WorkspaceID, nullableStr(e.CrewID), nullableStr(e.AgentID),
		ts, string(e.Type), string(e.Severity), string(e.ActorType), e.ActorID,
		e.Summary, "{}", "{}")
	if err != nil {
		t.Fatalf("insert entry: %v", err)
	}
}

func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestIndexerSelectiveFilter(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	// Three entries: escalation (should embed), summary (should embed),
	// exec.output_chunk (must NOT embed even if forced through sweep).
	insertEntry(t, db, journal.Entry{ID: "j1", WorkspaceID: "ws_test", AgentID: "a1",
		Type: journal.EntryPeerEscalation, Severity: journal.SeverityWarn, ActorType: journal.ActorAgent, Summary: "deploy broke"})
	insertEntry(t, db, journal.Entry{ID: "j2", WorkspaceID: "ws_test", AgentID: "a1",
		Type: journal.EntrySummaryGenerated, Severity: journal.SeverityInfo, ActorType: journal.ActorSystem, Summary: "daily digest"})
	insertEntry(t, db, journal.Entry{ID: "j3", WorkspaceID: "ws_test", AgentID: "a1",
		Type: journal.EntryExecOutputChunk, Severity: journal.SeverityInfo, ActorType: journal.ActorSidecar, Summary: "stdout"})

	emb := &stubEmbedder{model: "test-embed", dim: 4,
		vectors: map[string][]float32{
			"deploy": {1, 0, 0, 0},
			"daily":  {0, 1, 0, 0},
		}}
	idx := NewIndexer(db, emb, quietLogger(), 0)
	idx.sweepOnce(context.Background(), 10)

	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM journal_embeddings WHERE dim > 0`).Scan(&n)
	if n != 2 {
		t.Errorf("expected 2 real embeddings (escalation + summary), got %d", n)
	}
	// j3 (exec.output_chunk) must not have been selected at all — it's
	// excluded by the SQL-level type filter, so no tombstone either.
	var j3count int
	_ = db.QueryRow(`SELECT COUNT(*) FROM journal_embeddings WHERE entry_id = 'j3'`).Scan(&j3count)
	if j3count != 0 {
		t.Errorf("exec.output_chunk leaked into embeddings, count=%d", j3count)
	}
}

func TestRecallTopK(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	// Three escalations, stub embeddings make "deployment" match j1 best.
	for _, e := range []journal.Entry{
		{ID: "j1", WorkspaceID: "ws_test", AgentID: "a1", Type: journal.EntryPeerEscalation, Severity: journal.SeverityWarn, ActorType: journal.ActorAgent, Summary: "deployment broke: missing env var"},
		{ID: "j2", WorkspaceID: "ws_test", AgentID: "a1", Type: journal.EntryPeerEscalation, Severity: journal.SeverityWarn, ActorType: journal.ActorAgent, Summary: "database migration failed"},
		{ID: "j3", WorkspaceID: "ws_test", AgentID: "a1", Type: journal.EntryPeerEscalation, Severity: journal.SeverityWarn, ActorType: journal.ActorAgent, Summary: "lint failure"},
	} {
		insertEntry(t, db, e)
	}
	emb := &stubEmbedder{model: "test-embed", dim: 4,
		vectors: map[string][]float32{
			"deployment": {1, 0, 0, 0},
			"migration":  {0, 1, 0, 0},
			"lint":       {0, 0, 1, 0},
		}}
	idx := NewIndexer(db, emb, quietLogger(), 0)
	idx.sweepOnce(context.Background(), 10)

	hits, err := Recall(context.Background(), db, emb, Query{
		WorkspaceID: "ws_test", AgentID: "a1", Scope: ScopeOwn,
		QueryText: "deployment issue", K: 2,
	})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("expected 2 hits, got %d", len(hits))
	}
	if hits[0].EntryID != "j1" {
		t.Errorf("expected j1 top match, got %s", hits[0].EntryID)
	}
}

func TestRecallWorkspaceIsolation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO workspaces (id) VALUES ('ws_other')`); err != nil {
		t.Fatal(err)
	}
	insertEntry(t, db, journal.Entry{ID: "j1", WorkspaceID: "ws_test", AgentID: "a1",
		Type: journal.EntryPeerEscalation, Severity: journal.SeverityWarn, ActorType: journal.ActorAgent, Summary: "mine"})
	insertEntry(t, db, journal.Entry{ID: "j2", WorkspaceID: "ws_other", AgentID: "a2",
		Type: journal.EntryPeerEscalation, Severity: journal.SeverityWarn, ActorType: journal.ActorAgent, Summary: "theirs"})

	emb := &stubEmbedder{model: "test-embed", dim: 4}
	NewIndexer(db, emb, quietLogger(), 0).sweepOnce(context.Background(), 10)

	hits, err := Recall(context.Background(), db, emb, Query{
		WorkspaceID: "ws_test", AgentID: "a1", Scope: ScopeOwn,
		QueryText: "anything", K: 10,
	})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	for _, h := range hits {
		if h.EntryID == "j2" {
			t.Errorf("workspace leak: got %s from ws_other", h.EntryID)
		}
	}
}

func TestScopeCrewShared(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	// Two agents in same crew; lead recall should see both.
	insertEntry(t, db, journal.Entry{ID: "j1", WorkspaceID: "ws_test", CrewID: "crew_a", AgentID: "a1",
		Type: journal.EntryPeerEscalation, Severity: journal.SeverityWarn, ActorType: journal.ActorAgent, Summary: "alice issue"})
	insertEntry(t, db, journal.Entry{ID: "j2", WorkspaceID: "ws_test", CrewID: "crew_a", AgentID: "a2",
		Type: journal.EntryPeerEscalation, Severity: journal.SeverityWarn, ActorType: journal.ActorAgent, Summary: "bob issue"})
	insertEntry(t, db, journal.Entry{ID: "j3", WorkspaceID: "ws_test", CrewID: "crew_b", AgentID: "a3",
		Type: journal.EntryPeerEscalation, Severity: journal.SeverityWarn, ActorType: journal.ActorAgent, Summary: "other crew"})

	emb := &stubEmbedder{model: "test-embed", dim: 4}
	NewIndexer(db, emb, quietLogger(), 0).sweepOnce(context.Background(), 10)

	hits, err := Recall(context.Background(), db, emb, Query{
		WorkspaceID: "ws_test", CrewID: "crew_a", Scope: ScopeCrewShared,
		QueryText: "anything", K: 10,
	})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	gotIDs := map[string]bool{}
	for _, h := range hits {
		gotIDs[h.EntryID] = true
	}
	if !gotIDs["j1"] || !gotIDs["j2"] {
		t.Errorf("expected both crew_a entries, got %v", gotIDs)
	}
	if gotIDs["j3"] {
		t.Error("crew_b leaked into crew_a recall")
	}
}

func TestVectorRoundtrip(t *testing.T) {
	in := []float32{1.5, -2.25, 0, 3.14159, -0.0001}
	blob := EncodeVector(in)
	out, err := DecodeVector(blob, len(in))
	if err != nil {
		t.Fatal(err)
	}
	for i := range in {
		if in[i] != out[i] {
			t.Errorf("idx %d: in=%f out=%f", i, in[i], out[i])
		}
	}
}

// TestEmbedTruncatesAtRuneBoundary ensures the 4096-char input cap doesn't
// split a multi-byte UTF-8 rune. Without rune-aware truncation, the
// resulting prompt is invalid UTF-8 — Ollama either returns garbage or
// rejects the request, and the surviving JSON body contains a U+FFFD
// replacement at the cut site.
func TestEmbedTruncatesAtRuneBoundary(t *testing.T) {
	var seenPrompt string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Prompt string `json:"prompt"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		seenPrompt = body.Prompt
		_ = json.NewEncoder(w).Encode(map[string]any{"embedding": []float64{0.1, 0.2}})
	}))
	defer srv.Close()

	// Japanese "日" is 3 bytes in UTF-8. 4096 % 3 == 1, so cutting at byte
	// 4096 lands one byte INTO a rune — exactly the case the byte-slice
	// truncation breaks. With 2-byte runes (e.g. German "ö") 4096 is a
	// rune boundary by accident, hiding the bug.
	in := strings.Repeat("日", 2000)
	emb := NewOllamaEmbedder(srv.URL)
	if _, err := emb.Embed(context.Background(), in); err != nil {
		t.Fatalf("embed: %v", err)
	}

	if !utf8.ValidString(seenPrompt) {
		t.Errorf("server received invalid UTF-8 prompt — truncation cut mid-rune")
	}
	if strings.ContainsRune(seenPrompt, '�') {
		t.Errorf("prompt contains U+FFFD replacement char — truncation cut mid-rune")
	}
	if seenPrompt == "" {
		t.Errorf("expected non-empty prompt")
	}
}

func TestScopeForRole(t *testing.T) {
	tests := map[string]Scope{
		"LEAD":    ScopeCrewShared,
		"AGENT":   ScopeOwn,
		"":        ScopeOwn,
		"unknown": ScopeOwn,
	}
	for role, want := range tests {
		if got := ScopeForRole(role); got != want {
			t.Errorf("role %q: got %q want %q", role, got, want)
		}
	}
}
