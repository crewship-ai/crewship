package llm

import (
	"testing"
	"time"
)

// mapEnv builds a getenv func from a fixed map so the override logic is tested
// without touching the real process environment.
func mapEnv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestAuxiliaryModelsFromEnv_OverridesOnlySetSlots(t *testing.T) {
	base := DefaultAuxiliaryModels()
	got := AuxiliaryModelsFromEnv(base, mapEnv(map[string]string{
		"CREWSHIP_AUX_CURATOR_PROVIDER": "ollama",
		"CREWSHIP_AUX_CURATOR_MODEL":    "llama3.1",
		"CREWSHIP_AUX_CURATOR_TIMEOUT":  "45s",
	}))

	if got.Curator.Provider != "ollama" || got.Curator.Model != "llama3.1" {
		t.Fatalf("curator not overridden: %+v", got.Curator)
	}
	if got.Curator.Timeout != 45*time.Second {
		t.Fatalf("curator timeout = %v, want 45s", got.Curator.Timeout)
	}
	// Untouched slot keeps the default.
	if got.Keeper.Provider != "anthropic" || got.Keeper.Model != "claude-haiku-4-5" {
		t.Fatalf("keeper should keep default, got %+v", got.Keeper)
	}
	// The passed-in base must not be mutated (value semantics).
	if base.Curator.Provider != "anthropic" {
		t.Fatalf("base was mutated: %+v", base.Curator)
	}
}

func TestAuxiliaryModelsFromEnv_PartialOverrideKeepsRest(t *testing.T) {
	got := AuxiliaryModelsFromEnv(DefaultAuxiliaryModels(), mapEnv(map[string]string{
		"CREWSHIP_AUX_KEEPER_MODEL": "claude-sonnet-4-6",
	}))
	if got.Keeper.Model != "claude-sonnet-4-6" {
		t.Fatalf("keeper model = %q, want claude-sonnet-4-6", got.Keeper.Model)
	}
	// Provider + timeout untouched by a model-only override.
	if got.Keeper.Provider != "anthropic" || got.Keeper.Timeout != 5*time.Second {
		t.Fatalf("keeper provider/timeout drifted: %+v", got.Keeper)
	}
}

func TestAuxiliaryModelsFromEnv_BadTimeoutIgnored(t *testing.T) {
	def := DefaultAuxiliaryModels()
	got := AuxiliaryModelsFromEnv(def, mapEnv(map[string]string{
		"CREWSHIP_AUX_BEHAVIOR_TIMEOUT": "not-a-duration",
	}))
	// Unparsable timeout must be ignored, not zero out the deadline.
	if got.Behavior.Timeout != def.Behavior.Timeout {
		t.Fatalf("bad timeout should be ignored: got %v, want %v", got.Behavior.Timeout, def.Behavior.Timeout)
	}
}

func TestAuxiliaryModelsFromEnv_NoEnvIsNoop(t *testing.T) {
	def := DefaultAuxiliaryModels()
	got := AuxiliaryModelsFromEnv(def, mapEnv(nil))
	if got != def {
		t.Fatalf("no env should be a no-op:\n got %+v\nwant %+v", got, def)
	}
}
