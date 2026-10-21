package orchestrator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/crewship-ai/crewship/internal/provider"
	"log/slog"
	"strings"
	"testing"
)

func TestIssuesMCPInjectionCarriesRunTokenAndPreservesOverrides(t *testing.T) {
	spec := issuesMCPSpec()
	if spec.Headers["Authorization"] != "Bearer ${CREWSHIP_AGENT_TOKEN}" || spec.URL != "http://127.0.0.1:9119/mcp/issues" {
		t.Fatalf("%+v", spec)
	}
	specs := injectIssuesMCP(nil)
	if len(specs) != 1 || len(injectIssuesMCP(specs)) != 1 {
		t.Fatal("injector not idempotent")
	}
	input := `{"other":"keep","mcpServers":{"custom":{"url":"http://custom"}}}`
	output, err := injectIssuesMCPIntoClaudeJSON(input)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err = json.Unmarshal([]byte(output), &doc); err != nil {
		t.Fatal(err)
	}
	if string(doc["other"]) != `"keep"` || !strings.Contains(output, "alwaysLoad") || !strings.Contains(output, IssuesMCPServerName) {
		t.Fatal(output)
	}
	again, err := injectIssuesMCPIntoClaudeJSON(output)
	if err != nil || again != output {
		t.Fatal("JSON injector not idempotent")
	}
	override := `{"mcpServers":{"crewship-issues":{"url":"http://user"}}}`
	again, err = injectIssuesMCPIntoClaudeJSON(override)
	if err != nil || again != override {
		t.Fatal("overrode user MCP")
	}
}

func TestIssuesMCPAllSupportedWritersEmitAuthenticatedEndpoint(t *testing.T) {
	writers := map[string]func(context.Context, provider.ContainerProvider, string, AgentRunRequest, string, *slog.Logger) error{"codex": writeMCPCodex, "gemini": writeMCPGemini, "droid": writeMCPDroid, "opencode": writeMCPOpenCode}
	for name, writer := range writers {
		t.Run(name, func(t *testing.T) {
			capture := &captureContainer{}
			request := AgentRunRequest{AgentSlug: "agent-x", RunID: "issues-run"}
			if err := writer(t.Context(), capture, "synthetic-container", request, "/output/agent-x", slog.Default()); err != nil {
				t.Fatal(err)
			}
			combined := ""
			for _, call := range capture.calls {
				combined += call.body
			}
			if !strings.Contains(combined, IssuesMCPServerName) || !strings.Contains(combined, "/mcp/issues") || !strings.Contains(combined, "CREWSHIP_AGENT_TOKEN") {
				t.Fatalf("missing authenticated issues endpoint: %s", combined)
			}
		})
	}
	t.Run("claude", func(t *testing.T) {
		capture := &covContainer{route: covHealthySidecar}
		if err := setupMCPConfig(t.Context(), capture, "synthetic-container", "agent-x", "issues-run", "", "", nil, covQuietLogger()); err != nil {
			t.Fatal(err)
		}
		combined := ""
		for _, call := range capture.snapshotCalls() {
			match := covMCPWriteRE.FindStringSubmatch(covScript(call))
			if len(match) == 3 {
				decoded, err := base64.StdEncoding.DecodeString(match[1])
				if err != nil {
					t.Fatal(err)
				}
				combined += string(decoded)
			}
		}
		if !strings.Contains(combined, IssuesMCPServerName) || !strings.Contains(combined, "/mcp/issues") || !strings.Contains(combined, "CREWSHIP_AGENT_TOKEN") {
			t.Fatalf("missing authenticated Claude issues endpoint: %s", combined)
		}
	})
}
