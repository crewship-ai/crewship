package orchestrator

import (
	"strings"
	"testing"
)

// Regression suite for #1007: OPENCODE agents fail instantly on dev2 with
// "stream ended without terminal result".
//
// Two independent root causes, both pinned here:
//
//  1. FUNCTIONAL — a bare model name ("claude-sonnet-4-6", no "provider/"
//     prefix) is unroutable by OpenCode, which BYOKs across providers and
//     needs the provider baked into the --model value. Live dev2 repro:
//     `opencode run --model claude-sonnet-4-6` → {"type":"error","error":
//     {"name":"UnknownError","data":{"message":"Unexpected server error..."}}}.
//     The prefixed form reached api.anthropic.com fine. BuildCommand must
//     qualify a bare model with the agent's llm_provider.
//
//  2. DIAGNOSTIC — the real opencode error envelope carries `error` as a
//     NESTED OBJECT ({name, data:{message,...}}), not a string. The parser
//     modelled it as a string, so json.Unmarshal of the whole line failed
//     and the error fell through to the plain-text branch: no "error" event
//     was emitted, so streamOutput synthesized a non-error terminal result
//     and the operator never saw the cause. The parser must decode the
//     object form (and keep accepting the legacy string form).

// ---- Functional fix: provider-qualified --model ----

func TestOpencodeBuildCommand_PrefixesBareModelWithProvider(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		model    string
		want     string // expected value passed after --model, or "" for none
	}{
		{"anthropic bare", "ANTHROPIC", "claude-sonnet-4-6", "anthropic/claude-sonnet-4-6"},
		{"openai bare", "OPENAI", "gpt-4o", "openai/gpt-4o"},
		{"google bare", "GOOGLE", "gemini-2.5-pro", "google/gemini-2.5-pro"},
		// Already provider-qualified → untouched.
		{"already prefixed", "ANTHROPIC", "anthropic/claude-sonnet-4-6", "anthropic/claude-sonnet-4-6"},
		// ollama/local path already carries its provider segment.
		{"ollama local", "OLLAMA", "ollama/qwen2.5-coder:7b", "ollama/qwen2.5-coder:7b"},
		// Cross-provider per-call override (#1090 review): a bare model whose
		// name reveals a DIFFERENT provider than the configured one must follow
		// the MODEL, not the agent's static llm_provider. Otherwise a step tier
		// override / CREWSHIP_SUBAGENT_MODEL mis-stamps and misroutes.
		{"override openai on anthropic agent", "ANTHROPIC", "gpt-4o-mini", "openai/gpt-4o-mini"},
		{"override anthropic on openai agent", "OPENAI", "claude-haiku-4-5", "anthropic/claude-haiku-4-5"},
		{"override gemini on anthropic agent", "ANTHROPIC", "gemini-2.5-flash", "google/gemini-2.5-flash"},
		// Name reveals nothing → fall back to configured provider.
		{"unknown model name falls back to provider", "ANTHROPIC", "mystery-model", "anthropic/mystery-model"},
		// No provider known + uninformative name → pass through unchanged
		// (opencode will error, but the parser now surfaces it).
		{"no provider", "", "mystery-model", "mystery-model"},
		// Unknown provider string + uninformative name → pass through.
		{"unknown provider", "MYSTERY", "some-model", "some-model"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := AgentRunRequest{
				CLIAdapter:  "OPENCODE",
				LLMProvider: tc.provider,
				LLMModel:    tc.model,
				UserMessage: "hi",
			}
			cmd := opencodeAdapter{}.BuildCommand(req)
			got := modelFlagValue(cmd)
			if got != tc.want {
				t.Fatalf("--model = %q, want %q (cmd=%v)", got, tc.want, cmd)
			}
		})
	}
}

func TestOpencodeBuildCommand_NoModelFlagWhenEmpty(t *testing.T) {
	req := AgentRunRequest{CLIAdapter: "OPENCODE", LLMProvider: "ANTHROPIC", UserMessage: "hi"}
	cmd := opencodeAdapter{}.BuildCommand(req)
	if v := modelFlagValue(cmd); v != "" {
		t.Fatalf("expected no --model flag for empty model, got %q (cmd=%v)", v, cmd)
	}
}

// modelFlagValue returns the argument following the first --model flag, or "".
func modelFlagValue(cmd []string) string {
	for i, a := range cmd {
		if a == "--model" && i+1 < len(cmd) {
			return cmd[i+1]
		}
	}
	return ""
}

// ---- Diagnostic fix: nested error object surfaces a real error event ----

func TestParseOpenCode_ErrorObject_APIError(t *testing.T) {
	// Exact shape captured from a live dev2 opencode run.
	line := []byte(`{"type":"error","sessionID":"s","error":{"name":"APIError","data":{"message":"invalid x-api-key","statusCode":401}}}`)
	var got []AgentEvent
	newOpenCodeStreamParser().parseLine(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 {
		t.Fatalf("want exactly 1 event (error), got %d: %+v — an object error must NOT fall through to the text branch", len(got), got)
	}
	if got[0].Type != "error" {
		t.Fatalf("event type = %q, want \"error\": %+v", got[0].Type, got[0])
	}
	if !strings.Contains(got[0].Content, "invalid x-api-key") {
		t.Fatalf("error content %q must contain the upstream message", got[0].Content)
	}
}

func TestParseOpenCode_ErrorObject_UnknownErrorIncludesRef(t *testing.T) {
	line := []byte(`{"type":"error","sessionID":"s","error":{"name":"UnknownError","data":{"message":"Unexpected server error. Check server logs for details.","ref":"err_fa9dcbd2"}}}`)
	var got []AgentEvent
	newOpenCodeStreamParser().parseLine(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "error" {
		t.Fatalf("want 1 error event, got %+v", got)
	}
	if !strings.Contains(got[0].Content, "Unexpected server error") {
		t.Errorf("content %q must carry the message", got[0].Content)
	}
	if !strings.Contains(got[0].Content, "err_fa9dcbd2") {
		t.Errorf("content %q should surface the opencode error ref for correlation", got[0].Content)
	}
}

func TestParseOpenCode_ErrorNullPayload_NonEmptyMessage(t *testing.T) {
	// A null (or absent) error payload must still yield a non-empty error
	// message — never an empty error event that hides the failure (#1090 review).
	for _, line := range [][]byte{
		[]byte(`{"type":"error","sessionID":"s","error":null}`),
		[]byte(`{"type":"error","sessionID":"s"}`),
		[]byte(`{"type":"error","sessionID":"s","error":""}`),
	} {
		var got []AgentEvent
		newOpenCodeStreamParser().parseLine(line, func(e AgentEvent) { got = append(got, e) })
		if len(got) != 1 || got[0].Type != "error" {
			t.Fatalf("want 1 error event for %s, got %+v", line, got)
		}
		if strings.TrimSpace(got[0].Content) == "" {
			t.Errorf("error event Content must not be empty for %s", line)
		}
	}
}

func TestParseOpenCode_ErrorString_BackwardCompatible(t *testing.T) {
	// The legacy/simple string form must keep producing an error event.
	line := []byte(`{"type":"error","sessionID":"s","error":"missing API key"}`)
	var got []AgentEvent
	newOpenCodeStreamParser().parseLine(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "error" || got[0].Content != "missing API key" {
		t.Fatalf("string-form error wrong: %+v", got)
	}
}
