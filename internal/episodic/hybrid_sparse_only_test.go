package episodic

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

// TestHybridRecall_NilEmbedder_ServesSparseLane pins the degraded mode
// /healthz has been advertising all along.
//
// episodicMode() reports "sparse-only" when no embedder is configured
// and `crewship doctor` surfaces it, but HybridRecall took emb straight
// into Recall, which calls emb.Embed — a nil interface method call. The
// one caller that could reach it (the orchestrator's recall adapter)
// guarded with `if a.embedder == nil { return "", nil }`, so on any
// install without Ollama the whole episodic tier returned empty, while
// the BM25 lane underneath it needed no embedder at all. Removing that
// guard without this fix would panic instead.
func TestHybridRecall_NilEmbedder_ServesSparseLane(t *testing.T) {
	db := openTestDBWithFTSEpisodic(t)
	defer db.Close()

	insertEntry(t, db, journal.Entry{
		ID: "s1", WorkspaceID: "ws_test", AgentID: "a1",
		Type: journal.EntryPeerEscalation, Severity: journal.SeverityWarn,
		ActorType: journal.ActorAgent, Summary: "deployment rollback failed on prod"})
	insertEntry(t, db, journal.Entry{
		ID: "s2", WorkspaceID: "ws_test", AgentID: "a1",
		Type: journal.EntryPeerEscalation, Severity: journal.SeverityWarn,
		ActorType: journal.ActorAgent, Summary: "lunch order arrived"})

	hits, err := HybridRecall(context.Background(), db, nil, Query{
		WorkspaceID: "ws_test", AgentID: "a1", Scope: ScopeOwn,
		QueryText: "deployment rollback", K: 5,
	})
	if err != nil {
		t.Fatalf("HybridRecall with no embedder: %v", err)
	}
	if len(hits) != 1 || hits[0].EntryID != "s1" {
		t.Fatalf("sparse-only recall should return the keyword match, got %v", entryIDs(hits))
	}
}

// TestHybridRecall_NilEmbedder_NoFTS5Table returns empty rather than
// erroring: with neither lane available there is nothing to recall, and
// a recall that fails the run is worse than a recall that finds
// nothing (pre-migration-55 databases hit exactly this).
func TestHybridRecall_NilEmbedder_NoFTS5Table(t *testing.T) {
	db := openTestDB(t) // schema has no journal_entries_fts
	defer db.Close()

	hits, err := HybridRecall(context.Background(), db, nil, Query{
		WorkspaceID: "ws_test", AgentID: "a1", Scope: ScopeOwn,
		QueryText: "deployment", K: 5,
	})
	if err != nil {
		t.Fatalf("both lanes unavailable should degrade, not error: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("expected no hits, got %v", entryIDs(hits))
	}
}
