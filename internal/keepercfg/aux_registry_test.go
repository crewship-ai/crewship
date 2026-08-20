package keepercfg

import (
	"reflect"
	"strings"
	"testing"
)

// auxProviders() reads the llm provider registry now instead of holding its own
// literal, so the validator and the builder cannot disagree about what an
// operator may save.
//
// The list served to the console is unchanged, order included: it reaches the
// picker at internal/api/admin_keeper_aux.go, and a sorted list would reorder
// an operator's dropdown for no reason anyone could point at.
func TestAuxProviders_MatchesTheRegistry(t *testing.T) {
	want := []string{"anthropic", "openai", "ollama"}
	if got := AuxProviders(); !reflect.DeepEqual(got, want) {
		t.Errorf("AuxProviders() = %v, want %v", got, want)
	}
}

func TestKnownAuxProvider(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		want     bool
	}{
		{"anthropic", "anthropic", true},
		{"openai", "openai", true},
		{"ollama", "ollama", true},
		// Rejected on purpose: the catalogue offers Gemini ids, this build has
		// no Provider for them, and a slot that saves cleanly and then fails at
		// first use is worse than a refusal with a reason.
		{"google", "google", false},
		{"gemini", "gemini", false},
		{"unknown vendor", "cohere", false},
		{"empty", "", false},
		// The store keeps the lowercase form llm.AuxModel stores; the builder's
		// lookup is case-insensitive but this validator is not, and widening it
		// here would let two spellings of one provider into the table.
		{"uppercase is not the stored form", "Anthropic", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := KnownAuxProvider(tt.provider); got != tt.want {
				t.Errorf("KnownAuxProvider(%q) = %v, want %v", tt.provider, got, tt.want)
			}
		})
	}
}

// The google rejection carries its own message, and it is reached through
// KnownAuxProvider — so it has to keep firing now that the provider list moved.
func TestValidateAux_GoogleKeepsItsReason(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		wantMsg  string
	}{
		{"google", "google", "no Gemini provider"},
		{"gemini", "gemini", "no Gemini provider"},
		{"other unknown", "cohere", "unknown evaluator provider"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAux(AuxOverride{Provider: tt.provider, Model: "some-model"})
			if err == nil {
				t.Fatalf("validateAux accepted provider %q", tt.provider)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("err = %q, want it to mention %q", err.Error(), tt.wantMsg)
			}
		})
	}
}
