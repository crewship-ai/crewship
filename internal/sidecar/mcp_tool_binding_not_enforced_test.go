package sidecar

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// TestCallToolHasNoPerToolAllowDenyLookup guards the claim behind #2168:
// mcp_tool_bindings.enabled is documented and commented as advisory
// (model-facing prompt text), NOT gateway-side access control, because on
// the self-hosted/legacy MCP path CallTool has no per-tool allow/deny
// lookup — only a server-level egress/connection check.
//
// This is a trip-wire, not a behavioral test: there is no per-tool field to
// simulate "disabled" against, because none exists. If a future change adds
// one — a ToolBinding/AllowedTools-shaped field on MCPGateway or mcpClient,
// or a reference to the mcp_tool_bindings table inside CallTool — this test
// fails, which is the point: it forces whoever adds enforcement to also
// revisit the now-stale "advisory only" language in
// internal/api/mcp_tool_bindings.go, docs/api-reference/integrations.mdx,
// docs/cli/integration.mdx, and cmd/crewship/cmd_integration*.go.
//
// Verified red-then-green: temporarily adding an `AllowedTools []string`
// field to MCPGateway (or a `mcp_tool_bindings` string literal inside
// CallTool) fails this test; reverting passes it.
func TestCallToolHasNoPerToolAllowDenyLookup(t *testing.T) {
	t.Run("no per-tool field on the structs CallTool reads", func(t *testing.T) {
		suspect := regexp.MustCompile(`(?i)allowedtool|toolbinding|enabledtool|tool_binding|toolallow`)

		for _, typ := range []reflect.Type{
			reflect.TypeOf(MCPGateway{}),
			reflect.TypeOf(mcpClient{}),
		} {
			for i := 0; i < typ.NumField(); i++ {
				name := typ.Field(i).Name
				if suspect.MatchString(name) {
					t.Errorf("%s.%s looks like a per-tool allow/deny field — "+
						"CallTool now has (or is being given) tool-level "+
						"enforcement. Update the 'advisory only' docs/comments "+
						"filed under #2168 alongside this change, then adjust "+
						"or delete this guard.", typ.Name(), name)
				}
			}
		}
	})

	t.Run("CallTool's source has no tool-binding lookup", func(t *testing.T) {
		src := readCallToolSource(t)

		forbidden := []string{
			"mcp_tool_bindings",
			"AllowedTools",
			"allowed_tools",
			"ToolBinding",
			"toolBinding",
		}
		for _, needle := range forbidden {
			if strings.Contains(src, needle) {
				t.Errorf("CallTool's source now mentions %q — that reads like "+
					"a per-tool allow/deny check has been added. If so, "+
					"gateway-side enforcement now exists and the 'advisory "+
					"only' docs/comments filed under #2168 (docs/api-reference/"+
					"integrations.mdx, docs/cli/integration.mdx, "+
					"internal/api/mcp_tool_bindings.go, cmd/crewship/"+
					"cmd_integration*.go) need to be updated to say so.", needle)
			}
		}
	})
}

// readCallToolSource returns just the body of MCPGateway.CallTool from
// mcp_gateway.go in this package, so the source-scan above stays scoped to
// the function the docs cite rather than the whole file (which legitimately
// discusses tool bindings nowhere, but scoping to the function keeps the
// intent obvious if the file grows other MCP helpers later).
func readCallToolSource(t *testing.T) string {
	t.Helper()

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed to resolve this test file's path")
	}
	gatewayPath := filepath.Join(filepath.Dir(thisFile), "mcp_gateway.go")

	data, err := os.ReadFile(gatewayPath)
	if err != nil {
		t.Fatalf("reading %s: %v", gatewayPath, err)
	}

	fnPattern := regexp.MustCompile(`(?s)func \(g \*MCPGateway\) CallTool\(.*?\n\}\n`)
	loc := fnPattern.FindIndex(data)
	if loc == nil {
		t.Fatal("could not locate func (g *MCPGateway) CallTool(...) in mcp_gateway.go — " +
			"has it moved or been renamed? Update this guard's pattern.")
	}
	return string(data[loc[0]:loc[1]])
}
