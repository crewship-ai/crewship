package episodic

// Coverage tests for hybrid.go's bm25Lane — the sparse FTS5 retrieval
// lane. The base episodic test schema has no journal_entries_fts table;
// this file installs the migration-55 fixture so the BM25 SQL path is
// exercised end to end.

import (
	"context"
	"database/sql"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

// episodicFTSSchema mirrors migration 55's FTS5 virtual table + insert
// trigger (the only trigger these tests need — entries are never updated
// or deleted here).
const episodicFTSSchema = `
CREATE VIRTUAL TABLE journal_entries_fts USING fts5(
    summary, payload,
    content='journal_entries',
    content_rowid='rowid',
    tokenize='porter ascii'
);
CREATE TRIGGER journal_entries_ai AFTER INSERT ON journal_entries BEGIN
    INSERT INTO journal_entries_fts(rowid, summary, payload) VALUES (new.rowid, new.summary, new.payload);
END;
`

func openTestDBWithFTSEpisodic(t *testing.T) *sql.DB {
	t.Helper()
	db := openTestDB(t)
	if _, err := db.Exec(episodicFTSSchema); err != nil {
		t.Fatalf("fts schema: %v", err)
	}
	return db
}

func TestBM25Lane_ScopeOwn_RanksKeywordMatch(t *testing.T) {
	db := openTestDBWithFTSEpisodic(t)
	defer db.Close()
	ctx := context.Background()

	insertEntry(t, db, journal.Entry{
		ID: "e1", WorkspaceID: "ws_test", AgentID: "a1",
		Type: journal.EntryPeerEscalation, Severity: journal.SeverityWarn,
		ActorType: journal.ActorAgent, Summary: "deployment rollback failed on prod"})
	insertEntry(t, db, journal.Entry{
		ID: "e2", WorkspaceID: "ws_test", AgentID: "a1",
		Type: journal.EntryPeerEscalation, Severity: journal.SeverityWarn,
		ActorType: journal.ActorAgent, Summary: "lunch order arrived"})
	// Same workspace, different agent — must be excluded by ScopeOwn.
	insertEntry(t, db, journal.Entry{
		ID: "e3", WorkspaceID: "ws_test", AgentID: "a2",
		Type: journal.EntryPeerEscalation, Severity: journal.SeverityWarn,
		ActorType: journal.ActorAgent, Summary: "deployment of the new build"})

	hits, err := bm25Lane(ctx, db, Query{
		WorkspaceID: "ws_test", AgentID: "a1", Scope: ScopeOwn,
		QueryText: "deployment failure",
	}, 10)
	if err != nil {
		t.Fatalf("bm25Lane: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected exactly 1 hit (own agent, keyword match), got %d: %v", len(hits), entryIDs(hits))
	}
	h := hits[0]
	if h.EntryID != "e1" {
		t.Errorf("hit = %s, want e1", h.EntryID)
	}
	if h.EntryType != string(journal.EntryPeerEscalation) {
		t.Errorf("entry type = %q", h.EntryType)
	}
	if h.AgentID != "a1" {
		t.Errorf("agent id = %q, want a1", h.AgentID)
	}
	if h.Score <= 0 || h.Score > 1 {
		t.Errorf("normalised bm25 score out of (0,1]: %v", h.Score)
	}
	if h.Age <= 0 {
		t.Errorf("age should be positive, got %v", h.Age)
	}
}

func TestBM25Lane_ScopeCrewShared(t *testing.T) {
	db := openTestDBWithFTSEpisodic(t)
	defer db.Close()
	ctx := context.Background()

	insertEntry(t, db, journal.Entry{
		ID: "c1", WorkspaceID: "ws_test", CrewID: "crew_a", AgentID: "a1",
		Type: journal.EntryPeerEscalation, Severity: journal.SeverityWarn,
		ActorType: journal.ActorAgent, Summary: "database migration stuck"})
	insertEntry(t, db, journal.Entry{
		ID: "c2", WorkspaceID: "ws_test", CrewID: "crew_b", AgentID: "a2",
		Type: journal.EntryPeerEscalation, Severity: journal.SeverityWarn,
		ActorType: journal.ActorAgent, Summary: "database migration finished"})

	hits, err := bm25Lane(ctx, db, Query{
		WorkspaceID: "ws_test", CrewID: "crew_a", Scope: ScopeCrewShared,
		QueryText: "database migration",
	}, 10)
	if err != nil {
		t.Fatalf("bm25Lane: %v", err)
	}
	if len(hits) != 1 || hits[0].EntryID != "c1" {
		t.Fatalf("crew scope leak: got %v, want [c1]", entryIDs(hits))
	}
}

func TestBM25Lane_ScopeValidationErrors(t *testing.T) {
	db := openTestDBWithFTSEpisodic(t)
	defer db.Close()
	ctx := context.Background()

	cases := []struct {
		name string
		q    Query
	}{
		{"own_without_agent", Query{WorkspaceID: "ws_test", Scope: ScopeOwn, QueryText: "anything"}},
		{"crew_without_crew", Query{WorkspaceID: "ws_test", Scope: ScopeCrewShared, QueryText: "anything"}},
		{"unknown_scope", Query{WorkspaceID: "ws_test", Scope: Scope("bogus"), QueryText: "anything"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := bm25Lane(ctx, db, c.q, 5); err == nil {
				t.Errorf("expected error for %s", c.name)
			}
		})
	}
}

func TestBM25Lane_EmptyQueryTextSkips(t *testing.T) {
	db := openTestDBWithFTSEpisodic(t)
	defer db.Close()
	// "a" collapses to an empty FTS term (single-char words dropped) →
	// the lane must return (nil, nil) without touching the DB.
	hits, err := bm25Lane(context.Background(), db, Query{
		WorkspaceID: "ws_test", AgentID: "a1", Scope: ScopeOwn, QueryText: "a !",
	}, 5)
	if err != nil {
		t.Fatalf("empty match term should be a no-op, got %v", err)
	}
	if hits != nil {
		t.Errorf("expected nil hits, got %v", entryIDs(hits))
	}
}

func TestHybridRecall_FusesDenseAndSparseLanes(t *testing.T) {
	db := openTestDBWithFTSEpisodic(t)
	defer db.Close()
	ctx := context.Background()

	insertEntry(t, db, journal.Entry{
		ID: "h1", WorkspaceID: "ws_test", AgentID: "a1",
		Type: journal.EntryPeerEscalation, Severity: journal.SeverityWarn,
		ActorType: journal.ActorAgent, Summary: "deploy pipeline exploded"})
	insertEntry(t, db, journal.Entry{
		ID: "h2", WorkspaceID: "ws_test", AgentID: "a1",
		Type: journal.EntryPeerEscalation, Severity: journal.SeverityWarn,
		ActorType: journal.ActorAgent, Summary: "weekly report shipped"})

	emb := &stubEmbedder{model: "t", dim: 4, vectors: map[string][]float32{
		"deploy": {1, 0, 0, 0},
	}}
	NewIndexer(db, emb, quietLogger(), 0).sweepOnce(ctx, 10)

	hits, err := HybridRecall(ctx, db, emb, Query{
		WorkspaceID: "ws_test", AgentID: "a1", Scope: ScopeOwn,
		QueryText: "deploy pipeline", K: 2,
	})
	if err != nil {
		t.Fatalf("HybridRecall: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected fused hits")
	}
	// h1 matches both lanes (dense via stub vector, sparse via keyword)
	// so RRF must put it first.
	if hits[0].EntryID != "h1" {
		t.Errorf("top fused hit = %s, want h1", hits[0].EntryID)
	}
}
