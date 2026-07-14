package eval

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// CorpusRow is one recorded keeper decision to replay: the exact prompt
// production sent plus the decision + risk it shipped. It is the DB-sourced
// input the replay driver turns into a scorer Row.
//
// Ground-truth caveat (see the package doc): Recorded is *what production
// shipped*, made by whatever model was configured then — the reference label,
// not verified truth. LoadCorpus therefore never fabricates a decision; it
// only reads settled rows.
type CorpusRow struct {
	ID          string
	RequestType string
	Prompt      string   // keeper_requests.ollama_prompt — replayed verbatim
	Recorded    Decision // normalized keeper_requests.decision (reference label)
	// RecordedRisk is keeper_requests.risk_score clamped to [1,10]; a NULL
	// risk on a settled row degrades to 1 (the clamp floor production already
	// applies on write), so scoring never sees an out-of-range value.
	RecordedRisk int
}

// corpusRequestTypes are the live-activity request types the harness scores.
// skill_review / memory_health / negative_learning are lower value for
// governance-model selection (spec §3), so they are excluded from the corpus.
//
// `behavior` is deliberately EXCLUDED for now even though M1 targets it. The
// replay path normalizes model output with gatekeeper.NormalizeRawResponse,
// whose closed set is ALLOW/DENY/ESCALATE (WARN → DENY). But the LIVE behavior
// path records decisions via classifyBehaviorDecision, which keeps WARN as a
// first-class outcome. Scoring behavior rows here would (a) mis-score any
// candidate that legitimately answers WARN as a DENY disagreement, and (b) the
// decision filter below already silently drops behavior rows recorded as WARN —
// both skew the governance-model selection for that one type. access/execute
// both map cleanly onto NormalizeRawResponse, so they stay. Follow-up: route
// behavior replay through classifyBehaviorDecision, then add it back.
var corpusRequestTypes = []string{"access", "execute"}

// LoadCorpus reads the recorded keeper_requests corpus for replay: rows with a
// non-empty ollama_prompt and a *settled* decision (ALLOW/DENY/ESCALATE —
// PENDING and NULL are excluded), filtered to the live-activity request types.
// Rows are ordered newest-first so a limited run scores the most recent
// production behavior. limit <= 0 means no limit.
//
// The query is intentionally server-wide: keeper_requests has no workspace_id
// column (it is crew-scoped), and the harness picks a single curated *global*
// default model, so scoping to one workspace would only shrink the corpus.
func LoadCorpus(ctx context.Context, db *sql.DB, limit int) ([]CorpusRow, error) {
	// keeper_requests.request_type is a closed CHECK set; build the IN list
	// from corpusRequestTypes so the two can't drift.
	placeholders := make([]string, len(corpusRequestTypes))
	args := make([]any, 0, len(corpusRequestTypes)+1)
	for i, rt := range corpusRequestTypes {
		placeholders[i] = "?"
		args = append(args, rt)
	}

	q := fmt.Sprintf(`
		SELECT id, request_type, ollama_prompt, decision, risk_score
		FROM keeper_requests
		WHERE request_type IN (%s)
		  AND ollama_prompt IS NOT NULL AND ollama_prompt != ''
		  AND UPPER(decision) IN ('ALLOW','DENY','ESCALATE')
		ORDER BY created_at DESC`, strings.Join(placeholders, ","))
	if limit > 0 {
		q += "\n\t\tLIMIT ?"
		args = append(args, limit)
	}

	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("load keeper corpus: %w", err)
	}
	defer rows.Close()

	var out []CorpusRow
	for rows.Next() {
		var (
			id, reqType, prompt, decision string
			risk                          sql.NullInt64
		)
		if err := rows.Scan(&id, &reqType, &prompt, &decision, &risk); err != nil {
			return nil, fmt.Errorf("scan keeper corpus row: %w", err)
		}
		out = append(out, CorpusRow{
			ID:           id,
			RequestType:  reqType,
			Prompt:       prompt,
			Recorded:     Decision(strings.ToUpper(decision)),
			RecordedRisk: clampRisk(risk),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate keeper corpus: %w", err)
	}
	return out, nil
}

// clampRisk maps a nullable recorded risk_score into the valid [1,10] range,
// mirroring the clamp the gatekeeper applies on write. A NULL or sub-floor
// value degrades to 1 rather than being dropped — the decision is what the
// scorer's safety metric turns on; risk MAE is secondary.
func clampRisk(n sql.NullInt64) int {
	if !n.Valid || n.Int64 < 1 {
		return 1
	}
	if n.Int64 > 10 {
		return 10
	}
	return int(n.Int64)
}
