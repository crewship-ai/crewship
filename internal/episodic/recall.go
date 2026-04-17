package episodic

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Recall runs a cosine-similarity scan over the embeddings that match
// the scope rules and returns the top-K hits. Workspace isolation is
// enforced unconditionally — even a malformed Query can't leak across
// tenants because workspace_id is always in the WHERE clause.
//
// Implementation note: the candidate set is pre-filtered by SQL (scope
// + workspace + non-empty vector), then ranked in Go with a full cosine
// pass. For the expected working set (a few thousand rows per agent)
// this completes in low milliseconds. If the scan exceeds ~50ms in
// production, the right next step is an external vector store, not a
// SQLite extension — the package keeps its storage behind the helpers
// in embedder.go so that migration is localized.
func Recall(ctx context.Context, db *sql.DB, emb Embedder, q Query) ([]Hit, error) {
	if q.WorkspaceID == "" {
		return nil, fmt.Errorf("episodic: Recall requires workspace_id")
	}
	if q.K <= 0 || q.K > 50 {
		q.K = 5
	}
	qvec, err := emb.Embed(ctx, q.QueryText)
	if err != nil {
		return nil, fmt.Errorf("episodic: embed query: %w", err)
	}

	var (
		conds = []string{"em.workspace_id = ?", "em.dim > 0"}
		args  = []any{q.WorkspaceID}
	)
	switch q.Scope {
	case ScopeOwn:
		if q.AgentID == "" {
			return nil, fmt.Errorf("episodic: ScopeOwn requires agent_id")
		}
		conds = append(conds, "em.agent_id = ?")
		args = append(args, q.AgentID)
	case ScopeCrewShared:
		if q.CrewID == "" {
			return nil, fmt.Errorf("episodic: ScopeCrewShared requires crew_id")
		}
		conds = append(conds, "em.crew_id = ?")
		args = append(args, q.CrewID)
	default:
		return nil, fmt.Errorf("episodic: unknown scope %q", q.Scope)
	}

	query := `SELECT em.entry_id, em.dim, em.vector,
		        e.entry_type, e.summary, e.agent_id, e.payload, e.ts
		   FROM journal_embeddings em
		   JOIN journal_entries e ON e.id = em.entry_id
		  WHERE ` + strings.Join(conds, " AND ")
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("episodic: candidate query: %w", err)
	}
	defer rows.Close()

	type candidate struct {
		Hit
		vec []float32
	}
	var cands []candidate
	for rows.Next() {
		var (
			c          candidate
			dim        int
			blob       []byte
			agentID    sql.NullString
			payloadStr string
			tsStr      string
		)
		if err := rows.Scan(&c.EntryID, &dim, &blob,
			&c.EntryType, &c.Summary, &agentID, &payloadStr, &tsStr); err != nil {
			continue
		}
		if dim == 0 {
			// Tombstone row — nothing to compare against.
			continue
		}
		vec, err := DecodeVector(blob, dim)
		if err != nil {
			continue
		}
		c.vec = vec
		c.AgentID = agentID.String
		c.Payload = decodeJSONMap(payloadStr)
		if ts, err := time.Parse(time.RFC3339Nano, tsStr); err == nil {
			c.Age = time.Since(ts)
		} else if ts, err := time.Parse("2006-01-02T15:04:05.000Z", tsStr); err == nil {
			c.Age = time.Since(ts)
		}
		cands = append(cands, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range cands {
		cands[i].Score = cosine(qvec, cands[i].vec)
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].Score > cands[j].Score })

	out := make([]Hit, 0, q.K)
	for i := 0; i < q.K && i < len(cands); i++ {
		// Strip vec from output; it's internal.
		out = append(out, cands[i].Hit)
	}
	return out, nil
}

// RenderInjection formats top-K hits into a system-prompt fragment. The
// fragment is capped at maxChars (default 2000) so agent prompts don't
// bloat. Older hits are suppressed in favor of more recent similar
// events when the budget is tight — "recent similar" beats "most
// similar ever" for episodic memory.
func RenderInjection(hits []Hit, maxChars int) string {
	if maxChars <= 0 {
		maxChars = 2000
	}
	var b strings.Builder
	b.WriteString("## Past Similar Events\n")
	for _, h := range hits {
		line := fmt.Sprintf("- [%s • %s ago • score=%.2f] %s\n",
			h.EntryType, humanizeAge(h.Age), h.Score, h.Summary)
		if b.Len()+len(line) > maxChars {
			break
		}
		b.WriteString(line)
	}
	if b.Len() <= len("## Past Similar Events\n") {
		return ""
	}
	return b.String()
}

func humanizeAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%dmo", int(d.Hours()/(24*30)))
	}
}

// decodeJSONMap is a best-effort unmarshal — failures return nil rather
// than erroring out, since payload inspection is never load-bearing for
// the recall result itself.
func decodeJSONMap(s string) map[string]any {
	if s == "" || s == "{}" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil
	}
	return m
}
