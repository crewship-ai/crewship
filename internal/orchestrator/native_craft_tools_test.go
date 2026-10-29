package orchestrator

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNativeCraftToolsRequireHostSelectionAndRestrictOriginalExec(t *testing.T) {
	req := covRunReq()
	req.UserMessage = "synthetic private native instruction"
	req.CrewMCPConfigJSON, req.AgentMCPConfigJSON = "untrusted", "untrusted"
	selectTools, ok := any(req).(interface {
		WithNativeCraftTools() (AgentRunRequest, error)
	})
	if !ok {
		t.Fatal("native Craft tool configurator unavailable")
	}
	private, err := selectTools.WithNativeCraftTools()
	if err != nil {
		t.Fatal(err)
	}
	if !private.strictContentPrivacy || !claudePromptViaStdin(private) || private.mooringDocumentTools || private.CrewMCPConfigJSON != "" || private.AgentMCPConfigJSON != "" {
		t.Fatal("private original native task not isolated")
	}
	args := (claudeCodeAdapter{}).BuildCommand(private)
	for i, arg := range args {
		if arg == req.UserMessage {
			t.Fatal("private instruction in argv")
		}
		if arg == "--tools" && (i+1 == len(args) || args[i+1] != "") {
			t.Fatal("generic builtin tools remain")
		}
	}
	if !strings.Contains(strings.Join(args, " "), "--strict-mcp-config") {
		t.Fatal("missing strict MCP config")
	}
	var decoded AgentRunRequest
	if json.Unmarshal([]byte(`{"nativeCraftTools":true}`), &decoded) != nil {
		t.Fatal("decode")
	}
	if claudePromptViaStdin(decoded) {
		t.Fatal("JSON selected private tools")
	}
	for _, mutate := range []func(*AgentRunRequest){func(r *AgentRunRequest) { r.SkipSidecar = true }, func(r *AgentRunRequest) { r.CLIAdapter = "CODEX" }, func(r *AgentRunRequest) { r.RunID = "" }, func(r *AgentRunRequest) { r.WorkspaceID = "" }} {
		bad := req
		mutate(&bad)
		_, err := any(bad).(interface {
			WithNativeCraftTools() (AgentRunRequest, error)
		}).WithNativeCraftTools()
		if err == nil {
			t.Fatal("unqualified native execution accepted")
		}
	}
}
