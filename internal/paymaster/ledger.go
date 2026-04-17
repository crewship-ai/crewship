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
func Record(ctx context.Context, db *sql.DB, j journal.Emitter, c Call) (CostRecord, error) {
	if db == nil {
		return CostRecord{}, fmt.Errorf("paymaster: nil db")
	}
	if c.Scope.WorkspaceID == "" {
		return CostRecord{}, fmt.Errorf("paymaster: workspace_id required")
	}
	if c.Provider == "" || c.Model == "" {
		return CostRecord{}, fmt.Errorf("paymaster: provider and model required")
	}

	if c.TS.IsZero() {
		c.TS = time.Now().UTC()
	}
	id := newLedgerID()

	tagsJSON, err := encodeTags(c.Tags)
	if err != nil {
		return CostRecord{}, fmt.Errorf("paymaster: encode tags: %w", err)
	}

	const insertSQL = `INSERT INTO cost_ledger
		(id, workspace_id, crew_id, agent_id, mission_id, ts, provider, model,
		 input_tokens, output_tokens, cached_input_tokens, cache_creation_tokens,
		 cost_usd, tags)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

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
	)
	if err != nil {
		return CostRecord{}, fmt.Errorf("paymaster: insert ledger: %w", err)
	}

	rec := CostRecord{ID: id, TS: c.TS.UTC(), Cost: c.CostUSD}

	if j != nil {
		emitLLMCall(ctx, j, c, rec)
		if c.CostUSD > 0 {
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
	_, _ = j.Emit(ctx, journal.Entry{
		WorkspaceID: c.Scope.WorkspaceID,
		CrewID:      c.Scope.CrewID,
		AgentID:     c.Scope.AgentID,
		MissionID:   c.Scope.MissionID,
		TS:          rec.TS,
		Type:        journal.EntryLLMCall,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorSystem,
		Summary: fmt.Sprintf("%s/%s: %d in / %d out tokens, $%.4f",
			c.Provider, c.Model, c.InputTokens, c.OutputTokens, c.CostUSD),
		Payload: map[string]any{
			"provider":              c.Provider,
			"model":                 c.Model,
			"input_tokens":          c.InputTokens,
			"output_tokens":         c.OutputTokens,
			"cached_input_tokens":   c.CachedInputTokens,
			"cache_creation_tokens": c.CacheCreationTokens,
			"cost_usd":              c.CostUSD,
			"ledger_id":             rec.ID,
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

// newLedgerID generates a short collision-free ID for a ledger row. Same
// scheme as journal IDs: 64 random bits hex-encoded with a prefix that makes
// IDs self-identifying when grepping logs.
func newLedgerID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "cl_" + hex.EncodeToString(b[:])
}
