package orchestrator

import (
	"slices"
	"testing"
)

// getAdapter falls back to unknownAdapter for any cli_adapter string not in the
// registry — a typo, a value written by an older or newer schema, a hand-edited
// agent row. That fallback exists so a malformed record cannot crash the
// orchestrator, and it runs the REAL `claude` binary inside the crew container.
//
// Which makes the flags it omits a security question, not a completeness one.
// adapter_claude.go's own comment calls --setting-sources "" "what stands
// between an agent and a cloned repository's hooks" — it is the control that
// replaced --bare. On this path a cloned repo's .claude/settings.json
// SessionStart hook executes and its CLAUDE.md is injected, with nothing in the
// journal saying the run was degraded.
//
// "Minimal enough for debugging" is a fine goal for the prompt and the tool
// surface. It is not a reason to drop the isolation.
func TestUnknownAdapter_CarriesTheIsolationFlags(t *testing.T) {
	argv := unknownAdapter{}.BuildCommand(AgentRunRequest{UserMessage: "hello"})

	if got := argAfter(argv, "--setting-sources"); got != "" || !slices.Contains(argv, "--setting-sources") {
		t.Errorf(`--setting-sources must be present and empty (present=%v value=%q); argv=%v`,
			slices.Contains(argv, "--setting-sources"), got, argv)
	}
	if !slices.Contains(argv, "--strict-mcp-config") {
		t.Errorf("--strict-mcp-config missing: the run inherits whatever MCP config the workspace happens to have; argv=%v", argv)
	}
}

// A user message beginning with a dash is parsed as a flag without the `--`
// guard the Claude adapter uses. Same class of bug, and on this path it lands
// on a command line assembled from a record we already know is malformed.
func TestUnknownAdapter_GuardsTheMessageAgainstFlagParsing(t *testing.T) {
	argv := unknownAdapter{}.BuildCommand(AgentRunRequest{UserMessage: "--version please"})

	sep := slices.Index(argv, "--")
	if sep < 0 {
		t.Fatalf("no `--` separator before the user message; argv=%v", argv)
	}
	if sep != len(argv)-2 || argv[len(argv)-1] != "--version please" {
		t.Errorf("the message must be the only thing after `--`; argv=%v", argv)
	}
}
