package episodic

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
)

// HybridRecall combines two retrieval signals — dense cosine (what
// Recall does today) and sparse BM25 via SQLite FTS5 — and fuses the
// two ranked lists with Reciprocal Rank Fusion (RRF). The output is
// the top-K best-scoring hits across both lanes.
//
// Why: pure cosine misses keyword-exact recall ("deploy-42" as a
// literal substring shows up with low cosine when the query is
// "what went wrong in deploy-42"), and pure BM25 misses semantic
// paraphrases ("rollback failed" vs "reverting didn't work"). RRF is
// parameter-free and the current default in production RAG stacks
// (Langchain Cohere, Weaviate, Qdrant) — cheap to implement, proven
// to lift recall quality by ~20% over either lane alone.
//
// This function is safe to call as a drop-in for Recall. If the FTS5
// virtual table is missing (pre-migration-55 DB), the BM25 lane
// returns an empty set and the caller still gets dense-only results.
func HybridRecall(ctx context.Context, db *sql.DB, emb Embedder, q Query) ([]Hit, error) {
	if q.WorkspaceID == "" {
		return nil, fmt.Errorf("episodic: HybridRecall requires workspace_id")
	}
	if q.K <= 0 || q.K > 50 {
		q.K = 5
	}

	// Run both lanes. On dense-lane error we fail the whole call (the
	// cosine path is the baseline correctness); on sparse-lane error
	// we degrade to dense-only with a logged warning. This matches
	// the production reality: the embedder is always expected to work
	// (Ollama or fallback), but FTS5 may be unavailable on an older
	// DB that hasn't run migration 55 yet.
	denseK := q.K * 3 // over-fetch so RRF has room to re-rank
	if denseK < 20 {
		denseK = 20
	}
	denseQ := q
	denseQ.K = denseK
	denseHits, err := Recall(ctx, db, emb, denseQ)
	if err != nil {
		return nil, fmt.Errorf("episodic: dense lane: %w", err)
	}

	sparseHits, sparseErr := bm25Lane(ctx, db, q, denseK)
	if sparseErr != nil {
		// Log but don't fail — caller still gets the dense results.
		slog.Default().Warn("episodic: BM25 lane failed, falling back to dense only",
			"err", sparseErr, "workspace_id", q.WorkspaceID)
		sparseHits = nil
	}

	fused := rrfFuse(denseHits, sparseHits, q.K)
	return fused, nil
}

// bm25Lane runs a BM25 query over journal_entries_fts and returns the
// top-K matches as Hit structs. The FTS5 bm25() function is built into
// SQLite 3.34+ (we require 3.37+ elsewhere for JSON1, so this is safe).
//
// The query is sanitised through escapeFTSQuery because FTS5 has its
// own mini-language (prefix, phrase, NEAR, etc.) and a raw user query
// with quotes or AND/OR keywords would either parse-error or return
// surprising results. We lowercase + strip special chars + re-quote
// each word with prefix matching to get a forgiving "contains any of
// these words" behaviour.
func bm25Lane(ctx context.Context, db *sql.DB, q Query, limit int) ([]Hit, error) {
	matchTerm := escapeFTSQuery(q.QueryText)
	if matchTerm == "" {
		return nil, nil
	}

	var (
		conds = []string{"e.workspace_id = ?"}
		args  = []any{q.WorkspaceID}
	)
	switch q.Scope {
	case ScopeOwn:
		if q.AgentID == "" {
			return nil, fmt.Errorf("episodic: ScopeOwn requires agent_id")
		}
		conds = append(conds, "e.agent_id = ?")
		args = append(args, q.AgentID)
	case ScopeCrewShared:
		if q.CrewID == "" {
			return nil, fmt.Errorf("episodic: ScopeCrewShared requires crew_id")
		}
		conds = append(conds, "e.crew_id = ?")
		args = append(args, q.CrewID)
	default:
		return nil, fmt.Errorf("episodic: unknown scope %q", q.Scope)
	}

	query := `SELECT e.id, e.entry_type, e.summary, e.agent_id, e.payload, e.ts,
	                 bm25(journal_entries_fts) AS score
	            FROM journal_entries_fts
	            JOIN journal_entries e ON e.rowid = journal_entries_fts.rowid
	           WHERE journal_entries_fts MATCH ?
	             AND ` + strings.Join(conds, " AND ") + `
	           ORDER BY score ASC
	           LIMIT ?`
	allArgs := append([]any{matchTerm}, args...)
	allArgs = append(allArgs, limit)
	rows, err := db.QueryContext(ctx, query, allArgs...)
	if err != nil {
		return nil, fmt.Errorf("bm25: query: %w", err)
	}
	defer rows.Close()

	var out []Hit
	for rows.Next() {
		var (
			h             Hit
			agentID       sql.NullString
			payloadStr    string
			tsStr         string
			bm25Score     float64
			entryTypeStr  string
		)
		if err := rows.Scan(&h.EntryID, &entryTypeStr, &h.Summary, &agentID, &payloadStr, &tsStr, &bm25Score); err != nil {
			continue
		}
		h.AgentID = agentID.String
		h.EntryType = entryTypeStr
		h.Payload = decodeJSONMap(payloadStr)
		if ts, perr := time.Parse(time.RFC3339Nano, tsStr); perr == nil {
			h.Age = time.Since(ts)
		} else if ts, perr := time.Parse("2006-01-02T15:04:05.000Z", tsStr); perr == nil {
			h.Age = time.Since(ts)
		}
		// bm25() returns negative scores (lower = better match). Normalise
		// to a [0,1] range so downstream logs can read the number without
		// surprise; RRF only needs the rank order so the absolute value
		// doesn't matter for fusion.
		h.Score = 1.0 / (1.0 + -bm25Score)
		out = append(out, h)
	}
	return out, rows.Err()
}

// escapeFTSQuery turns a free-form query into an FTS5-safe MATCH
// expression. Each alphanumeric word becomes a prefix token ("deploy" →
// `deploy*`) joined with OR (FTS5 default is implicit AND which is
// too strict for human queries). Empty / pathological input returns
// the empty string so the caller can skip the query entirely.
func escapeFTSQuery(s string) string {
	if s == "" {
		return ""
	}
	var out []string
	var cur strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			cur.WriteRune(r)
		default:
			if cur.Len() > 1 {
				out = append(out, cur.String()+"*")
			}
			cur.Reset()
		}
	}
	if cur.Len() > 1 {
		out = append(out, cur.String()+"*")
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, " OR ")
}

// rrfFuse combines two ranked lists using Reciprocal Rank Fusion. The
// RRF score of an item is sum(1 / (k + rank)) across every ranking
// it appears in, where k is a smoothing constant (60 is the standard
// choice from the original Cormack 2009 paper and every subsequent
// implementation).
//
// The implementation is straightforward: build a map from EntryID to
// accumulated RRF score, then sort by descending score. Hits missing
// from one lane only get the other lane's rank, which is the correct
// RRF behaviour (missing from a lane = max possible rank = smallest
// contribution).
func rrfFuse(dense, sparse []Hit, topK int) []Hit {
	const k = 60.0
	scores := map[string]float64{}
	byID := map[string]Hit{}
	for rank, h := range dense {
		scores[h.EntryID] += 1.0 / (k + float64(rank+1))
		if _, ok := byID[h.EntryID]; !ok {
			byID[h.EntryID] = h
		}
	}
	for rank, h := range sparse {
		scores[h.EntryID] += 1.0 / (k + float64(rank+1))
		if _, ok := byID[h.EntryID]; !ok {
			byID[h.EntryID] = h
		}
	}
	type scored struct {
		h Hit
		s float64
	}
	fused := make([]scored, 0, len(scores))
	for id, s := range scores {
		fused = append(fused, scored{h: byID[id], s: s})
	}
	sort.Slice(fused, func(i, j int) bool { return fused[i].s > fused[j].s })
	if len(fused) > topK {
		fused = fused[:topK]
	}
	out := make([]Hit, 0, len(fused))
	for _, f := range fused {
		out = append(out, f.h)
	}
	return out
}
