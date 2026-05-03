package sidecar

import (
	"net"
	"strings"
	"sync"
)

// DefaultAllowedDomains contains the LLM API domains that the sidecar
// will forward requests to. All other domains are blocked. Mirrors the
// frontend list in components/features/crews/crew-network-policy.tsx —
// keep both lists in sync.
var DefaultAllowedDomains = []string{
	"api.anthropic.com",
	"api.openai.com",
	"generativelanguage.googleapis.com",
	"api.factory.ai",
	"api.cursor.sh", // cursor-agent CLI talks here for headless agent runs.
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
// Handles IPv6 addresses correctly (e.g. [::1]:443).
func (al *DomainAllowlist) IsAllowed(host string) bool {
	al.mu.RLock()
	defer al.mu.RUnlock()

	h := stripPort(host)
	return al.domains[strings.ToLower(h)]
}

// Add adds a domain to the allowlist.
func (al *DomainAllowlist) Add(domain string) {
	al.mu.Lock()
	defer al.mu.Unlock()
	al.domains[strings.ToLower(domain)] = true
}

// stripPort removes the port from a host string, handling IPv6 bracket notation.
func stripPort(host string) string {
	// Fast path: bare hostnames have no colon and no brackets — return as-is
	// instead of paying for net.SplitHostPort's error-alloc on every port-less
	// call from the proxy's IsAllowed / providerForHost hot path.
	if strings.IndexByte(host, ':') < 0 && strings.IndexByte(host, '[') < 0 {
		return host
	}
	// Try net.SplitHostPort (handles [::1]:443, host:port, etc.)
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	// No port present — strip brackets if bare IPv6 (e.g. "[::1]").
	return strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
}

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
