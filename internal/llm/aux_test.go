package llm

import (
	"testing"
	"time"
)

// TestAuxiliaryModels_DefaultsAreHaiku locks PR-B F3 MVP contract:
// every aux slot defaults to claude-haiku-4-5 with the Anthropic
// provider. The "no API key required" core moat (memory:
// project_anthropic_managed_agents) is a documented Phase 1
// compromise — Phase 2 will introduce local-model defaults via
// Ollama, gated by feature flag, but MVP wants the loud single-
// provider answer rather than silent degradation.
func TestAuxiliaryModels_DefaultsAreHaiku(t *testing.T) {
	cfg := DefaultAuxiliaryModels()
	slots := []struct {
		name string
		got  AuxModel
	}{
		{"Curator", cfg.Curator},
		{"Keeper", cfg.Keeper},
		{"Behavior", cfg.Behavior},
		{"MemoryHealth", cfg.MemoryHealth},
		{"Negative", cfg.Negative},
		{"Fallback", cfg.Fallback},
	}
	for _, s := range slots {
		if s.got.Provider != "anthropic" {
			t.Errorf("%s.Provider = %q, want anthropic", s.name, s.got.Provider)
		}
		if s.got.Model != "claude-haiku-4-5" {
			t.Errorf("%s.Model = %q, want claude-haiku-4-5", s.name, s.got.Model)
		}
		if s.got.Timeout <= 0 {
			t.Errorf("%s.Timeout = %v, want positive", s.name, s.got.Timeout)
		}
	}
}

// TestResolveAux_ReturnsExplicitSlot verifies a non-empty slot wins
// over Fallback. Operator-set values flow through verbatim.
func TestResolveAux_ReturnsExplicitSlot(t *testing.T) {
	cfg := AuxiliaryModels{
		Keeper:   AuxModel{Provider: "ollama", Model: "phi3:medium", Timeout: 10 * time.Second},
		Fallback: AuxModel{Provider: "anthropic", Model: "claude-haiku-4-5", Timeout: 5 * time.Second},
	}
	got, err := ResolveAux(cfg, SlotKeeper)
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != "ollama" || got.Model != "phi3:medium" {
		t.Errorf("got %+v, want ollama/phi3:medium (explicit slot wins)", got)
	}
}

// TestResolveAux_FallsBackWhenSlotEmpty: if a specific slot has no
// provider configured, the resolver substitutes Fallback. Lets the
// operator configure most slots once via Fallback and override only
// where they want a different model.
func TestResolveAux_FallsBackWhenSlotEmpty(t *testing.T) {
	cfg := AuxiliaryModels{
		// Keeper deliberately empty
		Fallback: AuxModel{Provider: "anthropic", Model: "claude-haiku-4-5", Timeout: 5 * time.Second},
	}
	got, err := ResolveAux(cfg, SlotKeeper)
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != "anthropic" || got.Model != "claude-haiku-4-5" {
		t.Errorf("got %+v, want fallback anthropic/claude-haiku-4-5", got)
	}
}

// TestResolveAux_NoFallbackNoSlot_ReturnsError: a slot with no
// provider AND no fallback is a config bug, surfaced loudly. Mirrors
// PR-Z Z.2's "no silent degradation" principle for the Keeper model.
func TestResolveAux_NoFallbackNoSlot_ReturnsError(t *testing.T) {
	cfg := AuxiliaryModels{} // every slot empty, Fallback empty
	if _, err := ResolveAux(cfg, SlotKeeper); err == nil {
		t.Error("expected error when neither slot nor Fallback has a provider")
	}
}

// TestResolveAux_UnknownSlot_ReturnsError guards the typed Slot
// enum at the boundary.
func TestResolveAux_UnknownSlot_ReturnsError(t *testing.T) {
	cfg := DefaultAuxiliaryModels()
	if _, err := ResolveAux(cfg, Slot("bogus")); err == nil {
		t.Error("expected error for unknown slot 'bogus'")
	}
}
