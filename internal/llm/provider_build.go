package llm

import (
	"fmt"
	"os"
	"strings"
)

// BuildAuxProvider maps an AuxModel.Provider string to a concrete
// Provider implementation, via the registry in registry.go. Returns an error
// rather than a silent no-op so mis-configuration surfaces as a startup warn
// line operators can grep — shared by every aux-slot consumer
// (internal/server's Keeper F4 evaluators, internal/api's post-run verdict
// wiring, ...) so provider selection only lives in one place.
//
// "anthropic" sources the key from ANTHROPIC_API_KEY and "openai" from
// OPENAI_API_KEY (ProviderSpec.KeyEnv, the same env the rest of the codebase
// reads). An empty key is a hard error: the constructor would build a provider
// that 401s on every request, which is strictly worse than the caller falling
// back to a local judge or disabling the feature with a clear reason.
//
// Google/Gemini is deliberately absent from the registry: there is no Provider
// implementation for it in this package, so accepting it here would
// produce a slot that resolves and then fails at first use.
func BuildAuxProvider(m AuxModel) (Provider, error) {
	return BuildAuxProviderAt(m, "")
}

// BuildAuxProviderAt is BuildAuxProvider with an explicit base URL.
//
// It exists because the Keeper judge endpoint became runtime-settable: an
// "ollama" aux slot must dial the endpoint the instance is actually
// configured with, not KEEPER_OLLAMA_URL from the environment the process
// started in. Callers that hold the resolved endpoint pass it; baseURL
// == "" keeps the historical env-then-localhost behaviour, which is what
// the plain BuildAuxProvider callers still want.
//
// The parameter used to be named ollamaBase, because ollama was the only
// endpoint-driven provider. It resolves through ProviderSpec.BaseEnv /
// BaseDefault now, so it is no longer Ollama-specific — but every caller
// passes it positionally, so the rename is source-compatible.
func BuildAuxProviderAt(m AuxModel, baseURL string) (Provider, error) {
	return BuildAuxProviderWithKey(m, baseURL, "")
}

// BuildAuxProviderWithKey is BuildAuxProviderAt with an explicit API key.
//
// It exists because an evaluator slot can now name the VAULT CREDENTIAL it
// spends (#1554) instead of silently billing whatever key the server process
// booted with. On an instance holding several keys — the normal case, since each
// carries its own subscription limit — "which model" was answerable and "on
// whose key" was not.
//
// apiKey == "" is the pre-existing behaviour, exactly: the key comes from
// ANTHROPIC_API_KEY / OPENAI_API_KEY and a missing one is still a hard error
// rather than a provider that 401s on first use. That is what keeps an instance
// nobody has configured unchanged by this parameter.
//
// The endpoint for a keyed provider is OURS (api.anthropic.com / api.openai.com),
// not an operator-supplied URL, so there is no key-exfiltration hazard of the
// kind that makes governance refuse to attach the server key to a tenant-chosen
// openai_compat endpoint. "ollama" ignores the key: it dials the instance judge
// endpoint, which needs none — it declares no ProviderSpec.KeyEnv, so a key is
// neither required nor rejected there.
//
// Provider matching goes through LookupProvider, which lowercases and trims.
// That is a deliberate widening of what used to be an exact switch: internal/api
// carries the provider as an uppercase enum value while keepercfg stores it
// lowercase, so "Anthropic" and " anthropic" resolve now where they previously
// fell through to the unsupported-provider error.
func BuildAuxProviderWithKey(m AuxModel, baseURL, apiKey string) (Provider, error) {
	spec, ok := LookupProvider(m.Provider)
	if !ok {
		// The hint is generated from the registry rather than written out, so
		// it cannot drift from what the builder actually accepts.
		return nil, fmt.Errorf("unsupported aux provider %q (want %s)",
			m.Provider, strings.Join(RegisteredProviders(), "|"))
	}

	key := apiKey
	if spec.KeyEnv != "" {
		if key == "" {
			key = os.Getenv(spec.KeyEnv)
		}
		if key == "" {
			return nil, fmt.Errorf("%s env not set (required for %s aux slot %q)",
				spec.KeyEnv, spec.ID, m.Model)
		}
	}

	base := strings.TrimSpace(baseURL)
	if base == "" && spec.BaseEnv != "" {
		base = os.Getenv(spec.BaseEnv)
	}
	if base == "" {
		base = spec.BaseDefault
	}
	return spec.New(m, base, key)
}
