package memory

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/episodic"
)

// newFTSEngineWithChunks bootstraps a real *Engine seeded with a few
// markdown files so HybridSearch's FTS half produces deterministic
// hits. Returns the engine + base dir for test cleanup.
func newFTSEngineWithChunks(t *testing.T, files map[string]string) *Engine {
	t.Helper()
	base := t.TempDir()
	for name, body := range files {
		path := filepath.Join(base, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	eng, err := New(base, DefaultConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	if err := eng.Reindex(); err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	return eng
}

func TestHybridSearch_FTSOnly_NoEpisodic(t *testing.T) {
	// With db=nil and embedder=nil, HybridSearch degrades to FTS-only —
	// every hit is sourced from "fts" and ranks within FTS produce a
	// well-ordered RRF score series.
	eng := newFTSEngineWithChunks(t, map[string]string{
		"AGENT.md": "## section one\noutlands thieving system\n",
		"CREW.md":  "## crew notes\nshared workspace context\n",
	})

	hits, err := HybridSearch(context.Background(), eng, nil, nil, HybridQuery{
		WorkspaceID: "ws_test",
		Text:        "outlands",
		Limit:       5,
	})
	if err != nil {
		t.Fatalf("HybridSearch: %v", err)
	}
	if len(hits) == 0 {
		t.Fatalf("expected at least one hit on FTS-only path")
	}
	for _, h := range hits {
		if h.Source != "fts" {
			t.Errorf("expected source=fts, got %q", h.Source)
		}
		if h.FTS == nil {
			t.Errorf("source=fts hit must populate FTS field")
		}
	}
}

func TestHybridSearch_NoEngineNoEpisodic_EmptyResult(t *testing.T) {
	hits, err := HybridSearch(context.Background(), nil, nil, nil, HybridQuery{
		Text:  "anything",
		Limit: 5,
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(hits) != 0 {
		t.Errorf("expected empty result when both engines nil, got %d", len(hits))
	}
}

func TestHybridSearch_LimitClamp(t *testing.T) {
	// 0 → 10 default, > 50 → 50.
	eng := newFTSEngineWithChunks(t, map[string]string{"AGENT.md": "x"})
	for _, tc := range []struct{ in, want int }{
		{0, 10},
		{100, 50},
		{5, 5},
	} {
		hits, _ := HybridSearch(context.Background(), eng, nil, nil, HybridQuery{Text: "x", Limit: tc.in})
		// We can't assert exactly tc.want hits (depends on FTS),
		// but we can assert the result count never exceeds the
		// clamped limit.
		if len(hits) > tc.want {
			t.Errorf("limit=%d (clamped %d): got %d hits", tc.in, tc.want, len(hits))
		}
	}
}

func TestRrfScore_RanksMonotonic(t *testing.T) {
	// rank=1 must out-score rank=2 must out-score rank=3 etc.
	// rank=0 means "not in list" -> score 0 so the caller skips.
	if rrfScore(1) <= rrfScore(2) {
		t.Errorf("rank=1 should out-score rank=2")
	}
	if rrfScore(0) != 0 {
		t.Errorf("rank=0 score = %v, want 0 (sentinel)", rrfScore(0))
	}
	last := 1.0
	for r := 1; r <= 10; r++ {
		s := rrfScore(r)
		if s >= last {
			t.Errorf("rrfScore(%d)=%v not strictly decreasing (last=%v)", r, s, last)
		}
		last = s
	}
}

func TestFtsKey_StableForSameChunk(t *testing.T) {
	a := SearchResult{File: "AGENT.md", LineStart: 42, LineEnd: 50}
	b := SearchResult{File: "AGENT.md", LineStart: 42, LineEnd: 99} // different end-line same start
	if ftsKey(a) != ftsKey(b) {
		t.Errorf("ftsKey should be stable across LineEnd differences: %q vs %q", ftsKey(a), ftsKey(b))
	}
	c := SearchResult{File: "AGENT.md", LineStart: 43}
	if ftsKey(a) == ftsKey(c) {
		t.Errorf("ftsKey should differ on LineStart")
	}
}

// stubEmbedder satisfies episodic.Embedder so HybridSearch enters
// the episodic branch even though we don't have a real Ollama running.
// Returns a fixed vector — episodic.HybridRecall will gracefully
// return zero results when the journal_entries table is empty, which
// is what these tests want (we're testing the dispatch, not episodic
// recall itself).
type stubEmbedder struct{}

func (stubEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	v := make([]float32, 768)
	v[0] = 1.0
	return v, nil
}

func (stubEmbedder) Dim() int      { return 768 }
func (stubEmbedder) Model() string { return "stub" }

func TestHybridSearch_EpisodicBranch_NoJournalRows_EmptyEpisodic(t *testing.T) {
	// db with the episodic schema but no rows → episodic side returns
	// 0 hits; FTS side still contributes; final list is FTS-only.
	db := openVersionsDB(t) // workspaces + memory_versions; episodic tables absent → HybridRecall will fail gracefully
	eng := newFTSEngineWithChunks(t, map[string]string{
		"AGENT.md": "outlands custom thieving\n",
	})
	hits, err := HybridSearch(context.Background(), eng, db, stubEmbedder{}, HybridQuery{
		WorkspaceID: "ws_test",
		Text:        "thieving",
		Scope:       episodic.ScopeCrewShared,
		Limit:       5,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// FTS half still works; episodic half errors out silently
	// (missing tables) and contributes 0 hits. That matches the doc:
	// "never errors out on a single-engine failure".
	for _, h := range hits {
		if h.Source != "fts" {
			t.Errorf("expected fts source without journal rows, got %q", h.Source)
		}
	}
}
