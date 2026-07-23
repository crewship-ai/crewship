package sidecar

import (
	"strings"

	"github.com/crewship-ai/crewship/internal/egressallow"
)

// The domain-allowlist primitive moved to the dependency-free leaf package
// internal/egressallow so that internal/egresspolicy can share it WITHOUT
// importing sidecar (which would form an import cycle now that the sidecar MCP
// gateway builds its gated client through egresspolicy). These aliases keep
// every existing sidecar call site — and the ~40 tests referencing
// NewDomainAllowlist / DefaultAllowedDomains — unchanged.
type DomainAllowlist = egressallow.DomainAllowlist

// DefaultAllowedDomains re-exports the leaf's default LLM/CLI allowlist.
var DefaultAllowedDomains = egressallow.DefaultAllowedDomains

// NewDomainAllowlist re-exports the leaf constructor.
func NewDomainAllowlist(domains []string) *DomainAllowlist {
	return egressallow.NewDomainAllowlist(domains)
}

// stripPort delegates to the leaf so providerForHost (below) and the sidecar
// allowlist fuzz test keep a single implementation.
func stripPort(host string) string { return egressallow.StripPort(host) }

// providerForHost returns the LLM provider type for a given host, or empty string.
func providerForHost(host string) ProviderType {
	h := strings.ToLower(stripPort(host))
	switch h {
	case "api.anthropic.com":
		return ProviderAnthropic
	case "api.openai.com":
		return ProviderOpenAI
	case "generativelanguage.googleapis.com":
		return ProviderGoogle
	default:
		return ""
	}
}
