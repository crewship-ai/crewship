package episodic

// Coverage tests for recall.go — Recall error branches, the reinforcement
// side-effect, humanizeAge buckets, decodeJSONMap fallbacks and
// RenderInjection budget handling.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// failingEmbedder always errors so tests can drive the embed-failure path.
type failingEmbedder struct{}

func (failingEmbedder) Embed(context.Context, string) ([]float32, error) {
	return nil, errors.New("ollama down")
}
func (failingEmbedder) Dim() int      { return 4 }
func (failingEmbedder) Model() string { return "fail" }

func TestRecall_EmbedErrorPropagates(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	_, err := Recall(context.Background(), db, failingEmbedder{}, Query{
		WorkspaceID: "ws_test", AgentID: "a1", Scope: ScopeOwn, QueryText: "x",
	})
	if err == nil || !strings.Contains(err.Error(), "embed query") {
		t.Fatalf("expected embed query error, got %v", err)
	}
}

func TestRecall_ScopeValidation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	emb := &stubEmbedder{model: "t", dim: 4}

	cases := []struct {
		name    string
		q       Query
		wantErr string
	}{
		{"no_workspace", Query{Scope: ScopeOwn, AgentID: "a1", QueryText: "x"}, "workspace_id"},
		{"own_no_agent", Query{WorkspaceID: "ws_test", Scope: ScopeOwn, QueryText: "x"}, "agent_id"},
		{"crew_no_crew", Query{WorkspaceID: "ws_test", Scope: ScopeCrewShared, QueryText: "x"}, "crew_id"},
		{"bad_scope", Query{WorkspaceID: "ws_test", Scope: Scope("nope"), QueryText: "x"}, "unknown scope"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Recall(context.Background(), db, emb, c.q)
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("want error containing %q, got %v", c.wantErr, err)
			}
		})
	}
}

func TestRecall_ReinforcesReturnedHits(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	insertEntry(t, db, journal.Entry{
		ID: "r1", WorkspaceID: "ws_test", AgentID: "a1",
		Type: journal.EntryPeerEscalation, Severity: journal.SeverityWarn,
		ActorType: journal.ActorAgent, Summary: "deploy broke badly"})

	emb := &stubEmbedder{model: "t", dim: 4, vectors: map[string][]float32{
		"deploy": {1, 0, 0, 0},
	}}
	NewIndexer(db, emb, quietLogger(), 0).sweepOnce(ctx, 10)

	hits, err := Recall(ctx, db, emb, Query{
		WorkspaceID: "ws_test", AgentID: "a1", Scope: ScopeOwn,
		QueryText: "deploy", K: 1,
	})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(hits) != 1 || hits[0].EntryID != "r1" {
		t.Fatalf("expected [r1], got %v", entryIDs(hits))
	}

	var refs int64
	var lastRef any
	if err := db.QueryRow(`SELECT reference_count, last_referenced_at FROM journal_embeddings WHERE entry_id = 'r1'`).
		Scan(&refs, &lastRef); err != nil {
		t.Fatalf("query refs: %v", err)
	}
	if refs != 1 {
		t.Errorf("reference_count after recall = %d, want 1", refs)
	}
	if lastRef == nil {
		t.Error("last_referenced_at not stamped by recall reinforcement")
	}
}

func TestRecall_ImportanceReordersSameCosine(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	// Two entries with identical vectors but different importance — the
	// higher-importance one must rank first even with equal cosine.
	for _, id := range []string{"low", "high"} {
		insertEntry(t, db, journal.Entry{
			ID: id, WorkspaceID: "ws_test", AgentID: "a1",
			Type: journal.EntryPeerEscalation, Severity: journal.SeverityWarn,
			ActorType: journal.ActorAgent, Summary: "same vector " + id})
		if _, err := db.Exec(`INSERT INTO journal_embeddings
			(entry_id, workspace_id, agent_id, model, dim, vector, indexed_at, importance_score)
			VALUES (?, 'ws_test', 'a1', 't', 4, ?, '2026-01-01T00:00:00.000Z', ?)`,
			id, EncodeVector([]float32{1, 0, 0, 0}), map[string]float64{"low": 0.1, "high": 0.9}[id]); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}

	emb := &stubEmbedder{model: "t", dim: 4, vectors: map[string][]float32{
		"same": {1, 0, 0, 0},
	}}
	hits, err := Recall(ctx, db, emb, Query{
		WorkspaceID: "ws_test", AgentID: "a1", Scope: ScopeOwn,
		QueryText: "same vector please", K: 2,
	})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("expected 2 hits, got %d", len(hits))
	}
	if hits[0].EntryID != "high" {
		t.Errorf("importance should break the cosine tie: got order %v", entryIDs(hits))
	}
}

func TestHumanizeAge_AllBuckets(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "just now"},
		{5 * time.Minute, "5m"},
		{3 * time.Hour, "3h"},
		{49 * time.Hour, "2d"},
		{45 * 24 * time.Hour, "1mo"},
	}
	for _, c := range cases {
		if got := humanizeAge(c.d); got != c.want {
			t.Errorf("humanizeAge(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestDecodeJSONMap(t *testing.T) {
	if got := decodeJSONMap(""); got != nil {
		t.Errorf("empty string → %v, want nil", got)
	}
	if got := decodeJSONMap("{}"); got != nil {
		t.Errorf("{} → %v, want nil", got)
	}
	if got := decodeJSONMap("{not json"); got != nil {
		t.Errorf("invalid json → %v, want nil", got)
	}
	got := decodeJSONMap(`{"k":"v","n":2}`)
	if got == nil || got["k"] != "v" || got["n"] != float64(2) {
		t.Errorf("valid json decoded wrong: %v", got)
	}
}

func TestRenderInjection_BudgetExhaustion(t *testing.T) {
	hits := []Hit{{EntryType: "peer.escalation", Age: time.Minute, Score: 0.9, Summary: "something happened"}}

	// maxChars smaller than the wrapper itself → empty output.
	if got := RenderInjection(hits, 10); got != "" {
		t.Errorf("tiny budget should render nothing, got %q", got)
	}

	// Default budget renders the wrapper + the line.
	out := RenderInjection(hits, 0)
	if !strings.Contains(out, "<recalled-memory>") || !strings.Contains(out, "something happened") {
		t.Errorf("default budget render missing parts: %q", out)
	}

	// A budget that fits the wrapper but not a single line → empty
	// (the body loop breaks before writing anything).
	long := Hit{EntryType: "peer.escalation", Age: time.Minute, Score: 0.9,
		Summary: strings.Repeat("x", 5000)}
	if got := RenderInjection([]Hit{long}, 600); got != "" {
		t.Errorf("over-budget single line should render empty, got %d chars", len(got))
	}
}
