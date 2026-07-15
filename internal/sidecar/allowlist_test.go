package sidecar

import (
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
