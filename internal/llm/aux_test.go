package llm

import (
	"testing"
	"time"
)

// auxSlotsOf lists every slot of a config by name, so a table can assert the
// same thing about all seven without repeating them per test.
func auxSlotsOf(cfg AuxiliaryModels) []struct {
	name string
	got  AuxModel
} {
	return []struct {
		name string
		got  AuxModel
	}{
		{"Curator", cfg.Curator},
		{"Keeper", cfg.Keeper},
		{"Behavior", cfg.Behavior},
		{"MemoryHealth", cfg.MemoryHealth},
		{"Negative", cfg.Negative},
		{"RunSummary", cfg.RunSummary},
		{"Fallback", cfg.Fallback},
	}
}

// TestAuxiliaryModels_DefaultsAreHaiku locks the SHIPPED default: every aux slot
// on anthropic/claude-haiku-4-5, sourced from the first registry row that names
// a DefaultAuxModel rather than from a literal in aux.go.
//
// Both halves are asserted on purpose. The registry half is the anti-drift one —
// ProviderSpec.DefaultAuxModel is now what aux.go reads, so a row edited without
// this in mind moves the shipped default. The literal half is the contract other
// packages already encode (internal/api/system_aux_test.go asserts every slot
// reads anthropic/claude-haiku-4-5, keepercfg uses this value as the `builtin`
// provenance layer), so a registry reorder that silently repointed the shipped
// default must fail HERE, in the package that owns it, and not as a surprise
// three packages away.
func TestAuxiliaryModels_DefaultsAreHaiku(t *testing.T) {
	spec, ok := auxDefaultSpec(nil)
	if !ok {
		t.Fatal("no registry row names a DefaultAuxModel")
	}
	if spec.ID != "anthropic" || spec.DefaultAuxModel != "claude-haiku-4-5" {
		t.Fatalf("shipped default is %s/%s, want anthropic/claude-haiku-4-5",
			spec.ID, spec.DefaultAuxModel)
	}

	cfg := DefaultAuxiliaryModels()
	for _, s := range auxSlotsOf(cfg) {
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

// TestAuxiliaryModels_DefaultsIgnoreTheEnvironment pins the split between the
// two default functions.
//
// DefaultAuxiliaryModels is keepercfg.AuxStore's `builtin` layer, and its only
// job there is to tell a value WE shipped apart from one the operator's
// environment selected (keepercfg.pickAux). If it read OPENAI_API_KEY, an
// instance holding only that key would report every slot as Source=default —
// claiming Crewship ships an openai default it does not ship — and the console's
// "inherited from environment" marker would go dark exactly when it is true.
//
// It is also what keeps every other package's aux test deterministic: they call
// DefaultAuxiliaryModels() and would otherwise depend on the developer's shell.
func TestAuxiliaryModels_DefaultsIgnoreTheEnvironment(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "sk-openai-only")

	cfg := DefaultAuxiliaryModels()
	for _, s := range auxSlotsOf(cfg) {
		if s.got.Provider != "anthropic" || s.got.Model != "claude-haiku-4-5" {
			t.Errorf("%s = %s/%s, want the shipped anthropic/claude-haiku-4-5 regardless of env",
				s.name, s.got.Provider, s.got.Model)
		}
	}
}

// TestAvailableAuxiliaryModels_PicksFirstCredentialedProvider is the behaviour
// change: an unconfigured slot follows the credentials the instance actually
// holds instead of a hardcoded anthropic/claude-haiku-4-5.
//
// The old shape gave an operator with only an OPENAI_API_KEY six evaluator slots
// that each failed at first use asking for a key they have no reason to own.
func TestAvailableAuxiliaryModels_PicksFirstCredentialedProvider(t *testing.T) {
	tests := []struct {
		name         string
		env          map[string]string
		wantProvider string
		wantModel    string
	}{
		{
			// No credential anywhere: keep the shipped default, so the builder's
			// error names ANTHROPIC_API_KEY. Guessing a provider we also cannot
			// reach would only move the failure later.
			name:         "no keys keeps the shipped default",
			env:          nil,
			wantProvider: "anthropic",
			wantModel:    "claude-haiku-4-5",
		},
		{
			name:         "anthropic key",
			env:          map[string]string{"ANTHROPIC_API_KEY": "sk-ant"},
			wantProvider: "anthropic",
			wantModel:    "claude-haiku-4-5",
		},
		{
			name:         "openai key only",
			env:          map[string]string{"OPENAI_API_KEY": "sk-openai"},
			wantProvider: "openai",
			wantModel:    "gpt-5.4-mini",
		},
		{
			// Declaration order decides, the same order the console's picker
			// renders. Adding a second key must never repoint a working slot.
			name: "both keys: declaration order wins",
			env: map[string]string{
				"ANTHROPIC_API_KEY": "sk-ant",
				"OPENAI_API_KEY":    "sk-openai",
			},
			wantProvider: "anthropic",
			wantModel:    "claude-haiku-4-5",
		},
		{
			// BuildAuxProviderWithKey would accept "  " and 401 on first use;
			// treating it as absent here keeps the failure at startup.
			name:         "whitespace-only key does not count",
			env:          map[string]string{"OPENAI_API_KEY": "   "},
			wantProvider: "anthropic",
			wantModel:    "claude-haiku-4-5",
		},
		{
			// ollama needs no key, but its registry row names no DefaultAuxModel
			// — the model is whatever the operator pulled. A slot on the empty
			// model builds a provider that fails on its first request, so the
			// keyless row is skipped rather than preferred.
			name:         "keyless ollama is not a default",
			env:          map[string]string{"KEEPER_OLLAMA_URL": "http://localhost:11434"},
			wantProvider: "anthropic",
			wantModel:    "claude-haiku-4-5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := AvailableAuxiliaryModels(mapEnv(tt.env))
			for _, s := range auxSlotsOf(cfg) {
				if s.got.Provider != tt.wantProvider || s.got.Model != tt.wantModel {
					t.Errorf("%s = %s/%s, want %s/%s",
						s.name, s.got.Provider, s.got.Model, tt.wantProvider, tt.wantModel)
				}
			}
		})
	}
}

// TestAvailableAuxiliaryModels_KeepsPerSlotBudgets: retargeting the provider
// must not move a deadline. The budgets are sized for the slowest judge a slot
// can end up on (see auxDefaultTimeout), so re-deriving them per provider would
// re-introduce #1530 — a budget too small for the model, surfacing as a
// fail-closed verdict.
func TestAvailableAuxiliaryModels_KeepsPerSlotBudgets(t *testing.T) {
	shipped := DefaultAuxiliaryModels()
	got := AvailableAuxiliaryModels(mapEnv(map[string]string{"OPENAI_API_KEY": "sk-openai"}))

	if got.Curator.Provider != "openai" {
		t.Fatalf("precondition: curator = %q, want openai", got.Curator.Provider)
	}
	for i, s := range auxSlotsOf(got) {
		want := auxSlotsOf(shipped)[i].got.Timeout
		if s.got.Timeout != want {
			t.Errorf("%s.Timeout = %v, want %v (unchanged by the provider switch)",
				s.name, s.got.Timeout, want)
		}
	}
}

// TestLoadAuxiliaryModels_EnvOverrideBeatsAvailability: an operator who named a
// provider has already answered the question availability guesses at, so
// CREWSHIP_AUX_<SLOT>_PROVIDER stays on top. This is the composition
// LoadAuxiliaryModels performs, checked through the real os.Getenv path since
// that is the one server bootstrap calls.
func TestLoadAuxiliaryModels_EnvOverrideBeatsAvailability(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	t.Setenv("CREWSHIP_AUX_CURATOR_PROVIDER", "ollama")
	t.Setenv("CREWSHIP_AUX_CURATOR_MODEL", "phi3:medium")

	got := LoadAuxiliaryModels()
	if got.Curator.Provider != "ollama" || got.Curator.Model != "phi3:medium" {
		t.Errorf("curator = %s/%s, want the explicit ollama/phi3:medium",
			got.Curator.Provider, got.Curator.Model)
	}
	// An un-named slot follows the credential the instance holds.
	if got.Behavior.Provider != "openai" || got.Behavior.Model != "gpt-5.4-mini" {
		t.Errorf("behavior = %s/%s, want openai/gpt-5.4-mini",
			got.Behavior.Provider, got.Behavior.Model)
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

// TestResolveAux_RunSummarySlot verifies the run-verdict summarizer
// (#1403) resolves through the same slot machinery as every other aux
// consumer — explicit slot wins, same as SlotKeeper etc.
func TestResolveAux_RunSummarySlot(t *testing.T) {
	cfg := AuxiliaryModels{
		RunSummary: AuxModel{Provider: "anthropic", Model: "claude-haiku-4-5", Timeout: 15 * time.Second},
		Fallback:   AuxModel{Provider: "anthropic", Model: "claude-haiku-4-5", Timeout: 5 * time.Second},
	}
	got, err := ResolveAux(cfg, SlotRunSummary)
	if err != nil {
		t.Fatal(err)
	}
	if got.Timeout != 15*time.Second {
		t.Errorf("Timeout = %v, want 15s (explicit slot wins)", got.Timeout)
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

// TestResolveAux_ExplicitSlot_BorrowsFallbackTimeout: an operator who
// sets provider+model on a slot but forgets the timeout shouldn't get
// a deadline-less LLM call. The resolver borrows Fallback.Timeout
// when present, else falls back to a 30s hard default.
func TestResolveAux_ExplicitSlot_BorrowsFallbackTimeout(t *testing.T) {
	t.Run("fallback timeout present", func(t *testing.T) {
		cfg := AuxiliaryModels{
			Keeper:   AuxModel{Provider: "ollama", Model: "phi3:medium"}, // no Timeout
			Fallback: AuxModel{Provider: "anthropic", Model: "claude-haiku-4-5", Timeout: 7 * time.Second},
		}
		got, err := ResolveAux(cfg, SlotKeeper)
		if err != nil {
			t.Fatal(err)
		}
		if got.Provider != "ollama" || got.Model != "phi3:medium" {
			t.Errorf("got %+v, want ollama/phi3:medium (explicit slot wins)", got)
		}
		if got.Timeout != 7*time.Second {
			t.Errorf("Timeout = %v, want 7s (borrowed from Fallback)", got.Timeout)
		}
	})

	t.Run("no fallback timeout: hard default", func(t *testing.T) {
		cfg := AuxiliaryModels{
			Keeper: AuxModel{Provider: "ollama", Model: "phi3:medium"}, // no Timeout
			// Fallback empty/zero
		}
		got, err := ResolveAux(cfg, SlotKeeper)
		if err != nil {
			t.Fatal(err)
		}
		if got.Timeout != 30*time.Second {
			t.Errorf("Timeout = %v, want 30s (hard default)", got.Timeout)
		}
	})
}
