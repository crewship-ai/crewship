package sidecar

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"sort"
	"strings"
	"sync"
)

// DefaultAllowedDomains contains the LLM API domains that the sidecar
// will forward requests to. All other domains are blocked. Mirrors the
// frontend list in components/features/crews/crew-network-policy.tsx —
// keep both lists in sync.
//
// Each entry exists for a specific CLI; comments document which.
var DefaultAllowedDomains = []string{
	// Anthropic
	"api.anthropic.com",     // Claude Code (API key + OAuth)
	"console.anthropic.com", // Claude Code OAuth refresh callback

	// OpenAI / Codex
	"api.openai.com",  // Codex CLI (API key)
	"auth.openai.com", // Codex CLI ChatGPT-subscription login flow
	"chatgpt.com",     // Codex CLI subscription routing

	// Google / Gemini
	"generativelanguage.googleapis.com", // Gemini CLI (AI Studio path)
	"oauth2.googleapis.com",             // Gemini CLI OAuth flow
	"accounts.google.com",               // Gemini CLI OAuth UI redirect

	// Cursor
	"api.cursor.sh",  // Cursor CLI auth/billing
	"api2.cursor.sh", // Cursor CLI primary model gateway (since 2026-Q1)

	// Factory Droid
	"api.factory.ai", // Factory Droid (legacy)
	"app.factory.ai", // Factory Droid CLI installer + API base

	// OpenCode BYOK providers (#944) — every provider whose models the
	// frontend registry advertises (lib/cli-adapters.ts OPENCODE_MODELS)
	// and whose API key exec_env.go accepts into the agent env. Without
	// these, restricted-mode crews silently egress-block the provider the
	// user configured.
	"openrouter.ai",    // OpenRouter gateway
	"api.x.ai",         // xAI Grok
	"api.groq.com",     // Groq
	"api.deepseek.com", // DeepSeek
	"api.moonshot.ai",  // Moonshot Kimi (global endpoint)
	"api.z.ai",         // Z.ai GLM
	"api.minimax.io",   // MiniMax (global endpoint)
}

// PackageRegistryDomains is the curated "allow package registries" preset
// (#1377). A Restricted-mode crew can't `npm/pip/cargo/go install` or pull a
// Docker Hub image unless every host those tools dial is on the allowlist —
// enumerating them by hand is the second-biggest Restricted-mode trap. This
// set is what the one-click UI button and the CLI `--allow-package-registries`
// flag append. Kept to explicit, well-known hosts (no wildcards) so the preset
// never widens egress beyond the registries it names. Mirrors the frontend
// list in components/features/crews/registry-presets.ts — keep both in sync.
var PackageRegistryDomains = []string{
	// npm
	"registry.npmjs.org",
	// pip (PyPI)
	"pypi.org",
	"files.pythonhosted.org",
	// cargo (crates.io)
	"crates.io",
	"static.crates.io",
	"index.crates.io",
	// Go modules
	"proxy.golang.org",
	"sum.golang.org",
	// apt (Debian + Ubuntu mirrors)
	"deb.debian.org",
	"security.debian.org",
	"archive.ubuntu.com",
	"security.ubuntu.com",
	"ports.ubuntu.com",
	// Docker Hub image pulls
	"registry-1.docker.io",
	"auth.docker.io",
	"index.docker.io",
	"production.cloudflare.docker.com",
}

// DomainAllowlist controls which outbound domains the agent is allowed to reach.
// Thread-safe.
//
// Two kinds of entry are supported (#1377):
//   - exact — "api.github.com" matches only that host.
//   - wildcard — "*.github.com" matches any subdomain (one or more labels:
//     "raw.github.com", "a.b.github.com") but NOT the apex "github.com" and
//     NOT a look-alike suffix ("notgithub.com", "github.com.evil.com").
//
// Wildcards are stored as their bare suffix (".github.com"); the apex is
// excluded by requiring at least one label before the suffix. Exact lookups
// stay O(1); wildcard matching is a short linear scan over the (typically tiny)
// wildcard set, so it never touches the exact hot path.
type DomainAllowlist struct {
	mu       sync.RWMutex
	exact    map[string]bool
	wildcard map[string]bool // keyed by suffix incl. leading dot, e.g. ".github.com"
}

// NewDomainAllowlist creates an allowlist from the given domains. Entries of
// the form "*.example.com" are treated as subdomain wildcards; everything else
// is an exact host match.
func NewDomainAllowlist(domains []string) *DomainAllowlist {
	al := &DomainAllowlist{
		exact:    make(map[string]bool, len(domains)),
		wildcard: make(map[string]bool),
	}
	for _, d := range domains {
		al.add(d)
	}
	return al
}

// add classifies and stores one entry. Caller holds the write lock (or holds
// no references yet, as during construction).
func (al *DomainAllowlist) add(domain string) {
	d := strings.ToLower(strings.TrimSpace(domain))
	if suffix, ok := wildcardSuffix(d); ok {
		al.wildcard[suffix] = true
		return
	}
	if d != "" {
		al.exact[d] = true
	}
}

// wildcardSuffix reports whether entry is a "*.example.com" wildcard and, if
// so, returns the suffix to match against (".example.com"). A bare "*." with
// no domain after it is rejected — it would match everything and is never what
// the operator meant.
func wildcardSuffix(entry string) (string, bool) {
	if !strings.HasPrefix(entry, "*.") {
		return "", false
	}
	suffix := entry[1:] // keep the leading dot: ".example.com"
	if len(suffix) < 2 {
		return "", false // bare "*." — refuse to allow everything
	}
	return suffix, true
}

// IsAllowed returns true if the host (with optional :port) is on the allowlist,
// by exact match or by a "*." subdomain wildcard. Handles IPv6 addresses
// correctly (e.g. [::1]:443).
func (al *DomainAllowlist) IsAllowed(host string) bool {
	al.mu.RLock()
	defer al.mu.RUnlock()

	h := strings.ToLower(stripPort(host))
	if h == "" {
		return false
	}
	if al.exact[h] {
		return true
	}
	for suffix := range al.wildcard {
		// HasSuffix + a strictly-longer host guarantees at least one label
		// before the suffix, so "*.github.com" matches "raw.github.com" but
		// not the apex "github.com" and not "notgithub.com".
		if len(h) > len(suffix) && strings.HasSuffix(h, suffix) {
			return true
		}
	}
	return false
}

// Add adds a domain (exact or "*." wildcard) to the allowlist.
func (al *DomainAllowlist) Add(domain string) {
	al.mu.Lock()
	defer al.mu.Unlock()
	al.add(domain)
}

// Hash returns a short, deterministic content hash of the current domain
// set — sorted so member ORDER never affects it (domains are already
// lower-cased on insert, so case doesn't either). Advertised on /health so
// the orchestrator (#1160) can tell whether a restricted-mode crew's
// allowlist actually changed since the sidecar started, instead of
// restarting it unconditionally on every exec.
func (al *DomainAllowlist) Hash() string {
	al.mu.RLock()
	domains := make([]string, 0, len(al.exact)+len(al.wildcard))
	for d := range al.exact {
		domains = append(domains, d)
	}
	// Reconstruct the wildcard's canonical "*.example.com" form so a wildcard
	// entry hashes differently from its apex ("github.com" vs "*.github.com").
	for suffix := range al.wildcard {
		domains = append(domains, "*"+suffix)
	}
	al.mu.RUnlock()

	sort.Strings(domains)
	h := sha256.New()
	for _, d := range domains {
		h.Write([]byte(d))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
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
