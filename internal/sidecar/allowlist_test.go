package sidecar

import (
	"strings"
	"testing"
)

func TestDomainAllowlist(t *testing.T) {
	al := NewDomainAllowlist([]string{"api.anthropic.com", "api.openai.com"})

	tests := []struct {
		host    string
		allowed bool
	}{
		{"api.anthropic.com", true},
		{"api.anthropic.com:443", true},
		{"API.ANTHROPIC.COM", true},
		{"api.openai.com", true},
		{"evil.com", false},
		{"api.anthropic.com.evil.com", false},
		{"", false},
	}
	for _, tt := range tests {
		if al.IsAllowed(tt.host) != tt.allowed {
			t.Errorf("IsAllowed(%q) = %v, want %v", tt.host, !tt.allowed, tt.allowed)
		}
	}
}

func TestDomainAllowlistIPv6(t *testing.T) {
	al := NewDomainAllowlist([]string{"::1", "api.anthropic.com"})
	tests := []struct {
		host    string
		allowed bool
	}{
		{"[::1]:443", true},
		{"[::1]:9119", true},
		{"::1", true},
		{"api.anthropic.com:443", true},
		{"[::2]:443", false},
	}
	for _, tt := range tests {
		if al.IsAllowed(tt.host) != tt.allowed {
			t.Errorf("IsAllowed(%q) = %v, want %v", tt.host, !tt.allowed, tt.allowed)
		}
	}
}

func TestDomainAllowlistAdd(t *testing.T) {
	al := NewDomainAllowlist(nil)
	if al.IsAllowed("custom.api.com") {
		t.Error("should not be allowed before add")
	}

	al.Add("custom.api.com")
	if !al.IsAllowed("custom.api.com") {
		t.Error("should be allowed after add")
	}
}

// TestDomainAllowlistHash is #1160: the orchestrator compares this hash
// against an independently-computed one (same domains, desired policy) to
// decide whether a restricted-mode sidecar actually needs restarting,
// instead of restarting unconditionally on every exec. The hash must be
// insensitive to input order and case (both sides may build their domain
// list differently) and must change when the domain SET changes.
func TestDomainAllowlistHash(t *testing.T) {
	a := NewDomainAllowlist([]string{"api.anthropic.com", "api.openai.com"})
	b := NewDomainAllowlist([]string{"API.OPENAI.COM", "api.anthropic.com"}) // reordered + different case
	if a.Hash() != b.Hash() {
		t.Errorf("hash should be order/case-insensitive: a=%q b=%q", a.Hash(), b.Hash())
	}

	c := NewDomainAllowlist([]string{"api.anthropic.com", "evil.com"})
	if a.Hash() == c.Hash() {
		t.Errorf("hash should differ when the domain set differs: a=%q c=%q", a.Hash(), c.Hash())
	}

	d := NewDomainAllowlist([]string{"api.anthropic.com", "api.openai.com"})
	d.Add("new.example.com")
	if d.Hash() == a.Hash() {
		t.Error("hash must reflect domains added after construction")
	}

	empty := NewDomainAllowlist(nil)
	if empty.Hash() == "" {
		t.Error("hash of an empty allowlist must still be a stable non-empty value")
	}
}

func TestProviderForHost(t *testing.T) {
	tests := []struct {
		host     string
		expected ProviderType
	}{
		{"api.anthropic.com", ProviderAnthropic},
		{"api.anthropic.com:443", ProviderAnthropic},
		{"api.openai.com", ProviderOpenAI},
		{"generativelanguage.googleapis.com", ProviderGoogle},
		{"unknown.com", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := providerForHost(tt.host)
		if got != tt.expected {
			t.Errorf("providerForHost(%q) = %q, want %q", tt.host, got, tt.expected)
		}
	}
}

// #944 — OpenCode is BYOK across providers; the default allowlist must
// cover every provider whose API key we accept into the agent env
// (exec_env.go apiKeyEnvVarsForAdapter) and whose models the frontend
// registry advertises (lib/cli-adapters.ts OPENCODE_MODELS). Otherwise
// restricted-mode crews silently egress-block the provider the user paid for.
func TestDefaultAllowedDomains_CoverOpenCodeBYOKProviders(t *testing.T) {
	al := NewDomainAllowlist(DefaultAllowedDomains)
	for _, host := range []string{
		"openrouter.ai",    // OpenRouter
		"api.x.ai",         // xAI Grok
		"api.groq.com",     // Groq
		"api.deepseek.com", // DeepSeek
		"api.moonshot.ai",  // Moonshot Kimi (global endpoint)
		"api.z.ai",         // Z.ai GLM
		"api.minimax.io",   // MiniMax (global endpoint)
	} {
		if !al.IsAllowed(host) {
			t.Errorf("DefaultAllowedDomains missing BYOK provider host %q", host)
		}
	}
}

// TestDomainAllowlist_Wildcard is #1377: an exact-match allowlist is the
// single biggest Restricted-mode trap — a user who adds "github.com" is
// surprised that raw.githubusercontent.com / codeload.github.com stay
// blocked. A "*.example.com" entry must match any subdomain (one or more
// labels) but must NOT match the apex itself, an unrelated suffix, or a
// look-alike that merely ends in the same text.
func TestDomainAllowlist_Wildcard(t *testing.T) {
	al := NewDomainAllowlist([]string{"*.github.com", "api.anthropic.com"})
	tests := []struct {
		host    string
		allowed bool
	}{
		{"raw.github.com", true},         // direct subdomain
		{"codeload.github.com", true},    // direct subdomain
		{"objects.a.b.github.com", true}, // deep subdomain
		{"RAW.GITHUB.COM", true},         // case-insensitive
		{"raw.github.com:443", true},     // port stripped
		{"github.com", false},            // apex is NOT covered by "*."
		{"notgithub.com", false},         // suffix-text look-alike
		{"github.com.evil.com", false},   // suffix injection
		{"evilgithub.com", false},        // no dot boundary
		{"api.anthropic.com", true},      // exact entry still works alongside wildcard
		{"other.anthropic.com", false},   // exact entry does NOT become a wildcard
	}
	for _, tt := range tests {
		if al.IsAllowed(tt.host) != tt.allowed {
			t.Errorf("IsAllowed(%q) = %v, want %v", tt.host, !tt.allowed, tt.allowed)
		}
	}
}

// TestDomainAllowlist_WildcardAddAndHash verifies wildcards added after
// construction take effect and that the /health hash (#1160) reflects
// wildcard membership — otherwise the orchestrator can't tell a
// wildcard-only policy change apart and skips a needed sidecar restart.
func TestDomainAllowlist_WildcardAddAndHash(t *testing.T) {
	al := NewDomainAllowlist(nil)
	if al.IsAllowed("raw.githubusercontent.com") {
		t.Fatal("should not be allowed before add")
	}
	al.Add("*.githubusercontent.com")
	if !al.IsAllowed("raw.githubusercontent.com") {
		t.Error("wildcard should be allowed after Add")
	}
	if al.IsAllowed("githubusercontent.com") {
		t.Error("Add(*.x) must not allow the apex")
	}

	a := NewDomainAllowlist([]string{"*.github.com"})
	b := NewDomainAllowlist([]string{"github.com"})
	if a.Hash() == b.Hash() {
		t.Errorf("hash must distinguish a wildcard from its apex: a=%q b=%q", a.Hash(), b.Hash())
	}
}

// TestPackageRegistryDomains_Preset is #1377: the one-click "allow package
// registries" preset must contain the hosts the common language + OS
// package managers actually dial, so a Restricted crew can npm/pip/cargo/go
// install without the user enumerating every host by hand.
func TestPackageRegistryDomains_Preset(t *testing.T) {
	al := NewDomainAllowlist(PackageRegistryDomains)
	for _, host := range []string{
		"registry.npmjs.org",     // npm
		"pypi.org",               // pip
		"files.pythonhosted.org", // pip wheels
		"crates.io",              // cargo
		"static.crates.io",       // cargo downloads
		"proxy.golang.org",       // go modules
		"sum.golang.org",         // go checksum db
		"deb.debian.org",         // apt (debian)
		"archive.ubuntu.com",     // apt (ubuntu)
		"registry-1.docker.io",   // docker hub pulls
	} {
		if !al.IsAllowed(host) {
			t.Errorf("PackageRegistryDomains missing registry host %q", host)
		}
	}
	// The preset must not smuggle in a wildcard that opens unrelated egress.
	for _, d := range PackageRegistryDomains {
		if strings.HasPrefix(d, "*.") {
			t.Errorf("registry preset entry %q is a wildcard; presets should be explicit hosts", d)
		}
	}
}
