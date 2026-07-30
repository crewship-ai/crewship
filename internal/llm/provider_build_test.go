package llm

import (
	"strings"
	"testing"
)

// Building an evaluator from a key the OPERATOR picked (#1554) rather than from
// whatever the process environment happens to hold.
//
// The property that matters is that the explicit key is genuinely optional: an
// instance that names no credential must build exactly the provider it built
// before this parameter existed, env fallback and all. Otherwise the feature
// would change behaviour on instances nobody touched.
func TestBuildAuxProviderWithKey(t *testing.T) {
	tests := []struct {
		name     string
		model    AuxModel
		envKey   string
		envVar   string
		explicit string
		wantErr  string
	}{
		{
			name:   "explicit key builds anthropic with no env at all",
			model:  AuxModel{Provider: "anthropic", Model: "claude-haiku-4-5"},
			envVar: "ANTHROPIC_API_KEY", envKey: "",
			explicit: "sk-ant-from-the-vault",
		},
		{
			name:   "explicit key builds openai with no env at all",
			model:  AuxModel{Provider: "openai", Model: "gpt-4o-mini"},
			envVar: "OPENAI_API_KEY", envKey: "",
			explicit: "sk-from-the-vault",
		},
		{
			// The backward-compatibility guarantee: no explicit key means the
			// historical env path, unchanged.
			name:   "no explicit key falls back to the process environment",
			model:  AuxModel{Provider: "anthropic", Model: "claude-haiku-4-5"},
			envVar: "ANTHROPIC_API_KEY", envKey: "sk-ant-from-the-env",
			explicit: "",
		},
		{
			name:   "no key anywhere is still a hard error, not a 401 later",
			model:  AuxModel{Provider: "anthropic", Model: "claude-haiku-4-5"},
			envVar: "ANTHROPIC_API_KEY", envKey: "",
			explicit: "",
			wantErr:  "ANTHROPIC_API_KEY",
		},
		{
			// Ollama is the local judge's endpoint and needs no key; passing one
			// must not turn a working local slot into an error.
			name:   "a key on an ollama slot is ignored, not rejected",
			model:  AuxModel{Provider: "ollama", Model: "qwen2.5:7b"},
			envVar: "KEEPER_OLLAMA_URL", envKey: "",
			explicit: "sk-irrelevant",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.envVar, tt.envKey)
			p, err := BuildAuxProviderWithKey(tt.model, "", tt.explicit)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("built a provider with no key at all")
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("err = %q, want it to mention %q", err.Error(), tt.wantErr)
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

// BuildAuxProviderAt is BuildAuxProviderWithKey with no key — spelled out so the
// two cannot drift into disagreeing about what "no credential" means.
func TestBuildAuxProviderAt_IsTheNoCredentialCase(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	if _, err := BuildAuxProviderAt(AuxModel{Provider: "anthropic", Model: "m"}, ""); err == nil {
		t.Error("BuildAuxProviderAt built an anthropic provider with no key")
	}
	t.Setenv("ANTHROPIC_API_KEY", "sk-env")
	if _, err := BuildAuxProviderAt(AuxModel{Provider: "anthropic", Model: "m"}, ""); err != nil {
		t.Errorf("BuildAuxProviderAt with an env key: %v", err)
	}
}
