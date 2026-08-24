package orchestrator

import (
	"encoding/json"
	"strings"
	"testing"
)

// Every first-party sidecar MCP server authenticates the CALLER with the
// per-agent bearer token (#812): the sidecar handler resolves actingAgentID
// before dispatching a tool and answers
// 403 {"error":"unrecognized agent token"} when the Authorization header is
// absent, because tokensProvisioned() is true for every real container.
//
// These tests exist because that contract was silently one-sided. The memory
// server sent the header; crewship-routines and crewship-notify did not, so
// EVERY save_routine / save_page / discover_capabilities / list_routines /
// run_routine / notify_send call failed with a bare auth error — while
// tools/list still advertised all of them. The model saw the capability,
// called it, and could only report a token problem it had no way to fix; from
// the outside it looked like the agent inventing an excuse for doing nothing.
//
// The failure was invisible to the existing per-injector tests because each
// asserted only its OWN fields (url, alwaysLoad, override-wins) and none
// asserted the header, so nothing compared the three servers against each
// other. These do, by construction: a new first-party server added to the
// table below without the header fails immediately.

// agentTokenHeader is the exact value every first-party server must send.
// ${VAR} is expanded by the CLI from the container env, so the token never
// appears in the config file on disk.
const agentTokenHeader = "Bearer ${CREWSHIP_AGENT_TOKEN}"

func TestFirstPartyMCPSpecs_AllCarryAgentToken(t *testing.T) {
	specs := map[string]mcpSpec{
		"crewship-memory":   memoryMCPSpec("some-agent"),
		"crewship-routines": routinesMCPSpec(),
		"crewship-notify":   notifyMCPSpec(),
	}
	for name, spec := range specs {
		t.Run(name, func(t *testing.T) {
			got := spec.Headers["Authorization"]
			if got != agentTokenHeader {
				t.Errorf("%s Authorization header = %q, want %q — "+
					"the sidecar 403s every tool call on this server without it",
					name, got, agentTokenHeader)
			}
		})
	}
}

// TestFirstPartyMCPClaudeJSON_AllCarryAgentToken is the same invariant on the
// OTHER writer. The spec list feeds the non-Claude adapters; Claude Code gets
// a hand-built .mcp.json entry per injector, and the two are separate code
// paths that have to agree — fixing only one leaves Claude Code, the default
// adapter, still broken.
func TestFirstPartyMCPClaudeJSON_AllCarryAgentToken(t *testing.T) {
	inject := map[string]func(string) (string, error){
		"crewship-memory": func(in string) (string, error) {
			return injectMemoryMCPIntoClaudeJSON(in, "some-agent", true)
		},
		"crewship-routines": injectRoutinesMCPIntoClaudeJSON,
		"crewship-notify":   injectNotifyMCPIntoClaudeJSON,
	}
	for name, fn := range inject {
		t.Run(name, func(t *testing.T) {
			out, err := fn(`{"mcpServers":{}}`)
			if err != nil {
				t.Fatalf("%s: inject error: %v", name, err)
			}
			var doc struct {
				MCPServers map[string]struct {
					Headers map[string]string `json:"headers"`
				} `json:"mcpServers"`
			}
			if err := json.Unmarshal([]byte(out), &doc); err != nil {
				t.Fatalf("%s: emitted invalid JSON: %v", name, err)
			}
			entry, ok := doc.MCPServers[name]
			if !ok {
				t.Fatalf("%s: injector did not add its own server; got %s", name, out)
			}
			if got := entry.Headers["Authorization"]; got != agentTokenHeader {
				t.Errorf("%s .mcp.json Authorization = %q, want %q — "+
					"Claude Code is the default adapter, so this is the path that matters most",
					name, got, agentTokenHeader)
			}
		})
	}
}

// TestFirstPartyMCPClaudeJSON_DoesNotLeakLiteralToken guards the shape of the
// fix as much as its presence: the header must stay a ${VAR} reference the CLI
// expands at run time. Writing the resolved token into .mcp.json would put a
// live per-agent credential on the container filesystem, where an agent that
// reads its own config could echo it back — the exact thing exec_mcp_build.go's
// header comment says must never happen.
func TestFirstPartyMCPClaudeJSON_DoesNotLeakLiteralToken(t *testing.T) {
	for name, out := range map[string]string{
		"crewship-routines": mustInject(t, injectRoutinesMCPIntoClaudeJSON),
		"crewship-notify":   mustInject(t, injectNotifyMCPIntoClaudeJSON),
	} {
		if !strings.Contains(out, "${CREWSHIP_AGENT_TOKEN}") {
			t.Errorf("%s: header is not a ${VAR} reference: %s", name, out)
		}
		for _, leaked := range []string{"agtv1.", "crwv1."} {
			if strings.Contains(out, leaked) {
				t.Errorf("%s: a resolved token prefix %q reached .mcp.json: %s", name, leaked, out)
			}
		}
	}
}

func mustInject(t *testing.T, fn func(string) (string, error)) string {
	t.Helper()
	out, err := fn(`{"mcpServers":{}}`)
	if err != nil {
		t.Fatalf("inject error: %v", err)
	}
	return out
}
