package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// TestEpisodicRecallAdapter_WithoutEmbedder_ServesSparseLane is the
// #1651 "related, same area" fix.
//
// The adapter used to open with `if a.embedder == nil { return "", nil }`,
// so on any install without Ollama the entire episodic tier returned
// empty — while /healthz reported a "sparse-only" recall mode and
// `crewship doctor` told the operator that mode existed. The BM25 lane
// needs no embedder, and this is the tier the [MEMORY GAP] block sends a
// woken agent to.
func TestEpisodicRecallAdapter_WithoutEmbedder_ServesSparseLane(t *testing.T) {
	db := openTestDB(t)
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug, created_at, updated_at)
		VALUES ('ws-sparse', 'WS', 'wssparse', ?, ?)`, now, now); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO journal_entries
		(id, workspace_id, agent_id, ts, entry_type, severity, actor_type, summary, payload, refs)
		VALUES ('je-sparse-1', 'ws-sparse', 'agent-sparse', ?, 'peer.escalation', 'warn', 'agent',
		        'the harbour deployment rollback failed', '{}', '{}')`, now); err != nil {
		t.Fatalf("seed journal entry: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO journal_entries
		(id, workspace_id, agent_id, ts, entry_type, severity, actor_type, summary, payload, refs)
		VALUES ('je-sparse-2', 'ws-sparse', 'agent-sparse', ?, 'peer.escalation', 'info', 'agent',
		        'lunch order arrived', '{}', '{}')`, now); err != nil {
		t.Fatalf("seed journal entry: %v", err)
	}

	a := newEpisodicRecallAdapter(db, nil) // no embedder — the Ollama-less install
	out, err := a.Recall(context.Background(), orchestrator.EpisodicRecallInput{
		WorkspaceID: "ws-sparse",
		AgentID:     "agent-sparse",
		Role:        "agent",
		Query:       "harbour rollback",
		MaxChars:    2000,
	})
	if err != nil {
		t.Fatalf("Recall without embedder: %v", err)
	}
	if out == "" {
		t.Fatal("episodic recall returned empty with no embedder — the BM25 lane needs none, and /healthz claims sparse-only recall works")
	}
	if !strings.Contains(out, "harbour deployment rollback failed") {
		t.Errorf("expected the keyword match in the injection block, got:\n%s", out)
	}
	if strings.Contains(out, "lunch order") {
		t.Errorf("unrelated entry leaked into a keyword recall:\n%s", out)
	}
}

// TestEpisodicRecallAdapter_WithoutEmbedder_EmptyCorpus keeps the quiet
// case quiet: nothing to recall must still be ("", nil), not an error
// that fails the agent run.
func TestEpisodicRecallAdapter_WithoutEmbedder_EmptyCorpus(t *testing.T) {
	db := openTestDB(t)
	a := newEpisodicRecallAdapter(db, nil)
	out, err := a.Recall(context.Background(), orchestrator.EpisodicRecallInput{
		WorkspaceID: "ws-empty",
		AgentID:     "agent-empty",
		Role:        "agent",
		Query:       "anything",
		MaxChars:    2000,
	})
	if err != nil {
		t.Fatalf("Recall on an empty corpus: %v", err)
	}
	if out != "" {
		t.Errorf("expected empty recall, got %q", out)
	}
}
