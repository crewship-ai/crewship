package llm

import (
	"fmt"
	"os"
)

// BuildAuxProvider maps an AuxModel.Provider string to a concrete
// Provider implementation. Closed set today: "anthropic" + "ollama".
// New providers (gemini, openai) require extending this switch.
// Returns an error rather than a silent no-op so mis-configuration
// surfaces as a startup warn line operators can grep — shared by
// every aux-slot consumer (internal/server's Keeper F4 evaluators,
// internal/api's post-run verdict wiring, ...) so provider selection
// only lives in one place.
//
// "anthropic" sources the key from ANTHROPIC_API_KEY (the same env
// the rest of the codebase reads). An empty key here is treated as a
// hard error: NewAnthropic would build a provider that 401s on every
// request, which is strictly worse than the caller falling back to a
// local judge or disabling the feature with a clear reason.
func BuildAuxProvider(m AuxModel) (Provider, error) {
	switch m.Provider {
	case "anthropic":
		key := os.Getenv("ANTHROPIC_API_KEY")
		if key == "" {
			return nil, fmt.Errorf("ANTHROPIC_API_KEY env not set (required for anthropic aux slot %q)", m.Model)
		}
		return NewAnthropic(key), nil
	case "ollama":
		base := os.Getenv("KEEPER_OLLAMA_URL")
		if base == "" {
			base = "http://localhost:11434"
		}
		return NewOllama(base, m.Model), nil
	default:
		return nil, fmt.Errorf("unsupported aux provider %q (want anthropic|ollama)", m.Provider)
	}
}
