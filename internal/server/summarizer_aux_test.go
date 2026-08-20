package server

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/consolidate"
	"github.com/crewship-ai/crewship/internal/llm"
)

// The property this file exists for (#1695): the model an operator points the
// CURATOR slot at is the model memory consolidation calls.
//
// Every surface said it already did — internal/llm/aux.go's "memory
// consolidation, skill review", keepercfg.AuxLabels' and the aux-status
// endpoint's "Skill review + memory consolidation", the Judge models card that
// renders them. The consolidator read none of it: server bootstrap built the
// summariser straight from KEEPER_OLLAMA_URL + KEEPER_MODEL, once, and
// internal/consolidate imports no LLM package at all. Repointing curator moved
// skill review and left consolidation where it was, with nothing saying so.

// recordingSummarizer is the KEEPER_* fallback client, recording that it (and
// not the slot) was the one asked.
type recordingSummarizer struct {
	out    string
	calls  int
	prompt string
}

func (s *recordingSummarizer) Summarize(ctx context.Context, prompt string) (string, error) {
	s.calls++
	s.prompt = prompt
	return s.out, nil
}

// slotProvider is a fake llm.Provider standing in for whatever the curator
// slot resolves to, recording the model it was asked for.
type slotProvider struct {
	name      string
	out       string
	err       error
	calls     int
	lastModel string
}

func (p *slotProvider) Complete(ctx context.Context, req llm.Request) (*llm.Response, error) {
	p.calls++
	p.lastModel = req.Model
	if p.err != nil {
		return nil, p.err
	}
	return &llm.Response{Content: p.out}, nil
}

func (p *slotProvider) Stream(ctx context.Context, req llm.Request, h func(llm.StreamEvent) error) (*llm.Response, error) {
	return nil, nil
}

func (p *slotProvider) Name() string {
	if p.name == "" {
		return "slot"
	}
	return p.name
}

func testAuxResolver(p llm.Provider, model string, budget time.Duration) auxResolver {
	return func() (llm.Provider, string, time.Duration) { return p, model, budget }
}

// The consolidation call goes to the curator slot's provider and model, not to
// the KEEPER_* client built at boot — even when that client exists, which is
// the configuration where the bug was invisible.
func TestAuxSummarizer_ConsolidationCallsTheCuratorSlot(t *testing.T) {
	slot := &slotProvider{out: `{"rules":[]}`}
	fallback := &recordingSummarizer{out: "from KEEPER_*"}

	s := newAuxSummarizer(testAuxResolver(slot, "curator-model", 0), nil, fallback, slog.Default())
	if s == nil {
		t.Fatal("no summarizer wired for a buildable curator slot")
	}

	out, err := s.Summarize(context.Background(), "prompt")
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if slot.calls != 1 {
		t.Fatalf("curator slot provider calls = %d, want 1 — consolidation did not go through the slot", slot.calls)
	}
	if slot.lastModel != "curator-model" {
		t.Errorf("model = %q, want the curator slot's curator-model", slot.lastModel)
	}
	if fallback.calls != 0 {
		t.Errorf("the KEEPER_* fallback was called %d times while the slot was buildable", fallback.calls)
	}
	if out != `{"rules":[]}` {
		t.Errorf("output = %q, want the slot provider's response", out)
	}
}

// The slot is resolved per consolidation, not captured at boot. The
// consolidator is built once and lives for the process, so a captured pair is
// a pair the operator's edit could only reach with a restart — #1556's bug,
// which #1669's per-sweep resolver had already avoided for the OTHER consumer
// of this same slot.
func TestAuxSummarizer_ResolvesTheSlotOnEveryConsolidation(t *testing.T) {
	booted := &slotProvider{out: "first"}
	overridden := &slotProvider{out: "second"}

	inForce := llm.Provider(booted)
	model := "boot-model"
	s := newAuxSummarizer(func() (llm.Provider, string, time.Duration) { return inForce, model, 0 },
		nil, nil, slog.Default())
	if s == nil {
		t.Fatal("no summarizer wired")
	}

	if _, err := s.Summarize(context.Background(), "p1"); err != nil {
		t.Fatalf("first summarize: %v", err)
	}
	if booted.calls != 1 {
		t.Fatalf("boot provider calls = %d, want 1", booted.calls)
	}

	// The operator repoints the curator slot. Nothing is re-wired, nothing
	// restarts.
	inForce = overridden
	model = "live-model"

	if _, err := s.Summarize(context.Background(), "p2"); err != nil {
		t.Fatalf("second summarize: %v", err)
	}
	if overridden.calls != 1 {
		t.Errorf("override provider calls = %d, want 1 — the summariser captured the boot-time slot", overridden.calls)
	}
	if overridden.lastModel != "live-model" {
		t.Errorf("model = %q, want live-model", overridden.lastModel)
	}
	if booted.calls != 1 {
		t.Errorf("the second consolidation went to the boot provider again (calls = %d)", booted.calls)
	}
}

// A slot with nothing buildable behind it degrades to the local KEEPER_*
// client rather than losing consolidation — the same fallback
// buildAuxGatekeeper gives the four evaluators, and the reason an install
// running a local judge with no API key keeps working.
func TestAuxSummarizer_UnbuildableSlotFallsBackToTheKeeperModel(t *testing.T) {
	fallback := &recordingSummarizer{out: "from KEEPER_*"}
	s := newAuxSummarizer(testAuxResolver(nil, "", 0), nil, fallback, slog.Default())
	if s == nil {
		t.Fatal("an install with a KEEPER_* model must keep consolidation")
	}

	out, err := s.Summarize(context.Background(), "prompt")
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if fallback.calls != 1 {
		t.Errorf("fallback calls = %d, want 1", fallback.calls)
	}
	if out != "from KEEPER_*" {
		t.Errorf("output = %q, want the fallback's", out)
	}
}

// Consolidation is no longer Ollama-only: an install with an API key behind
// the curator slot and no KEEPER_* model gets a summariser, where before it
// got nil and logged "memory consolidation disabled" while the aux-status
// endpoint reported curator as configured and healthy.
func TestAuxSummarizer_SlotAloneEnablesConsolidation(t *testing.T) {
	slot := &slotProvider{out: `{"rules":[]}`}
	s := newAuxSummarizer(testAuxResolver(slot, "claude-haiku-4-5", 0), nil, nil, slog.Default())
	if s == nil {
		t.Fatal("a buildable curator slot with no KEEPER_* model must still enable consolidation")
	}
	if _, err := s.Summarize(context.Background(), "prompt"); err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if slot.calls != 1 {
		t.Errorf("slot provider calls = %d, want 1", slot.calls)
	}
}

// Neither source means nil, which is what consolidate.Consolidator reads as
// "pin-snapshot path only" and the manual-trigger endpoint reports as "no
// summarizer configured". A non-nil client that always errors would turn a
// documented off state into a per-tick failure.
func TestAuxSummarizer_NoSlotAndNoFallbackIsNil(t *testing.T) {
	cases := []struct {
		name     string
		resolve  auxResolver
		fallback consolidate.SummarizerClient
	}{
		{name: "no resolver at all (the Router failed to build)", resolve: nil, fallback: nil},
		{name: "a resolver that resolves nothing", resolve: testAuxResolver(nil, "", 0), fallback: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if s := newAuxSummarizer(tc.resolve, nil, tc.fallback, slog.Default()); s != nil {
				t.Error("got a summarizer with nothing behind it; consolidation would fail per tick instead of skipping")
			}
		})
	}
}

// The resolved provider is wrapped with the same middleware stack the
// boot-time summariser had (cost ledger, lookout, telemetry). The Router hands
// back a bare provider, so consolidation dropping the wrapper would mean paid
// consolidation calls that never reach the ledger.
func TestAuxSummarizer_WrapsTheResolvedProvider(t *testing.T) {
	slot := &slotProvider{out: "ok"}
	wrapper := &slotProvider{out: "wrapped"}
	wrapped := 0

	s := newAuxSummarizer(testAuxResolver(slot, "m", 0), func(p llm.Provider) llm.Provider {
		wrapped++
		return wrapper
	}, nil, slog.Default())
	if s == nil {
		t.Fatal("no summarizer wired")
	}

	out, err := s.Summarize(context.Background(), "prompt")
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if wrapped != 1 {
		t.Errorf("wrap applied %d times, want 1 per call", wrapped)
	}
	if out != "wrapped" {
		t.Errorf("output = %q, want the wrapped provider's — the middleware chain was bypassed", out)
	}
}

// A slot that becomes unbuildable after boot on an instance with no KEEPER_*
// model is an error, not a silent empty summary: an empty string parses as
// "zero rules extracted" and would look like a successful tick.
func TestAuxSummarizer_NothingResolvableAtCallTimeIsAnError(t *testing.T) {
	live := true
	s := newAuxSummarizer(func() (llm.Provider, string, time.Duration) {
		if live {
			return &slotProvider{out: "ok"}, "m", 0
		}
		return nil, "", 0
	}, nil, nil, slog.Default())
	if s == nil {
		t.Fatal("no summarizer wired")
	}

	live = false
	out, err := s.Summarize(context.Background(), "prompt")
	if err == nil {
		t.Fatalf("got (%q, nil), want an error naming the curator slot", out)
	}
	if out != "" {
		t.Errorf("output = %q, want empty on failure", out)
	}
}

// A provider error is surfaced, not swallowed — the consolidator's own error
// path emits the failure and the tick is retried.
func TestAuxSummarizer_ProviderErrorSurfaces(t *testing.T) {
	slot := &slotProvider{err: errors.New("model is loading")}
	s := newAuxSummarizer(testAuxResolver(slot, "m", 0), nil, nil, slog.Default())
	if s == nil {
		t.Fatal("no summarizer wired")
	}
	if _, err := s.Summarize(context.Background(), "prompt"); err == nil {
		t.Error("a provider failure was swallowed")
	}
}
