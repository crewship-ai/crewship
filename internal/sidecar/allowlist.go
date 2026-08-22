package sidecar

import (
	"strings"

	"github.com/crewship-ai/crewship/internal/egressallow"
	"github.com/crewship-ai/crewship/internal/llmroute"
)

// The domain-allowlist primitive moved to the dependency-free leaf package
// internal/egressallow so that internal/egresspolicy can share it WITHOUT
// importing sidecar (which would form an import cycle now that the sidecar MCP
// gateway builds its gated client through egresspolicy). These aliases keep
// every existing sidecar call site — and the ~40 tests referencing
// NewDomainAllowlist / DefaultAllowedDomains / PackageRegistryDomains —
// unchanged. The wildcard/subdomain matching and the package-registry preset
// (#1377) live in the leaf alongside the type they extend.
type DomainAllowlist = egressallow.DomainAllowlist

// DefaultAllowedDomains re-exports the leaf's default LLM/CLI allowlist.
var DefaultAllowedDomains = egressallow.DefaultAllowedDomains

// PackageRegistryDomains re-exports the leaf's curated "allow package
// registries" preset (#1377) — the set the one-click UI button and the CLI
// `--allow-package-registries` flag append.
var PackageRegistryDomains = egressallow.PackageRegistryDomains

// NewDomainAllowlist re-exports the leaf constructor.
func NewDomainAllowlist(domains []string) *DomainAllowlist {
	return egressallow.NewDomainAllowlist(domains)
}

// stripPort delegates to the leaf so providerForHost (below) and the sidecar
// allowlist fuzz test keep a single implementation.
func stripPort(host string) string { return egressallow.StripPort(host) }

// providerForHost returns the LLM provider type for a given host, or empty string.
//
// The host→provider table now lives in the llmroute descriptor, but ONLY the
// three grandfathered providers populate Spec.Hosts. That asymmetry is
// deliberate, not an oversight: openrouter.ai is already in
// DefaultAllowedDomains, so mapping it here would flip every existing OpenCode
// BYOK crew that dials OpenRouter directly from pass-through to a hard 503 in
// handleHTTP the moment no OPENROUTER credential is in the store. A new
// provider is reachable through its reverse-proxy path prefix, never by host.
func providerForHost(host string) ProviderType {
	h := strings.ToLower(stripPort(host))
	if s, ok := llmroute.MatchHost(h); ok {
		return ProviderType(s.ID)
	}
	return ""
}
