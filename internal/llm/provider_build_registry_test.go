package llm

import (
	"strings"
	"testing"
)

// BuildAuxProviderWithKey after the switch became a registry lookup. The
// existing provider_build_test.go still pins the behaviour operators depend on;
// what is new here is the behaviour that used to be a literal — the hint string,
// the case-insensitivity, and the base URL resolving through
// ProviderSpec.BaseEnv/BaseDefault instead of an ollama-shaped branch.

// The unknown-provider hint is generated from the registry now, so it cannot
// drift from what the builder accepts. It must still render what it always
// rendered.
func TestBuildAuxProvider_UnknownProviderHint(t *testing.T) {
	tests := []struct {
		name     string
		provider string
	}{
		{"unknown vendor", "cohere"},
		// Rejected with a reason rather than accepted into a slot that fails at
		// first use: there is no Gemini Provider in this package.
		{"google", "google"},
		{"gemini", "gemini"},
		{"empty", ""},
		{"a model id in the provider field", "claude-haiku-4-5"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := BuildAuxProviderWithKey(AuxModel{Provider: tt.provider, Model: "m"}, "", "k")
			if err == nil {
				t.Fatalf("built a provider for %q", tt.provider)
			}
			if !strings.Contains(err.Error(), "unsupported aux provider") {
				t.Errorf("err = %q, want it to say unsupported aux provider", err)
			}
			if !strings.Contains(err.Error(), "anthropic|openai|ollama") {
				t.Errorf("err = %q, want it to list anthropic|openai|ollama", err)
			}
		})
	}
}

// The deliberate widening: internal/api carries the provider as an uppercase
// enum value and keepercfg stores it lowercase, so the exact-match switch made
// the same slot buildable on one side of a call and not the other.
func TestBuildAuxProvider_ProviderMatchingIsCaseAndSpaceInsensitive(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		wantName string
	}{
		{"lowercase", "anthropic", "anthropic"},
		{"uppercase enum form", "ANTHROPIC", "anthropic"},
		{"padded and capitalised", " Anthropic ", "anthropic"},
		{"openai mixed case", "OpenAI", "openai"},
		{"ollama padded", "  ollama  ", "ollama"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := BuildAuxProviderWithKey(AuxModel{Provider: tt.provider, Model: "m"}, "", "sk-explicit")
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if got := p.Name(); got != tt.wantName {
				t.Errorf("Name() = %q, want %q", got, tt.wantName)
			}
		})
	}
}

// Key resolution per provider: explicit wins, env is the fallback, and neither
// is a hard error naming the variable — the error text internal/server logs and
// provider_build_test.go greps for.
//
// Ollama declares no KeyEnv, so it must build with no key anywhere AND ignore
// one that is handed to it; a local judge that started demanding a credential
// would be a regression, not a tightening.
func TestBuildAuxProvider_KeyResolution(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		envVar   string
		envVal   string
		explicit string
		wantErr  string
	}{
		{
			name:     "anthropic explicit key, empty env",
			provider: "anthropic", envVar: "ANTHROPIC_API_KEY", envVal: "",
			explicit: "sk-ant-explicit",
		},
		{
			name:     "anthropic falls back to env",
			provider: "anthropic", envVar: "ANTHROPIC_API_KEY", envVal: "sk-ant-env",
		},
		{
			name:     "anthropic with no key anywhere",
			provider: "anthropic", envVar: "ANTHROPIC_API_KEY", envVal: "",
			wantErr: `ANTHROPIC_API_KEY env not set (required for anthropic aux slot "m")`,
		},
		{
			name:     "openai explicit key, empty env",
			provider: "openai", envVar: "OPENAI_API_KEY", envVal: "",
			explicit: "sk-explicit",
		},
		{
			name:     "openai falls back to env",
			provider: "openai", envVar: "OPENAI_API_KEY", envVal: "sk-env",
		},
		{
			name:     "openai with no key anywhere",
			provider: "openai", envVar: "OPENAI_API_KEY", envVal: "",
			wantErr: `OPENAI_API_KEY env not set (required for openai aux slot "m")`,
		},
		{
			name:     "ollama needs no key at all",
			provider: "ollama", envVar: "KEEPER_OLLAMA_URL", envVal: "",
		},
		{
			name:     "a key on an ollama slot is ignored, not rejected",
			provider: "ollama", envVar: "KEEPER_OLLAMA_URL", envVal: "",
			explicit: "sk-irrelevant",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.envVar, tt.envVal)
			p, err := BuildAuxProviderWithKey(AuxModel{Provider: tt.provider, Model: "m"}, "", tt.explicit)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatal("built a provider with no key at all")
				}
				if err.Error() != tt.wantErr {
					t.Errorf("err = %q, want %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if p == nil {
				t.Fatal("nil provider with no error")
			}
		})
	}
}

// Base resolution: explicit argument, then ProviderSpec.BaseEnv, then
// BaseDefault. This is the branch that used to be spelled out for ollama; the
// values must land identically now that they come from the spec.
func TestBuildAuxProvider_OllamaBaseResolution(t *testing.T) {
	tests := []struct {
		name     string
		explicit string
		env      string
		want     string
	}{
		{"explicit base wins", "http://box:11434", "http://env:11434", "http://box:11434"},
		{"explicit base wins over an unset env", "http://box:11434", "", "http://box:11434"},
		{"whitespace-only base is not a base", "   ", "http://env:11434", "http://env:11434"},
		{"env is the fallback", "", "http://env:11434", "http://env:11434"},
		{"neither means localhost", "", "", "http://localhost:11434"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("KEEPER_OLLAMA_URL", tt.env)
			p, err := BuildAuxProviderWithKey(AuxModel{Provider: "ollama", Model: "qwen2.5:7b"}, tt.explicit, "")
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			o, isOllama := p.(*Ollama)
			if !isOllama {
				t.Fatalf("built a %T, want *Ollama", p)
			}
			if o.baseURL != tt.want {
				t.Errorf("baseURL = %q, want %q", o.baseURL, tt.want)
			}
			if o.model != "qwen2.5:7b" {
				t.Errorf("model = %q, want the AuxModel's model", o.model)
			}
		})
	}
}

// The keyed providers dial OUR endpoint, not an operator-supplied one — the
// server key is attached to the request, so an explicit base must not redirect
// it. Neither spec declares a BaseEnv, and both constructors ignore the base
// they are handed.
func TestBuildAuxProvider_KeyedProvidersIgnoreTheBase(t *testing.T) {
	for _, provider := range []string{"anthropic", "openai"} {
		t.Run(provider, func(t *testing.T) {
			spec, ok := LookupProvider(provider)
			if !ok {
				t.Fatalf("%q not registered", provider)
			}
			if spec.BaseEnv != "" {
				t.Errorf("BaseEnv = %q, want empty: a keyed provider must not be endpoint-driven", spec.BaseEnv)
			}
			p, err := BuildAuxProviderWithKey(
				AuxModel{Provider: provider, Model: "m"}, "http://attacker.example", "sk-explicit")
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if got := p.Name(); got != provider {
				t.Errorf("Name() = %q, want %q", got, provider)
			}
		})
	}
}
