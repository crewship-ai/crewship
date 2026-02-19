package sidecar

import (
	"strings"
	"sync"
)

// DefaultAllowedDomains contains the LLM API domains that the sidecar
// will forward requests to. All other domains are blocked.
var DefaultAllowedDomains = []string{
	"api.anthropic.com",
	"api.openai.com",
	"generativelanguage.googleapis.com",
	"api.factory.ai",
}

// DomainAllowlist controls which outbound domains the agent is allowed to reach.
// Thread-safe.
type DomainAllowlist struct {
	mu      sync.RWMutex
	domains map[string]bool
}

// NewDomainAllowlist creates an allowlist from the given domains.
func NewDomainAllowlist(domains []string) *DomainAllowlist {
	al := &DomainAllowlist{
		domains: make(map[string]bool, len(domains)),
	}
	for _, d := range domains {
		al.domains[strings.ToLower(d)] = true
	}
	return al
}

// IsAllowed returns true if the host (with optional :port) is on the allowlist.
func (al *DomainAllowlist) IsAllowed(host string) bool {
	al.mu.RLock()
	defer al.mu.RUnlock()

	// Strip port if present
	h := strings.ToLower(host)
	if idx := strings.LastIndex(h, ":"); idx != -1 {
		h = h[:idx]
	}

	return al.domains[h]
}

// Add adds a domain to the allowlist.
func (al *DomainAllowlist) Add(domain string) {
	al.mu.Lock()
	defer al.mu.Unlock()
	al.domains[strings.ToLower(domain)] = true
}

// providerForHost returns the LLM provider type for a given host, or empty string.
func providerForHost(host string) ProviderType {
	h := strings.ToLower(host)
	if idx := strings.LastIndex(h, ":"); idx != -1 {
		h = h[:idx]
	}
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
