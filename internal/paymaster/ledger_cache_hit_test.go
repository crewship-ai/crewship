package paymaster

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// ---------------------------------------------------------------------------
// ledger.go — emitLLMCacheHit.
//
// Fires alongside an llm.call when the prompt cache absorbed enough of
// the input tokens that the cost dashboard wants to surface it as a
// distinct event ("you saved money here"). Volume is bounded by a
// ratio-threshold gate in Record, so this function itself is dumb and
// assumes its caller already decided to emit.
//
// The contract that matters:
//   1. Entry type is journal.EntryLLMCacheHit (the dashboard queries
//      by this type — a typo would silently make the savings panel
//      empty)
//   2. Severity = Info, ActorType = System (cache hits are background
//      observations, not user actions)
//   3. hit_ratio = CachedInputTokens / InputTokens, GUARDED against
//      division-by-zero when InputTokens is 0 (regression here would
//      panic in a hot path)
//   4. Scope (workspace/crew/agent/mission) propagates so the entry
//      lands on the right Crew Journal pages
//   5. Payload carries the ledger_id so the entry joins back to the
//      cost_ledger row that triggered it
// ---------------------------------------------------------------------------

func TestEmitLLMCacheHit_HappyPath_EmitsEntryWithFullPayload(t *testing.T) {
	em := &fakeEmitter{}
	ts := time.Date(2026, 5, 18, 10, 0, 0, 0, time.UTC)
	c := Call{
		Scope: Scope{
			WorkspaceID: "ws-1",
			CrewID:      "crew-1",
			AgentID:     "agent-1",
			MissionID:   "mission-1",
		},
		Provider:          "anthropic",
		Model:             "claude-3-5-sonnet",
		InputTokens:       1000,
		CachedInputTokens: 800,
	}
	rec := CostRecord{ID: "ledger-abc", TS: ts}

	emitLLMCacheHit(context.Background(), em, c, rec)

	hits := em.byType(journal.EntryLLMCacheHit)
	if len(hits) != 1 {
		t.Fatalf("emitted %d entries of type %q, want 1", len(hits), journal.EntryLLMCacheHit)
	}
	e := hits[0]

	if e.WorkspaceID != "ws-1" {
		t.Errorf("WorkspaceID = %q", e.WorkspaceID)
	}
	if e.CrewID != "crew-1" {
		t.Errorf("CrewID = %q", e.CrewID)
	}
	if e.AgentID != "agent-1" {
		t.Errorf("AgentID = %q", e.AgentID)
	}
	if e.MissionID != "mission-1" {
		t.Errorf("MissionID = %q", e.MissionID)
	}
	if !e.TS.Equal(ts) {
		t.Errorf("TS = %v, want %v (must use rec.TS, not time.Now())", e.TS, ts)
	}
	if e.Severity != journal.SeverityInfo {
		t.Errorf("Severity = %q, want info (cache hit is a background observation)", e.Severity)
	}
	if e.ActorType != journal.ActorSystem {
		t.Errorf("ActorType = %q, want system", e.ActorType)
	}

	if e.Payload["provider"] != "anthropic" {
		t.Errorf("payload.provider = %v", e.Payload["provider"])
	}
	if e.Payload["model"] != "claude-3-5-sonnet" {
		t.Errorf("payload.model = %v", e.Payload["model"])
	}
	if e.Payload["input_tokens"] != int64(1000) {
		t.Errorf("payload.input_tokens = %v, want int64(1000)", e.Payload["input_tokens"])
	}
	if e.Payload["cached_input_tokens"] != int64(800) {
		t.Errorf("payload.cached_input_tokens = %v, want int64(800)", e.Payload["cached_input_tokens"])
	}

	ratio, ok := e.Payload["hit_ratio"].(float64)
	if !ok {
		t.Fatalf("payload.hit_ratio not a float64: %T %v", e.Payload["hit_ratio"], e.Payload["hit_ratio"])
	}
	if ratio < 0.799 || ratio > 0.801 {
		t.Errorf("hit_ratio = %v, want ~0.8 (800/1000)", ratio)
	}

	if e.Payload["ledger_id"] != "ledger-abc" {
		t.Errorf("payload.ledger_id = %v, want \"ledger-abc\"", e.Payload["ledger_id"])
	}
	if e.Refs["ledger_id"] != "ledger-abc" {
		t.Errorf("refs.ledger_id = %v, want \"ledger-abc\" (join key back to cost_ledger)", e.Refs["ledger_id"])
	}
}

func TestEmitLLMCacheHit_ZeroInputTokens_NoDivisionByZeroPanic(t *testing.T) {
	// Defensive: InputTokens == 0 would be a 0/0 division → NaN at
	// best, panic at worst. Source guards with `if c.InputTokens > 0`.
	// Pin so a future "simpler one-liner" refactor that drops the guard
	// surfaces here, not at the next zero-token sidecar emit.
	em := &fakeEmitter{}
	c := Call{
		Scope:             Scope{WorkspaceID: "ws-1"},
		Provider:          "openai",
		Model:             "gpt-4",
		InputTokens:       0,
		CachedInputTokens: 0,
	}
	rec := CostRecord{ID: "ledger-zero", TS: time.Now()}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("emitLLMCacheHit panicked on zero InputTokens: %v", r)
		}
	}()
	emitLLMCacheHit(context.Background(), em, c, rec)

	hits := em.byType(journal.EntryLLMCacheHit)
	if len(hits) != 1 {
		t.Fatalf("emitted %d entries, want 1", len(hits))
	}
	ratio, _ := hits[0].Payload["hit_ratio"].(float64)
	if ratio != 0.0 {
		t.Errorf("zero-input hit_ratio = %v, want 0.0", ratio)
	}
}

func TestEmitLLMCacheHit_SummaryFormat(t *testing.T) {
	// The Summary string is what shows in the Crew Journal timeline.
	// Pin its shape so a UI scraping pattern (looking for "%" suffix
	// or the "cache hit:" marker) stays stable.
	em := &fakeEmitter{}
	c := Call{
		Scope:             Scope{WorkspaceID: "ws-1"},
		Provider:          "anthropic",
		Model:             "claude-3-5-sonnet",
		InputTokens:       2000,
		CachedInputTokens: 1500,
	}
	emitLLMCacheHit(context.Background(), em, c, CostRecord{ID: "lx", TS: time.Now()})

	sum := em.byType(journal.EntryLLMCacheHit)[0].Summary
	for _, fragment := range []string{
		"anthropic/claude-3-5-sonnet",
		"cache hit:",
		"1500 cached / 2000 input",
		"75%", // 1500/2000 = 75% formatted as %.0f%%
	} {
		if !strings.Contains(sum, fragment) {
			t.Errorf("Summary = %q, missing %q", sum, fragment)
		}
	}
}

func TestEmitLLMCacheHit_FullCacheHit_100Percent(t *testing.T) {
	// All-cache case (CachedInputTokens == InputTokens). Pin the
	// 100% boundary explicitly because Sprintf %.0f rounds and a
	// regression to %.1f or %.0f-with-multiplication-bug would silently
	// shift the dashboard's "100%" badge.
	em := &fakeEmitter{}
	c := Call{
		Scope:             Scope{WorkspaceID: "ws-1"},
		Provider:          "anthropic",
		Model:             "claude",
		InputTokens:       500,
		CachedInputTokens: 500,
	}
	emitLLMCacheHit(context.Background(), em, c, CostRecord{ID: "lx"})

	e := em.byType(journal.EntryLLMCacheHit)[0]
	if r := e.Payload["hit_ratio"].(float64); r != 1.0 {
		t.Errorf("hit_ratio = %v, want 1.0", r)
	}
	if !strings.Contains(e.Summary, "100%") {
		t.Errorf("Summary = %q, want \"100%%\" for full cache hit", e.Summary)
	}
}

func TestEmitLLMCacheHit_NoCache_ZeroRatio(t *testing.T) {
	// CachedInputTokens == 0 with InputTokens > 0 → hit_ratio = 0.
	// In practice Record's gate wouldn't fire emit for a zero-cache
	// call, but defensively pin the math anyway — a downstream consumer
	// summing payload.hit_ratio assumes the field is always present.
	em := &fakeEmitter{}
	c := Call{
		Scope:             Scope{WorkspaceID: "ws-1"},
		Provider:          "anthropic",
		Model:             "claude",
		InputTokens:       1000,
		CachedInputTokens: 0,
	}
	emitLLMCacheHit(context.Background(), em, c, CostRecord{ID: "lx"})

	e := em.byType(journal.EntryLLMCacheHit)[0]
	if r := e.Payload["hit_ratio"].(float64); r != 0.0 {
		t.Errorf("hit_ratio = %v, want 0.0", r)
	}
}

func TestEmitLLMCacheHit_PartialScope_PropagatesNonEmptyFields(t *testing.T) {
	// Scope fields below WorkspaceID are optional. A workspace-scoped
	// cache hit (no crew/agent/mission) must still emit cleanly with
	// empty strings on the unused fields — pin so a downstream join
	// on CrewID doesn't get a stray non-empty value.
	em := &fakeEmitter{}
	c := Call{
		Scope:             Scope{WorkspaceID: "ws-only"},
		Provider:          "x",
		Model:             "y",
		InputTokens:       10,
		CachedInputTokens: 5,
	}
	emitLLMCacheHit(context.Background(), em, c, CostRecord{ID: "lx"})

	e := em.byType(journal.EntryLLMCacheHit)[0]
	if e.WorkspaceID != "ws-only" {
		t.Errorf("WorkspaceID = %q", e.WorkspaceID)
	}
	if e.CrewID != "" {
		t.Errorf("CrewID = %q, want empty", e.CrewID)
	}
	if e.AgentID != "" {
		t.Errorf("AgentID = %q, want empty", e.AgentID)
	}
	if e.MissionID != "" {
		t.Errorf("MissionID = %q, want empty", e.MissionID)
	}
}

func TestEmitLLMCacheHit_EmitterErrorSwallowed(t *testing.T) {
	// Source uses `_, _ = j.Emit(...)` — emit errors are explicitly
	// swallowed because cache-hit emission is best-effort observability.
	// Pin that an errEmitter implementation doesn't panic / propagate.
	em := &errOnlyEmitter{}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("emitLLMCacheHit panicked when emitter returned error: %v", r)
		}
	}()
	emitLLMCacheHit(context.Background(), em, Call{
		Scope:             Scope{WorkspaceID: "ws-1"},
		InputTokens:       100,
		CachedInputTokens: 50,
	}, CostRecord{ID: "lx"})
}

// errOnlyEmitter returns an error from every Emit; the cache-hit path
// explicitly swallows it so the surrounding LLM record never fails on
// best-effort observability.
type errOnlyEmitter struct{}

func (errOnlyEmitter) Emit(_ context.Context, _ journal.Entry) (string, error) {
	return "", errSentinel
}
func (errOnlyEmitter) Flush(_ context.Context) error { return nil }

var errSentinel = errEmit("emit denied")

type errEmit string

func (e errEmit) Error() string { return string(e) }
