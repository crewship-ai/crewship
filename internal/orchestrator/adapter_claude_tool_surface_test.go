package orchestrator

import (
	"slices"
	"strings"
	"testing"
)

// The tool-surface contract, measured against the real CLI (2.1.226) rather
// than inferred from the flag reference.
//
// `--bare` does not merely skip auto-discovery: it replaces the built-in tool
// catalogue with {Bash, Edit, Read}, and `--tools` can only subtract from that
// set — it can never add back. Measured, with the same --tools value in every
// row:
//
//	--bare --tools "default"                       -> [Bash Edit Read]
//	--bare --tools "<the CODING allowlist>"        -> [Bash Edit Read]
//	--bare --tools "Read,Glob,Grep,ToolSearch"     -> [Read]
//	          --tools "<the CODING allowlist>"     -> [Bash Edit Glob Grep Read ToolSearch WebFetch WebSearch Write]
//
// So every run that carried --bare silently lost Write, Glob, Grep, WebFetch
// and WebSearch no matter what tool_profile said — a CODING agent that cannot
// create a file, a MINIMAL reviewer that cannot grep. That branch was taken for
// API-key credentials only, which is why it survived: the OAuth path (the
// common one) already dropped --bare and has always had the full allowlist.
//
// These tests pin the shape of the fix — no --bare on any auth path — and,
// just as importantly, the flags that now carry the isolation --bare used to
// provide. Deleting one of those is what would make dropping --bare unsafe.

func TestClaudeAdapter_NeverPassesBare(t *testing.T) {
	adapter := claudeCodeAdapter{}

	cases := []struct {
		name string
		req  AgentRunRequest
	}{
		{"no credentials", AgentRunRequest{}},
		{"API key credential", AgentRunRequest{Credentials: []Credential{
			{Type: "API_KEY", PlainValue: "sk-ant-api03-xxx"},
		}}},
		{"OAuth credential by type", AgentRunRequest{Credentials: []Credential{
			{Type: "AI_CLI_TOKEN", PlainValue: "whatever"},
		}}},
		{"OAuth credential by value prefix", AgentRunRequest{Credentials: []Credential{
			{Type: "SECRET", PlainValue: "sk-ant-oat01-xxx"},
		}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			argv := adapter.BuildCommand(tc.req)
			if slices.Contains(argv, "--bare") {
				t.Fatalf("--bare is present: it clamps the built-in tools to Bash/Edit/Read "+
					"and voids the --tools allowlist this same argv asks for (%q)",
					argAfter(argv, "--tools"))
			}
		})
	}
}

// The allowlist is only worth building if it survives to the CLI. Pair each
// profile's expected value with the absence of --bare, because with --bare the
// flag parses fine and then means nothing.
func TestClaudeAdapter_ToolAllowlistIsDeliverable(t *testing.T) {
	adapter := claudeCodeAdapter{}

	cases := []struct {
		profile string
		want    string
	}{
		{"MINIMAL", "Read,Glob,Grep,ToolSearch"},
		{"CODING", "Read,Glob,Grep,Write,Edit,Bash,WebFetch,WebSearch,ToolSearch"},
		{"FULL", "Read,Glob,Grep,Write,Edit,Bash,WebFetch,WebSearch,NotebookEdit,ToolSearch"},
		{"", "Read,Glob,Grep,Write,Edit,Bash,WebFetch,WebSearch,ToolSearch"},
	}
	for _, tc := range cases {
		t.Run(tc.profile, func(t *testing.T) {
			argv := adapter.BuildCommand(AgentRunRequest{ToolProfile: tc.profile})
			if got := argAfter(argv, "--tools"); got != tc.want {
				t.Errorf("--tools = %q, want %q", got, tc.want)
			}
			if slices.Contains(argv, "--bare") {
				t.Errorf("--bare would reduce that allowlist to its intersection with "+
					"{Bash,Edit,Read} — profile %q would arrive as something else entirely", tc.profile)
			}
		})
	}
}

// What --bare used to buy us, now bought explicitly. Verified against 2.1.226
// with a project that had both a CLAUDE.md and a .claude/settings.json
// declaring a SessionStart hook: with --setting-sources "" and no --bare, the
// hook did not fire and the model answered NO when asked whether the repo's
// CLAUDE.md marker was in its context (with and without --system-prompt).
//
// That makes --setting-sources "" load-bearing rather than belt-and-braces: it
// is the flag standing between an agent and a cloned repository's hooks. Same
// for --strict-mcp-config, which keeps MCP to the servers we wrote ourselves.
func TestClaudeAdapter_KeepsTheIsolationBareUsedToProvide(t *testing.T) {
	argv := claudeCodeAdapter{}.BuildCommand(AgentRunRequest{AgentSlug: "ada"})

	if got := argAfter(argv, "--setting-sources"); got != "" || !slices.Contains(argv, "--setting-sources") {
		t.Errorf(`--setting-sources must be present and empty (got present=%v value=%q) — `+
			`it is what stops a cloned repo's .claude/settings.json hooks from running`,
			slices.Contains(argv, "--setting-sources"), got)
	}
	for _, flag := range []string{"--strict-mcp-config", "--no-session-persistence", "--dangerously-skip-permissions"} {
		if !slices.Contains(argv, flag) {
			t.Errorf("%s missing from argv: %v", flag, argv)
		}
	}
}

// A credential list must not change the command shape any more. The old
// adapter branched on it to decide --bare; nothing else ever depended on it,
// so equality here is the cheapest guard against that branch growing back.
func TestClaudeAdapter_ArgvIsIndependentOfCredentialType(t *testing.T) {
	adapter := claudeCodeAdapter{}
	base := AgentRunRequest{AgentSlug: "ada", UserMessage: "hello", ToolProfile: "CODING"}

	oauth := base
	oauth.Credentials = []Credential{{Type: "AI_CLI_TOKEN", PlainValue: "tok"}}
	apiKey := base
	apiKey.Credentials = []Credential{{Type: "API_KEY", PlainValue: "sk-ant-api03-xxx"}}

	got, want := adapter.BuildCommand(oauth), adapter.BuildCommand(apiKey)
	if !slices.Equal(got, want) {
		t.Errorf("argv differs by credential type:\n OAuth: %s\nAPIKey: %s",
			strings.Join(got, " "), strings.Join(want, " "))
	}
}
