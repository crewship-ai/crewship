package paymaster

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// tsLayout is the textual format we use when writing to TEXT timestamp
// columns. The same layout is used by internal/journal so cross-table joins
// stay collation-friendly without explicit conversions.
const tsLayout = "2006-01-02T15:04:05.000Z"

// Record persists one ledger row, then mirrors the same fact into the journal
// so the audit stream is the single source of truth. The two writes are NOT
// in a single transaction on purpose — the journal is async/batched, and
// blocking the cost write on a journal flush would defeat the batching. If
// the journal emit fails we still return success because the ledger row is
// the system of record for billing; the journal layer logs its own error.
//
// j may be nil (rare; used by tests that only care about the SQL side); when
// nil no journal entries are emitted. Production callers always pass a real
// emitter so cost activity is observable.
//
// Billing-mode handling (added migration v62):
//   - BillingFlatRate forces CostUSD to 0 and Confidence to Unknown on disk —
//     subscription calls have no marginal $ cost. The ledger row still serves
//     as audit ("this credential was used at this timestamp by this agent").
//     A `cost.incurred` journal entry is suppressed because no $ was incurred.
//   - BillingMetered (default) snapshots the rate card at write time so a
//     later pricing.go change can't rewrite history (Langfuse pattern). When
//     CostUSD is zero on input, Estimate fills it from the same snapshot.
func Record(ctx context.Context, db *sql.DB, j journal.Emitter, c Call) (CostRecord, error) {
	if db == nil {
		// nil db is an infrastructure bug, not a user-input fault — keep it
		// outside ErrInvalidRequest so handlers map it to 500.
		return CostRecord{}, fmt.Errorf("paymaster: nil db")
	}
	if c.Scope.WorkspaceID == "" {
		return CostRecord{}, fmt.Errorf("%w: workspace_id required", ErrInvalidRequest)
	}
	if c.Provider == "" || c.Model == "" {
		return CostRecord{}, fmt.Errorf("%w: provider and model required", ErrInvalidRequest)
	}

	if c.TS.IsZero() {
		c.TS = time.Now().UTC()
	}
	if c.BillingMode == "" {
		c.BillingMode = BillingMetered
	}

	// Snapshot the rate card and force flat-rate invariants. Done here (not
	// at the call site) so the same rules apply whether Record is called
	// directly or through Middleware.
	rate := RateCard(c.Provider, c.Model)
	if c.BillingMode == BillingFlatRate {
		c.CostUSD = 0
		c.Confidence = ConfidenceUnknown
	} else if c.Confidence == "" {
		c.Confidence = ConfidenceEstimate
	}

	id := newLedgerID()

	tagsJSON, err := encodeTags(c.Tags)
	if err != nil {
		return CostRecord{}, fmt.Errorf("paymaster: encode tags: %w", err)
	}

	const insertSQL = `INSERT INTO cost_ledger
		(id, workspace_id, crew_id, agent_id, mission_id, ts, provider, model,
		 input_tokens, output_tokens, cached_input_tokens, cache_creation_tokens,
		 cost_usd, tags,
		 billing_mode, quota_remaining_pct, quota_window, subscription_plan,
		 rate_input_per_m, rate_output_per_m, rate_cached_in_per_m, rate_cache_write_per_m,
		 cost_confidence)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	// Quota window is the sentinel for "did we get a rate-limit signal?".
	// When window is empty, both quota_window and quota_remaining_pct land
	// as NULL; once window is set, remaining_pct rides through verbatim
	// even if it's 0.0 — that's the exhausted-quota case EnforceQuota
	// surfaces and we'd lose it by collapsing to NULL.
	var quotaPct any
	if c.QuotaWindow != "" {
		quotaPct = c.QuotaRemainingPct
	}

	_, err = db.ExecContext(ctx, insertSQL,
		id,
		c.Scope.WorkspaceID,
		nullable(c.Scope.CrewID),
		nullable(c.Scope.AgentID),
		nullable(c.Scope.MissionID),
		c.TS.UTC().Format(tsLayout),
		c.Provider,
		c.Model,
		c.InputTokens,
		c.OutputTokens,
		c.CachedInputTokens,
		c.CacheCreationTokens,
		c.CostUSD,
		tagsJSON,
		string(c.BillingMode),
		quotaPct,
		nullableQuota(c.QuotaWindow),
		nullable(c.SubscriptionPlan),
		// Rate snapshot: always write the actual lookup result. Zero is
		// honest for ollama/local (free) and for unknown-provider lookups
		// (we couldn't price); NULL would erase that distinction. Per
		// CodeRabbit on this column being a Langfuse-pattern audit field.
		rate.InputPerM,
		rate.OutputPerM,
		rate.CachedInputPerM,
		rate.CacheWritePerM,
		string(c.Confidence),
	)
	if err != nil {
		return CostRecord{}, fmt.Errorf("paymaster: insert ledger: %w", err)
	}

	rec := CostRecord{ID: id, TS: c.TS.UTC(), Cost: c.CostUSD}

	if j != nil {
		emitLLMCall(ctx, j, c, rec)
		// Cache-hit path: when the call landed primarily on cached
		// tokens, emit a tighter llm.cache_hit entry too so the
		// Timeline / Cost views can break down "warm" vs "cold" calls
		// without re-deriving from llm.call payloads. Threshold of
		// 50% of input tokens being cached is the standard heuristic;
		// pure cache-creation calls (CacheCreationTokens > 0,
		// CachedInputTokens == 0) don't qualify because the cache
		// didn't help on this turn.
		if c.CachedInputTokens > 0 && c.InputTokens > 0 &&
			float64(c.CachedInputTokens)/float64(c.InputTokens) >= 0.5 {
			emitLLMCacheHit(ctx, j, c, rec)
		}
		// cost.incurred fires only for metered rows with non-zero $ — flat-
		// rate rows are not "money spent" from our perspective (sub already
		// covers them), and zero-cost metered calls (cache hits, local
		// models) don't need a redundant entry.
		if c.BillingMode == BillingMetered && c.CostUSD > 0 {
			emitCostIncurred(ctx, j, c, rec)
		}
	}

	return rec, nil
}

// emitLLMCall fires the journal entry that pairs with every ledger insert.
// Errors are swallowed (logged through the journal's own logger inside Emit)
// because the ledger row already succeeded — we don't want a journal hiccup
// to roll back accounting.
func emitLLMCall(ctx context.Context, j journal.Emitter, c Call, rec CostRecord) {
	summary := fmt.Sprintf("%s/%s: %d in / %d out tokens, $%.4f",
		c.Provider, c.Model, c.InputTokens, c.OutputTokens, c.CostUSD)
	if c.BillingMode == BillingFlatRate {
		// Flat-rate summaries explicitly say so — operators glancing at the
		// timeline shouldn't have to dig into payload to learn the row had
		// no $ tracking.
		plan := c.SubscriptionPlan
		if plan == "" {
			plan = "subscription"
		}
		summary = fmt.Sprintf("%s/%s: %d in / %d out tokens (flat-rate · %s)",
			c.Provider, c.Model, c.InputTokens, c.OutputTokens, plan)
	}
	payload := map[string]any{
		"provider":              c.Provider,
		"model":                 c.Model,
		"input_tokens":          c.InputTokens,
		"output_tokens":         c.OutputTokens,
		"cached_input_tokens":   c.CachedInputTokens,
		"cache_creation_tokens": c.CacheCreationTokens,
		"cost_usd":              c.CostUSD,
		"billing_mode":          string(c.BillingMode),
		"cost_confidence":       string(c.Confidence),
		"ledger_id":             rec.ID,
	}
	if c.SubscriptionPlan != "" {
		payload["subscription_plan"] = c.SubscriptionPlan
	}
	// QuotaWindow is the canonical "did we get a rate-limit signal?"
	// sentinel — same gate used in the SQL write path, in the sidecar
	// observer, and in the API quota-enforce branch. RemainingPct can
	// legitimately be 0 (exhausted-quota signal) so guarding on it
	// would silently drop the case EnforceQuota cares about most.
	if c.QuotaWindow != "" {
		payload["quota_remaining_pct"] = c.QuotaRemainingPct
		payload["quota_window"] = string(c.QuotaWindow)
	}
	_, _ = j.Emit(ctx, journal.Entry{
		WorkspaceID: c.Scope.WorkspaceID,
		CrewID:      c.Scope.CrewID,
		AgentID:     c.Scope.AgentID,
		MissionID:   c.Scope.MissionID,
		TS:          rec.TS,
		Type:        journal.EntryLLMCall,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorSystem,
		Summary:     summary,
		Payload:     payload,
		Refs:        map[string]any{"ledger_id": rec.ID},
	})
}

// emitLLMCacheHit fires when the prompt cache absorbed the bulk of the
// input tokens. Useful for cost dashboards (you can see how much the
// cache is saving over time) and for debugging an agent that's
// unexpectedly NOT hitting cache (e.g., trace_id changed every call so
// nothing reuses). Volume is bounded by the cache-hit ratio threshold
// in Record so we don't double-emit on every llm.call.
func emitLLMCacheHit(ctx context.Context, j journal.Emitter, c Call, rec CostRecord) {
	hitRatio := 0.0
	if c.InputTokens > 0 {
		hitRatio = float64(c.CachedInputTokens) / float64(c.InputTokens)
	}
	_, _ = j.Emit(ctx, journal.Entry{
		WorkspaceID: c.Scope.WorkspaceID,
		CrewID:      c.Scope.CrewID,
		AgentID:     c.Scope.AgentID,
		MissionID:   c.Scope.MissionID,
		TS:          rec.TS,
		Type:        journal.EntryLLMCacheHit,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorSystem,
		Summary: fmt.Sprintf("%s/%s cache hit: %d cached / %d input (%.0f%%)",
			c.Provider, c.Model, c.CachedInputTokens, c.InputTokens, hitRatio*100),
		Payload: map[string]any{
			"provider":            c.Provider,
			"model":               c.Model,
			"input_tokens":        c.InputTokens,
			"cached_input_tokens": c.CachedInputTokens,
			"hit_ratio":           hitRatio,
			"ledger_id":           rec.ID,
		},
		Refs: map[string]any{"ledger_id": rec.ID},
	})
}

// emitCostIncurred is split out so zero-cost calls (cache hits, local models)
// don't pollute the cost stream — those still land as llm.call entries but
// not as cost.incurred. The Crew Journal UI uses cost.incurred to drive its
// "money was spent" timeline.
func emitCostIncurred(ctx context.Context, j journal.Emitter, c Call, rec CostRecord) {
	_, _ = j.Emit(ctx, journal.Entry{
		WorkspaceID: c.Scope.WorkspaceID,
		CrewID:      c.Scope.CrewID,
		AgentID:     c.Scope.AgentID,
		MissionID:   c.Scope.MissionID,
		TS:          rec.TS,
		Type:        journal.EntryCostIncurred,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorSystem,
		Summary:     fmt.Sprintf("$%.4f spent on %s/%s", c.CostUSD, c.Provider, c.Model),
		Payload: map[string]any{
			"provider":  c.Provider,
			"model":     c.Model,
			"cost_usd":  c.CostUSD,
			"ledger_id": rec.ID,
		},
		Refs: map[string]any{"ledger_id": rec.ID},
	})
}

// encodeTags JSON-encodes the freeform tag map. Empty/nil maps serialize to
// "{}" so the column's NOT NULL DEFAULT '{}' invariant holds even if a caller
// passes nil — matches the journal package's payloadJSON behaviour.
func encodeTags(tags map[string]any) (string, error) {
	if len(tags) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(tags)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// nullable converts an empty string to a SQL NULL so indexed "X IS NULL"
// queries on optional scope columns stay cheap. Mirrors the helper in the
// journal package on purpose — same database, same convention.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullableQuota mirrors nullable for the typed QuotaWindow enum.
func nullableQuota(q QuotaWindow) any {
	if q == "" {
		return nil
	}
	return string(q)
}

// newLedgerID generates a short collision-free ID for a ledger row. Same
// scheme as journal IDs: 64 random bits hex-encoded with a prefix that makes
// IDs self-identifying when grepping logs.
func newLedgerID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return "cl_" + hex.EncodeToString(b[:])
}
