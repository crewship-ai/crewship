// Package egressallow is the dependency-free leaf holding the crew egress
// domain allowlist primitive — the exact-match, port/case-insensitive host
// check the sidecar proxy enforces for in-container egress. It carries NO
// crewship imports (std-lib only) so it can be shared by BOTH the sidecar
// package (which enforces the allowlist on agent-container egress) AND
// internal/egresspolicy (which resolves the same boundary for the app-layer
// paths — routine http steps, notify/webhook channels, hooks, and the MCP
// gateway). Keeping it a leaf is what lets the sidecar's MCP gateway build its
// gated client through egresspolicy without an import cycle: egresspolicy no
// longer reaches back into sidecar for this type.
//
// internal/sidecar re-exports these symbols via type/var/func aliases, so its
// existing call sites (and the ~40 tests that reference NewDomainAllowlist /
// DefaultAllowedDomains) are unchanged.
package egressallow

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

	h := StripPort(host)
	return al.domains[strings.ToLower(h)]
}

// Add adds a domain to the allowlist.
func (al *DomainAllowlist) Add(domain string) {
	al.mu.Lock()
	defer al.mu.Unlock()
	al.domains[strings.ToLower(domain)] = true
}

// Hash returns a short, deterministic content hash of the current domain
// set — sorted so member ORDER never affects it (domains are already
// lower-cased on insert, so case doesn't either). Advertised on /health so
// the orchestrator (#1160) can tell whether a restricted-mode crew's
// allowlist actually changed since the sidecar started, instead of
// restarting it unconditionally on every exec.
func (al *DomainAllowlist) Hash() string {
	al.mu.RLock()
	domains := make([]string, 0, len(al.domains))
	for d := range al.domains {
		domains = append(domains, d)
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

// StripPort removes the port from a host string, handling IPv6 bracket notation.
func StripPort(host string) string {
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
