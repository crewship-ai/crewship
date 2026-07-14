package governance

import (
	"context"
	"fmt"
)

// Governance-model provider identifiers (M2a, #1001). Stored in
// keeper_governance_settings.gov_model_provider; empty means "use the
// server/env default" (backward-compatible with the pre-M2a env wiring).
const (
	ProviderOllama       = "ollama"
	ProviderAnthropic    = "anthropic"
	ProviderOpenAICompat = "openai_compat"
)

// MaxGovModelIDLen bounds a stored model identifier so the DB row and any
// prompt/log that echoes it stay bounded regardless of what the CLI/UI send.
const MaxGovModelIDLen = 200

// KnownGovProvider reports whether p is a supported governance-model provider.
// Exported so the API layer validates the picker input against the same set the
// resolver trusts — the two can't drift.
func KnownGovProvider(p string) bool {
	switch p {
	case ProviderOllama, ProviderAnthropic, ProviderOpenAICompat:
		return true
	default:
		return false
	}
}

// Credential types a governance-model credential may carry. The API layer
// validates gov_model_credential_id points at one of these; the resolver routes
// the decrypted value into the matching field by this type.
const (
	CredTypeEndpointURL = "ENDPOINT_URL"
	CredTypeAPIKey      = "API_KEY"
)

// CredentialLookup fetches a vault credential's decrypted secret by id, scoped
// to a workspace. It is the seam ResolveGovModel uses to source a governance
// model's endpoint/key without importing the API/credentials layer (avoiding an
// import cycle); the concrete implementation lives in internal/api, and tests
// pass a fake.
//
// It returns the credential's type (CredTypeEndpointURL / CredTypeAPIKey) and
// its decrypted value. A missing / revoked / soft-deleted / undecryptable
// credential returns a non-nil error — which ResolveGovModel treats as the
// revoke-safety trigger (§4.4), NOT a hard failure.
type CredentialLookup interface {
	LookupCredential(ctx context.Context, workspaceID, credentialID string) (credType, value string, err error)
}

// OllamaDefault is the always-available local judge ResolveGovModel degrades to
// when a configured governance credential is unusable (§4.4). It is the
// server's cfg.Keeper.OllamaURL / cfg.Keeper.Model — a working evaluator that
// needs no external secret.
type OllamaDefault struct {
	URL   string
	Model string
}

// ResolvedGovModel is a fully-resolved governance-model choice, ready for the
// server layer to build an llm.Provider from. Secret material is already
// fetched from the vault (empty EndpointURL/APIKey → the provider builder falls
// back to its env default).
type ResolvedGovModel struct {
	Provider    string // ProviderOllama | ProviderAnthropic | ProviderOpenAICompat
	Model       string
	EndpointURL string // base URL from an ENDPOINT_URL credential; "" = provider/env default
	APIKey      string // key from an API_KEY credential; "" = env fallback

	// Degraded is true when the configured credential was missing/revoked/
	// undecryptable (or the stored config was invalid) and this resolved to the
	// default OLLAMA judge instead (§4.4). The caller surfaces DegradeReason as
	// a WARN in the Keeper status card + a journal entry. A degraded result is
	// still a working evaluator — that is the whole point of the contract.
	Degraded      bool
	DegradeReason string
}

// ResolveGovModel turns a workspace's governance-model Settings into a concrete,
// buildable provider choice. It NEVER returns an error and NEVER yields a
// broken/nil evaluator — the §4.4 invariant is that a resolvable working judge
// always exists, because the access path is fail-closed-DENY (a broken provider
// would deny every credential request) and the behavior path must never
// silently become "no judge".
//
// Cases:
//   - Unconfigured (empty provider) → (_, false): the caller uses the
//     server/env default (today's behavior). This is the backward-compatible
//     path an un-migrated / un-touched workspace takes.
//   - Configured, no credential → resolves provider+model; secrets come from the
//     provider builder's env fallback. found=true, not degraded.
//   - Configured, with a usable credential → resolves the secret from the vault
//     by the credential's type. found=true, not degraded.
//   - Configured, but the credential is missing/revoked/undecryptable, OR the
//     stored provider/type is invalid → DEGRADE to dflt (OLLAMA) with
//     Degraded=true + a reason. found=true (a provider is still built).
func ResolveGovModel(ctx context.Context, s Settings, workspaceID string, vault CredentialLookup, dflt OllamaDefault) (ResolvedGovModel, bool) {
	if s.GovModelProvider == "" {
		return ResolvedGovModel{}, false
	}

	// An invalid stored provider is a mis-config (the API validates on write,
	// but a hand-edited DB or a future enum change must not brick the judge).
	if !KnownGovProvider(s.GovModelProvider) {
		return degrade(dflt, fmt.Sprintf("governance model provider %q is not supported; using the default local judge", s.GovModelProvider)), true
	}

	resolved := ResolvedGovModel{Provider: s.GovModelProvider, Model: s.GovModelID}

	// No credential → provider builder sources any needed secret from env.
	if s.GovModelCredentialID == "" {
		return resolved, true
	}

	credType, value, err := vault.LookupCredential(ctx, workspaceID, s.GovModelCredentialID)
	if err != nil {
		return degrade(dflt, fmt.Sprintf("governance model credential is unavailable (%v); using the default local judge", err)), true
	}

	switch credType {
	case CredTypeEndpointURL:
		resolved.EndpointURL = value
	case CredTypeAPIKey:
		resolved.APIKey = value
	default:
		return degrade(dflt, fmt.Sprintf("governance model credential type %q is not usable (want ENDPOINT_URL or API_KEY); using the default local judge", credType)), true
	}

	return resolved, true
}

// degrade builds the fail-closed OLLAMA fallback used across §4.4 branches.
func degrade(dflt OllamaDefault, reason string) ResolvedGovModel {
	return ResolvedGovModel{
		Provider:      ProviderOllama,
		Model:         dflt.Model,
		EndpointURL:   dflt.URL,
		Degraded:      true,
		DegradeReason: reason,
	}
}
