package memory

import (
	"context"
	"database/sql"
	"sort"

	"github.com/crewship-ai/crewship/internal/episodic"
)

// HybridHit is a single result from HybridSearch, tagged with its
// originating engine so callers (sidecar, CLI, tool layer) can render
// or weight differently per source. Exactly one of FTS / Episodic is
// populated based on Source.
//
// Source values:
//
//	"fts"       FTS5 BM25 hit against a memory.Engine (markdown chunk)
//	"episodic"  Hybrid vec+BM25 hit against journal_entries
//
// Score is the post-RRF score (higher = more relevant). The two
// engines' native scores are NOT comparable directly — RRF reranks
// purely off ranks-within-list — so Score is monotonic-only across
// the combined list, not across individual native-score pairs.
type HybridHit struct {
	Source   string        `json:"source"`
	Score    float64       `json:"score"`
	FTS      *SearchResult `json:"fts,omitempty"`
	Episodic *episodic.Hit `json:"episodic,omitempty"`
}

// HybridQuery is the request shape. WorkspaceID + Text are required;
// AgentID + CrewID + Scope qualify the episodic side per
// episodic.Query.
type HybridQuery struct {
	WorkspaceID string
	AgentID     string
	CrewID      string
	Scope       episodic.Scope
	Text        string
	Limit       int
}

// rrfK is the Reciprocal Rank Fusion constant. 60 is the literature
// standard (Cormack, Buettcher, Clarke 2009) — tuned to dampen the
// dominance of rank-1 items so cross-list mid-rank items can still
// surface. Tweaking this is rarely worth it; tracked here as a const
// rather than a parameter so we don't accidentally A/B the same code
// path with different k values.
const rrfK = 60

// HybridSearch fans a query out to the FTS5 markdown engine + the
// episodic hybrid recall (BM25+vec over journal_entries) and merges
// the two ranked lists with Reciprocal Rank Fusion. Each hit keeps
// its native-engine identity in FTS / Episodic but is scored against
// the unified ordering so a caller can apply one global Limit.
//
// Either input is optional:
//
//   - engine == nil → FTS half is skipped (episodic-only fallback,
//     useful for callers without a markdown corpus)
//   - embedder == nil OR db == nil → episodic half is skipped
//     (FTS-only fallback, the pre-hybrid baseline)
//   - both nil → returns nil, nil
//
// The function never errors out on a single-engine failure: a logged
// nil result from one side still lets the other side's hits through.
// Caller-side fallbacks aren't needed.
//
// k=60 RRF; results sorted by combined RRF score descending, then
// truncated to q.Limit (default 10, max 50 — the same limits the
// sidecar /memory/search endpoint already enforced for FTS-only).
func HybridSearch(
	ctx context.Context,
	engine *Engine,
	db *sql.DB,
	embedder episodic.Embedder,
	q HybridQuery,
) ([]HybridHit, error) {
	if q.Limit <= 0 {
		q.Limit = 10
	}
	if q.Limit > 50 {
		q.Limit = 50
	}

	// Per-source rank table: key is the engine's identity for the
	// item (file:line for FTS, entry_id for episodic). Value is the
	// 1-based rank in that list.
	ftsRank := make(map[string]int)
	epiRank := make(map[string]int)

	var ftsHits []SearchResult
	if engine != nil {
		hits, err := engine.Search(ctx, q.Text, q.Limit)
		if err == nil {
			ftsHits = hits
			for i, h := range hits {
				ftsRank[ftsKey(h)] = i + 1
			}
		}
	}

	var epiHits []episodic.Hit
	if db != nil && embedder != nil && q.WorkspaceID != "" {
		hits, err := episodic.HybridRecall(ctx, db, embedder, episodic.Query{
			WorkspaceID: q.WorkspaceID,
			CrewID:      q.CrewID,
			AgentID:     q.AgentID,
			Scope:       q.Scope,
			QueryText:   q.Text,
			K:           q.Limit,
		})
		if err == nil {
			epiHits = hits
			for i, h := range hits {
				epiRank[h.EntryID] = i + 1
			}
		}
	}

	// Materialise unified hits with their RRF score. We score each
	// item exclusively against its own engine's rank — disjoint
	// corpora means an item from list A never has a rank in list B.
	// RRF over rank-only is still meaningful here: higher-ranked
	// items from any list out-score lower-ranked items from any
	// other list, with the rrfK constant flattening the curve.
	out := make([]HybridHit, 0, len(ftsHits)+len(epiHits))
	for _, h := range ftsHits {
		rank := ftsRank[ftsKey(h)]
		out = append(out, HybridHit{
			Source: "fts",
			Score:  rrfScore(rank),
			FTS:    cloneFTS(h),
		})
	}
	for _, h := range epiHits {
		hCopy := h
		rank := epiRank[h.EntryID]
		out = append(out, HybridHit{
			Source:   "episodic",
			Score:    rrfScore(rank),
			Episodic: &hCopy,
		})
	}

	// Stable sort by score descending; preserves intra-engine order
	// when two hits share a score (unlikely but cheap to guarantee).
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Score > out[j].Score
	})
	if len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

// ftsKey produces a stable identifier for a FTS hit so the RRF table
// has a hashable handle. File alone isn't quite right when the same
// file has multiple chunks; combining File + LineStart picks out the
// chunk identity, which is what the BM25 score was computed against.
func ftsKey(h SearchResult) string {
	// e.g. "AGENT.md:42" — line numbers are int but small, so
	// concatenation with a separator avoids any encoding ambiguity.
	return h.File + ":" + itoa(h.LineStart)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// cloneFTS returns a heap-allocated copy so the slice ranging callers
// don't keep a reference to a loop variable's address.
func cloneFTS(h SearchResult) *SearchResult { return &h }

// rrfScore is 1/(k+rank). rank is 1-based; rank=0 means "did not
// appear in this list" so the caller MUST skip rank=0 entries —
// keeping this as a tiny pure function so the call sites read clearly.
func rrfScore(rank int) float64 {
	if rank <= 0 {
		return 0
	}
	return 1.0 / float64(rrfK+rank)
}
