package orchestrator

import (
	"strings"
	"testing"
)

// The readiness answer is only worth anything if it agrees with the run. Each
// case therefore states the verdict AND, where the runtime has an observable
// selector for the same input, what that selector does — so a case that
// passes here with the wrong verdict would contradict BuildEnvVarsSidecar or
// buildSidecarCreds on the same credentials.
func TestModelCredentialReadiness(t *testing.T) {
	ghToken := Credential{ID: "gh", EnvVarName: "GH_TOKEN", PlainValue: "ghp_x", Type: "CLI_TOKEN", Provider: "GITHUB"}
	claudeLogin := Credential{ID: "claude-login", EnvVarName: "CLAUDE_CODE_OAUTH_TOKEN", PlainValue: "sk-ant-oat01-x", Type: "AI_CLI_TOKEN", Provider: "ANTHROPIC"}
	anthropicKey := Credential{ID: "anthropic-key", EnvVarName: "ANTHROPIC_API_KEY", PlainValue: "sk-ant-api03-x", Type: "API_KEY", Provider: "ANTHROPIC"}
	openaiKey := Credential{ID: "openai-key", EnvVarName: "OPENAI_API_KEY", PlainValue: "sk-x", Type: "API_KEY", Provider: "OPENAI"}
	openrouterKey := Credential{ID: "openrouter-key", EnvVarName: "OPENROUTER_API_KEY", PlainValue: "or-x", Type: "API_KEY", Provider: "OPENROUTER"}
	googleKey := Credential{ID: "google-key", EnvVarName: "GOOGLE_API_KEY", PlainValue: "g-x", Type: "API_KEY", Provider: "GOOGLE"}
	cursorKey := Credential{ID: "cursor-key", EnvVarName: "CURSOR_API_KEY", PlainValue: "c-x", Type: "API_KEY", Provider: "CURSOR"}
	codexLogin := Credential{ID: "codex-login", EnvVarName: "OPENAI_API_KEY", PlainValue: codexLoginJSON(t, "plus"), Type: "AI_CLI_TOKEN", Provider: "OPENAI"}
	handleOnlyAnthropic := Credential{ID: "anthropic-handle", EnvVarName: "ANTHROPIC_API_KEY", Type: "API_KEY", Provider: "ANTHROPIC", HandleOnly: true}

	cases := []struct {
		name                   string
		adapter, prov, model   string
		creds                  []Credential
		wantState              ModelCredentialState
		wantProvider           string
		wantCredential         string
		wantDelivery           ModelCredentialDelivery
		wantNote               string
		runtimeEnvHas          string // a NAME=VALUE the sidecar env must carry, when ready via env/login_env
		runtimeCredStoreHolds  string // a provider buildSidecarCreds must load, when ready via sidecar
		runtimeCredStoreIsBare bool   // buildSidecarCreds must load nothing, when missing
	}{
		{
			// #2169, exactly: a fresh CLAUDE_CODE agent in a workspace whose
			// only binding is a crew GH_TOKEN. The old guard saw one
			// credential and stayed quiet.
			name: "claude code with only a crew GH_TOKEN binding", adapter: "CLAUDE_CODE", prov: "ANTHROPIC",
			creds: []Credential{ghToken}, wantState: ModelCredentialMissing, wantProvider: "ANTHROPIC",
			runtimeCredStoreIsBare: true,
		},
		{
			name: "claude code with an agent-granted login", adapter: "CLAUDE_CODE", prov: "ANTHROPIC",
			creds: []Credential{ghToken, claudeLogin}, wantState: ModelCredentialReady, wantProvider: "ANTHROPIC",
			wantCredential: "claude-login", wantDelivery: DeliveryLoginEnv,
			runtimeEnvHas: "CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-x",
		},
		{
			// The env holds a DUMMY here by design; the readiness must still
			// say ready, and say why.
			name: "claude code with an API key is served by the sidecar", adapter: "CLAUDE_CODE", prov: "ANTHROPIC",
			creds: []Credential{anthropicKey}, wantState: ModelCredentialReady, wantProvider: "ANTHROPIC",
			wantCredential: "anthropic-key", wantDelivery: DeliverySidecar,
			runtimeCredStoreHolds: "ANTHROPIC", runtimeEnvHas: "ANTHROPIC_API_KEY=sk-ant-dummy-crewship-sidecar",
		},
		{
			name: "claude code with only an OpenAI key", adapter: "CLAUDE_CODE", prov: "ANTHROPIC",
			creds: []Credential{openaiKey}, wantState: ModelCredentialMissing, wantProvider: "ANTHROPIC",
		},
		{
			name: "a handle-only key is not delivered", adapter: "CLAUDE_CODE", prov: "ANTHROPIC",
			creds: []Credential{handleOnlyAnthropic}, wantState: ModelCredentialMissing, wantProvider: "ANTHROPIC",
		},
		{
			name: "codex with an OpenAI key", adapter: "CODEX_CLI", prov: "OPENAI", model: "gpt-5.5",
			creds: []Credential{openaiKey}, wantState: ModelCredentialReady, wantProvider: "OPENAI",
			wantCredential: "openai-key", wantDelivery: DeliverySidecar, runtimeCredStoreHolds: "OPENAI",
		},
		{
			// A ChatGPT login is the same AI_CLI_TOKEN type as a Claude login
			// and lands on disk, never in the CredStore (#2428).
			name: "codex with a ChatGPT login", adapter: "CODEX_CLI", prov: "OPENAI",
			creds: []Credential{codexLogin}, wantState: ModelCredentialReady, wantProvider: "OPENAI",
			wantCredential: "codex-login", wantDelivery: DeliveryLoginFile, runtimeCredStoreIsBare: true,
		},
		{
			name: "claude code with only a ChatGPT login", adapter: "CLAUDE_CODE", prov: "ANTHROPIC",
			creds: []Credential{codexLogin}, wantState: ModelCredentialMissing, wantProvider: "ANTHROPIC",
			runtimeCredStoreIsBare: true,
		},
		{
			name: "codex with a Claude login only", adapter: "CODEX_CLI", prov: "OPENAI",
			creds: []Credential{claudeLogin}, wantState: ModelCredentialMissing, wantProvider: "OPENAI",
		},
		{
			name: "gemini with a Google key", adapter: "GEMINI_CLI", prov: "GOOGLE",
			creds: []Credential{googleKey}, wantState: ModelCredentialReady, wantProvider: "GOOGLE",
			wantCredential: "google-key", wantDelivery: DeliverySidecar, runtimeCredStoreHolds: "GOOGLE",
		},
		{
			name: "opencode with an OpenAI key", adapter: "OPENCODE", prov: "OPENAI", model: "gpt-5.5",
			creds: []Credential{ghToken, openaiKey}, wantState: ModelCredentialReady, wantProvider: "OPENAI",
			wantCredential: "openai-key", wantDelivery: DeliveryEnv, runtimeEnvHas: "OPENAI_API_KEY=sk-x",
		},
		{
			name: "opencode model prefix wins over llm_provider", adapter: "OPENCODE", prov: "OPENAI", model: "anthropic/claude-sonnet-4-5",
			creds: []Credential{openaiKey}, wantState: ModelCredentialMissing, wantProvider: "ANTHROPIC",
		},
		{
			name: "opencode routed provider is served by the sidecar", adapter: "OPENCODE", prov: "OPENROUTER", model: "openrouter/x",
			creds: []Credential{openrouterKey}, wantState: ModelCredentialReady, wantProvider: "OPENROUTER",
			wantCredential: "openrouter-key",
		},
		{
			name: "opencode on the local endpoint has no opinion", adapter: "OPENCODE", prov: "OLLAMA", model: "ollama/llama3",
			creds: nil, wantState: ModelCredentialUnknown, wantNote: "local endpoint",
		},
		{
			name: "opencode naming no provider has no opinion", adapter: "OPENCODE",
			creds: []Credential{openaiKey}, wantState: ModelCredentialUnknown, wantNote: "names no model provider",
		},
		{
			name: "cursor gets the real key in env", adapter: "CURSOR_CLI", prov: "CURSOR",
			creds: []Credential{cursorKey}, wantState: ModelCredentialReady, wantProvider: "CURSOR",
			wantCredential: "cursor-key", wantDelivery: DeliveryEnv, runtimeEnvHas: "CURSOR_API_KEY=c-x",
		},
		{
			name: "unknown adapter has no opinion", adapter: "SOME_NEW_CLI", prov: "ANTHROPIC",
			creds: []Credential{anthropicKey}, wantState: ModelCredentialUnknown, wantNote: "not one the runtime recognises",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ModelCredentialReadiness(tc.adapter, tc.prov, tc.model, tc.creds)
			if got.State != tc.wantState {
				t.Fatalf("state = %q, want %q (%+v)", got.State, tc.wantState, got)
			}
			if got.Provider != tc.wantProvider {
				t.Errorf("provider = %q, want %q", got.Provider, tc.wantProvider)
			}
			if got.CredentialID != tc.wantCredential {
				t.Errorf("credential = %q, want %q", got.CredentialID, tc.wantCredential)
			}
			if tc.wantDelivery != "" && got.Delivery != tc.wantDelivery {
				t.Errorf("delivery = %q, want %q", got.Delivery, tc.wantDelivery)
			}
			if got.Notes == nil {
				t.Errorf("notes must be a non-nil slice so the JSON is [] and not null")
			}
			if tc.wantNote != "" && !strings.Contains(strings.Join(got.Notes, "\n"), tc.wantNote) {
				t.Errorf("notes = %q, want one containing %q", got.Notes, tc.wantNote)
			}
			if got.State == ModelCredentialMissing && len(got.Notes) == 0 {
				t.Errorf("a missing verdict must say what is missing")
			}

			// Agreement with the run itself, on the same credentials.
			req := AgentRunRequest{AgentID: "a", AgentSlug: "a", RunID: "r", CLIAdapter: tc.adapter,
				LLMProvider: tc.prov, LLMModel: tc.model, Credentials: tc.creds, sidecarActive: true}
			if tc.runtimeEnvHas != "" {
				env := BuildEnvVarsSidecar(req, true)
				if !envHasAssignment(env, strings.SplitN(tc.runtimeEnvHas, "=", 2)[0], strings.SplitN(tc.runtimeEnvHas, "=", 2)[1]) {
					t.Errorf("readiness says ready but BuildEnvVarsSidecar lacks %q:\n%s", tc.runtimeEnvHas, strings.Join(env, "\n"))
				}
			}
			if tc.runtimeCredStoreHolds != "" || tc.runtimeCredStoreIsBare {
				sc := buildSidecarCreds(tc.creds, nil)
				held := map[string]bool{}
				for _, c := range sc {
					held[c.Provider] = true
				}
				if tc.runtimeCredStoreHolds != "" && !held[tc.runtimeCredStoreHolds] {
					t.Errorf("readiness says the sidecar serves %s but buildSidecarCreds loads %v", tc.runtimeCredStoreHolds, sc)
				}
				if tc.runtimeCredStoreIsBare && len(sc) != 0 {
					t.Errorf("readiness says missing but buildSidecarCreds loads %v", sc)
				}
			}
		})
	}
}
