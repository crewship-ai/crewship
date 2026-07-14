package server

import (
	"context"
	"log/slog"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/llm"
)

// buildLLMProvider still treats an anthropic slot with no key as a hard error:
// a provider that 401s on every request is worse than a clean fallback/503.
func TestBuildLLMProvider_AnthropicRequiresKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	if _, err := buildLLMProvider(llm.AuxModel{Provider: "anthropic", Model: "claude-haiku-4-5"}); err == nil {
		t.Fatal("expected error when ANTHROPIC_API_KEY unset")
	}
}

func TestBuildLLMProvider_Ollama(t *testing.T) {
	p, err := buildLLMProvider(llm.AuxModel{Provider: "ollama", Model: "qwen2.5:3b-instruct"})
	if err != nil || p == nil {
		t.Fatalf("ollama provider: got (%v, %v), want non-nil provider and no error", p, err)
	}
}

// M2 fully-local: when the configured aux provider can't be built at boot
// (typically an anthropic default slot with no ANTHROPIC_API_KEY) but a local
// default judge IS configured, the aux evaluator must still construct on that
// local judge instead of being disabled. This is what lets the behavior + F4
// evaluators run governance fully-local with no API key.
func TestBuildAuxGatekeeper_FallsBackToLocalJudge(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	aux := llm.DefaultAuxiliaryModels() // behavior slot defaults to anthropic
	gk := buildAuxGatekeeper(aux, llm.SlotBehavior, nil, "http://localhost:11434", "qwen2.5:3b-instruct", nil, nil, slog.Default())
	if gk == nil {
		t.Fatal("expected a non-nil gatekeeper via local-judge fallback when the anthropic default can't build")
	}
}

// Without a key AND without a local default judge there is nothing to fall back
// to, so the slot is left disabled (nil ⇒ endpoint 503) exactly as before.
func TestBuildAuxGatekeeper_NilWhenNoKeyAndNoLocalDefault(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	aux := llm.DefaultAuxiliaryModels()
	gk := buildAuxGatekeeper(aux, llm.SlotBehavior, nil, "", "", nil, nil, slog.Default())
	if gk != nil {
		t.Fatal("expected nil gatekeeper when no API key and no local default judge is configured")
	}
}

// A per-workspace gov-model resolver is accepted and the gatekeeper constructs
// with it wired — the seam that makes the vault-backed setting govern the aux
// evaluators the same way it already governs the access gatekeeper.
func TestBuildAuxGatekeeper_AcceptsGovModelResolver(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	aux := llm.DefaultAuxiliaryModels()
	resolver := gatekeeper.GovModelResolver(func(context.Context, string) (llm.Provider, string) { return nil, "" })
	gk := buildAuxGatekeeper(aux, llm.SlotBehavior, resolver, "http://localhost:11434", "qwen2.5:3b-instruct", nil, nil, slog.Default())
	if gk == nil {
		t.Fatal("expected a non-nil gatekeeper when a gov-model resolver is supplied")
	}
}
