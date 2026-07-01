package orchestrator

// Tests for the oversized-prompt-via-stdin fix.
//
// Linux caps a single argv element at MAX_ARG_STRLEN (128 KiB). The agent's
// user message was passed as a positional argument (`claude ... -- <msg>`), so
// a routine feeding a large web page into an agent_run prompt produced a
// >128 KiB arg and execve failed with E2BIG (agent exits 255, $0.00, run fails
// cryptically). The claude adapter now signals — for messages over the safe
// ceiling — that the prompt should be delivered over stdin, and BuildCommand
// omits the positional argument so execve never sees the oversized element.
//
// Only the Claude Code adapter opts in (its `--print` mode reads the prompt
// from stdin, verified against the real CLI). Every other adapter keeps
// passing the message as an argument unchanged.

import (
	"strings"
	"testing"
)

const stdinTestMaxArg = 128 * 1024 // Linux MAX_ARG_STRLEN

// argvContains reports whether any single argv element equals s.
func argvContains(cmd []string, s string) bool {
	for _, a := range cmd {
		if a == s {
			return true
		}
	}
	return false
}

// maxArgLen returns the byte length of the longest argv element.
func maxArgLen(cmd []string) int {
	max := 0
	for _, a := range cmd {
		if len(a) > max {
			max = len(a)
		}
	}
	return max
}

func TestClaudeAdapter_PromptViaStdin_SizeGated(t *testing.T) {
	small := AgentRunRequest{CLIAdapter: "CLAUDE_CODE", AgentSlug: "a", UserMessage: "hello"}
	large := AgentRunRequest{CLIAdapter: "CLAUDE_CODE", AgentSlug: "a", UserMessage: strings.Repeat("A", 200*1024)}

	if (claudeCodeAdapter{}).PromptViaStdin(small) {
		t.Error("small message must NOT route via stdin (keeps tmux + arg path)")
	}
	if !(claudeCodeAdapter{}).PromptViaStdin(large) {
		t.Error("oversized message MUST route via stdin to dodge E2BIG")
	}
}

func TestClaudeAdapter_BuildCommand_SmallKeepsArg(t *testing.T) {
	msg := "do the thing"
	req := AgentRunRequest{CLIAdapter: "CLAUDE_CODE", AgentSlug: "a", UserMessage: msg}
	cmd := claudeCodeAdapter{}.BuildCommand(req)

	// Small message: unchanged behaviour — `-- <msg>` as the final two args.
	if len(cmd) < 2 || cmd[len(cmd)-1] != msg || cmd[len(cmd)-2] != "--" {
		t.Fatalf("small message must end with `-- %q`; got tail %v", msg, cmd[max(0, len(cmd)-3):])
	}
}

func TestClaudeAdapter_BuildCommand_LargeOmitsArg(t *testing.T) {
	msg := strings.Repeat("B", 200*1024) // 200 KiB, well over MAX_ARG_STRLEN
	req := AgentRunRequest{CLIAdapter: "CLAUDE_CODE", AgentSlug: "a", UserMessage: msg}
	cmd := claudeCodeAdapter{}.BuildCommand(req)

	if argvContains(cmd, msg) {
		t.Error("oversized message must NOT appear as a positional argv element")
	}
	// The `--` separator only exists to guard the positional message; with the
	// message gone there must be no trailing `--` either.
	if len(cmd) > 0 && cmd[len(cmd)-1] == "--" {
		t.Error("trailing `--` separator must be dropped when the message goes via stdin")
	}
	// The load-bearing guarantee: no argv element approaches the kernel limit.
	if got := maxArgLen(cmd); got >= stdinTestMaxArg {
		t.Errorf("largest argv element = %d bytes, must stay under MAX_ARG_STRLEN (%d)", got, stdinTestMaxArg)
	}
	// Sanity: the bounded --system-prompt arg is still present.
	if !argvContains(cmd, "--system-prompt") {
		t.Error("--system-prompt must remain (it is bounded; only the user message is unbounded)")
	}
}

func TestOtherAdapters_NeverStdin_AndKeepArg(t *testing.T) {
	bigMsg := strings.Repeat("C", 200*1024)
	cases := []struct {
		name    string
		adapter CLIAdapter
		cliID   string
	}{
		{"codex", codexAdapter{}, "CODEX_CLI"},
		{"gemini", geminiAdapter{}, "GEMINI_CLI"},
		{"opencode", opencodeAdapter{}, "OPENCODE"},
		{"cursor", cursorAdapter{}, "CURSOR_CLI"},
		{"droid", droidAdapter{}, "FACTORY_DROID"},
		{"unknown", unknownAdapter{}, "WHATEVER"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := AgentRunRequest{CLIAdapter: tc.cliID, AgentSlug: "a", UserMessage: bigMsg}
			if tc.adapter.PromptViaStdin(req) {
				t.Errorf("%s must NOT route via stdin (not verified to read stdin)", tc.name)
			}
			// Regression guard: these adapters still embed the message as an arg.
			cmd := tc.adapter.BuildCommand(req)
			joined := strings.Join(cmd, "\x00")
			if !strings.Contains(joined, bigMsg) {
				t.Errorf("%s must still pass the message as an argv element", tc.name)
			}
		})
	}
}
