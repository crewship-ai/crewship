package llm

import (
	"fmt"
	"os"
	"strings"
)

// BuildAuxProvider maps an AuxModel.Provider string to a concrete
// Provider implementation. Returns an error rather than a silent no-op
// so mis-configuration surfaces as a startup warn line operators can
// grep — shared by every aux-slot consumer (internal/server's Keeper F4
// evaluators, internal/api's post-run verdict wiring, ...) so provider
// selection only lives in one place.
//
// "anthropic" sources the key from ANTHROPIC_API_KEY and "openai" from
// OPENAI_API_KEY (the same env the rest of the codebase reads). An empty
// key is a hard error: the constructor would build a provider that 401s
// on every request, which is strictly worse than the caller falling back
// to a local judge or disabling the feature with a clear reason.
//
// Google/Gemini is deliberately absent: there is no Provider
// implementation for it in this package, so accepting it here would
// produce a slot that resolves and then fails at first use.
func BuildAuxProvider(m AuxModel) (Provider, error) {
	return BuildAuxProviderAt(m, "")
}

// BuildAuxProviderAt is BuildAuxProvider with an explicit Ollama base URL.
//
// It exists because the Keeper judge endpoint became runtime-settable: an
// "ollama" aux slot must dial the endpoint the instance is actually
// configured with, not KEEPER_OLLAMA_URL from the environment the process
// started in. Callers that hold the resolved endpoint pass it; ollamaBase
// == "" keeps the historical env-then-localhost behaviour, which is what
// the plain BuildAuxProvider callers still want.
func BuildAuxProviderAt(m AuxModel, ollamaBase string) (Provider, error) {
	switch m.Provider {
	case "anthropic":
		key := os.Getenv("ANTHROPIC_API_KEY")
		if key == "" {
			return nil, fmt.Errorf("ANTHROPIC_API_KEY env not set (required for anthropic aux slot %q)", m.Model)
		}
		return NewAnthropic(key), nil
	case "openai":
		key := os.Getenv("OPENAI_API_KEY")
		if key == "" {
			return nil, fmt.Errorf("OPENAI_API_KEY env not set (required for openai aux slot %q)", m.Model)
		}
		return NewOpenAI(key), nil
	case "ollama":
		base := strings.TrimSpace(ollamaBase)
		if base == "" {
			base = os.Getenv("KEEPER_OLLAMA_URL")
		}
		if base == "" {
			base = "http://localhost:11434"
		}
		return NewOllama(base, m.Model), nil
	default:
		return nil, fmt.Errorf("unsupported aux provider %q (want anthropic|openai|ollama)", m.Provider)
	}
}
