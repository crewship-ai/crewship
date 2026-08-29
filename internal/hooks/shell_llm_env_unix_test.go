//go:build unix

// The shell handler's LLM-context env vars, asserted through a real `sh -c`.
//
// This lives in a build-tagged file rather than beside its siblings in
// hooks_test.go with a `runtime.GOOS == "windows"` guard, because a skip
// reports the same "ok" as a pass and the platform answer is knowable at
// compile time. (scripts/skip-budget.txt records the branch that converted
// six such guards to `unix` tags for exactly this reason; adding a seventh
// guard would have been the move that file argues against.)
//
// The command below is sh syntax — `printf` with a single-quoted format and
// double-quoted expansions. On Windows shellCommand runs `cmd.exe /c`, where
// that line means something else entirely, so this is a genuinely unix-only
// assertion rather than a test that merely happens not to have been ported.

package hooks

import (
	"context"
	"testing"
)

// TestShellHandlerExportsLLMContext pins that a shell hook can actually read
// the LLM identity and cost of the call it fired on. EventContext has carried
// LLMProvider / LLMModel / CostUSD since before anything wrote them; now that
// llm.Middleware's hooksCaller populates them on pre_llm_call / post_llm_call,
// the shell handler has to put them somewhere a command can reach — otherwise
// the fields are only visible to HTTP handlers and the whole point of a
// `--handler shell` cost alarm is lost.
func TestShellHandlerExportsLLMContext(t *testing.T) {
	h := Hook{
		HandlerKind: HandlerKindShell,
		HandlerConfig: map[string]any{
			"command": `printf '%s|%s|%s' "$CREWSHIP_LLM_PROVIDER" "$CREWSHIP_LLM_MODEL" "$CREWSHIP_COST_USD"`,
		},
	}
	res, err := shellHandler(context.Background(), h, EventContext{
		Event:       EventPostLLMCall,
		WorkspaceID: "ws_test",
		LLMProvider: "anthropic",
		LLMModel:    "claude-test",
		CostUSD:     0.0105,
	})
	if err != nil {
		t.Fatalf("shell: %v", err)
	}
	payload, _ := res.Payload.(map[string]any)
	if got := payload["stdout"]; got != "anthropic|claude-test|0.0105" {
		t.Fatalf("LLM env output = %q, want %q", got, "anthropic|claude-test|0.0105")
	}
}
