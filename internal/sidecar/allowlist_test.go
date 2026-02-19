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
