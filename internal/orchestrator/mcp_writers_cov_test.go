package orchestrator

// Coverage tests for mcp_writers.go gap branches: normaliseMCPInputs error
// paths + legacy list precedence, parseMCPServerJSON variants, Codex TOML
// header/env representations, OpenCode entry skipping, and tomlString
// escaping. Reuses captureContainer from mcp_writers_test.go.

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

func TestNormaliseMCPInputs_MergeErrorFromAgentJSON(t *testing.T) {
	t.Parallel()
	_, err := normaliseMCPInputs(AgentRunRequest{
		CrewMCPConfigJSON:  `{"mcpServers":{}}`,
		AgentMCPConfigJSON: `{broken`,
	})
	if err == nil || !strings.Contains(err.Error(), "merge MCP configs") {
		t.Fatalf("expected merge error, got %v", err)
	}
}

func TestNormaliseMCPInputs_BadCrewJSON(t *testing.T) {
	t.Parallel()
	// Crew JSON alone is NOT validated by the merge step (agent JSON empty),
	// so the unmarshal of the merged blob is what must fail.
	_, err := normaliseMCPInputs(AgentRunRequest{CrewMCPConfigJSON: `{not json`})
	if err == nil || !strings.Contains(err.Error(), "unmarshal merged MCP JSON") {
		t.Fatalf("expected unmarshal error, got %v", err)
	}
}

func TestNormaliseMCPInputs_MalformedServerEntry(t *testing.T) {
	t.Parallel()
	_, err := normaliseMCPInputs(AgentRunRequest{
		CrewMCPConfigJSON: `{"mcpServers":{"bad":"just-a-string"}}`,
	})
	if err == nil || !strings.Contains(err.Error(), `malformed MCP server "bad"`) {
		t.Fatalf("expected malformed-server error, got %v", err)
	}
}

func TestNormaliseMCPInputs_LegacyListAndPrecedence(t *testing.T) {
	t.Parallel()
	req := AgentRunRequest{
		CrewMCPConfigJSON: `{"mcpServers":{"dup":{"command":"raw-wins"}}}`,
		MCPServers: []MCPServerConfig{
			{Name: ""}, // skipped
			{Name: "dup", Command: "legacy-loses"},
			{Name: "legacy", Transport: "stdio", Command: "node", Args: []string{"s.js"}, Endpoint: ""},
		},
	}
	specs, err := normaliseMCPInputs(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(specs) != 2 {
		t.Fatalf("want 2 specs (dup + legacy), got %d: %+v", len(specs), specs)
	}
	// Sorted: dup, legacy.
	if specs[0].Name != "dup" || specs[0].Command != "raw-wins" {
		t.Errorf("raw JSON must win over legacy list: %+v", specs[0])
	}
	if specs[1].Name != "legacy" || specs[1].Command != "node" {
		t.Errorf("legacy entry lost: %+v", specs[1])
	}
}

func TestParseMCPServerJSON_HTTPURLFallbackAndTransport(t *testing.T) {
	t.Parallel()
	spec, err := parseMCPServerJSON("g", []byte(`{"httpUrl":"https://h.example/mcp"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.URL != "https://h.example/mcp" {
		t.Errorf("httpUrl must populate URL, got %q", spec.URL)
	}
	if spec.Transport != "http" {
		t.Errorf("transport must default to http for url-only entries, got %q", spec.Transport)
	}
}

func TestTranslateEnvRefsToCursor_BareForm(t *testing.T) {
	t.Parallel()
	out := translateEnvRefsToCursor(map[string]string{
		"A": "$TOKEN",
		"B": "prefix ${OTHER} suffix",
		"C": "literal",
	})
	if out["A"] != "${env:TOKEN}" {
		t.Errorf("bare $VAR not translated: %q", out["A"])
	}
	if out["B"] != "prefix ${env:OTHER} suffix" {
		t.Errorf("embedded ref not translated: %q", out["B"])
	}
	if out["C"] != "literal" {
		t.Errorf("literal must pass through: %q", out["C"])
	}
}

func TestWriters_PropagateNormaliseError(t *testing.T) {
	t.Parallel()
	writers := map[string]func(context.Context, provider.ContainerProvider, string, AgentRunRequest, string, *slog.Logger) error{
		"droid":    writeMCPDroid,
		"gemini":   writeMCPGemini,
		"opencode": writeMCPOpenCode,
		"codex":    writeMCPCodex,
		"cursor":   writeMCPCursor,
	}
	req := AgentRunRequest{AgentSlug: "agent-x", CrewMCPConfigJSON: `{bad`}
	for name, w := range writers {
		t.Run(name, func(t *testing.T) {
			cap := &captureContainer{}
			err := w(context.Background(), cap, "ctr1", req, "/output/agent-x", covQuietLogger())
			if err == nil {
				t.Fatal("expected error from malformed MCP JSON")
			}
			if len(cap.calls) != 0 {
				t.Errorf("no file must be written on error, got %d writes", len(cap.calls))
			}
		})
	}
}

func TestWriteMCPOpenCode_SkipsEntryWithoutCommandOrURL(t *testing.T) {
	t.Parallel()
	cap := &captureContainer{}
	req := AgentRunRequest{
		AgentSlug: "agent-x",
		MCPServers: []MCPServerConfig{
			{Name: "ghost"}, // neither command nor endpoint
			{Name: "ok", Command: "node", Args: []string{"s.js"}},
		},
	}
	if err := writeMCPOpenCode(context.Background(), cap, "ctr1", req, "/output/agent-x", covQuietLogger()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cap.calls) != 1 {
		t.Fatalf("want 1 write, got %d", len(cap.calls))
	}
	body := cap.calls[0].body
	if strings.Contains(body, `"ghost"`) {
		t.Errorf("entry without command/url must be skipped:\n%s", body)
	}
	if !strings.Contains(body, `"ok"`) {
		t.Errorf("valid entry missing:\n%s", body)
	}
}

func TestWriteMCPCodex_EnvAndHeaderRepresentations(t *testing.T) {
	t.Parallel()
	cap := &captureContainer{}
	req := AgentRunRequest{
		AgentSlug: "agent-x",
		CrewMCPConfigJSON: `{
			"mcpServers": {
				"stdio-srv": {
					"command": "node",
					"args": ["s.js"],
					"env": {"LITERAL": "plain-value", "REF": "${SECRET}"}
				},
				"http-srv": {
					"type": "http",
					"url": "https://h.example/mcp",
					"headers": {
						"X-API-Key": "${APIKEY}",
						"X-Static": "fixed",
						"X-Mixed": "prefix ${A} ${B}"
					}
				}
			}
		}`,
	}
	if err := writeMCPCodex(context.Background(), cap, "ctr1", req, "/output/agent-x", covQuietLogger()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cap.calls) != 1 {
		t.Fatalf("want 1 write, got %d", len(cap.calls))
	}
	w := cap.calls[0]
	if w.workingDir != "/crew/agents/agent-x" {
		t.Errorf("codex config must land in HOME, got workdir %q", w.workingDir)
	}
	body := w.body
	if !strings.Contains(body, `env = { LITERAL = "plain-value" }`) {
		t.Errorf("literal env must be written, ${VAR} refs omitted:\n%s", body)
	}
	if strings.Contains(body, "${SECRET}") {
		t.Errorf("env ref must not be written literally:\n%s", body)
	}
	if !strings.Contains(body, `env_http_headers = { "X-API-Key" = "APIKEY" }`) {
		t.Errorf("whole-value env header must use env_http_headers:\n%s", body)
	}
	if !strings.Contains(body, `http_headers = { "X-Static" = "fixed" }`) {
		t.Errorf("literal header must use http_headers:\n%s", body)
	}
	if strings.Contains(body, "X-Mixed") {
		t.Errorf("mixed literal+ref header is unrepresentable and must be dropped:\n%s", body)
	}
}

func TestTomlString_Escapes(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{`plain`, `"plain"`},
		{`has "quote"`, `"has \"quote\""`},
		{`back\slash`, `"back\\slash"`},
		{"line\nbreak", `"line\nbreak"`},
		{"cr\rreturn", `"cr\rreturn"`},
		{"tab\there", `"tab\there"`},
	}
	for _, tc := range tests {
		if got := tomlString(tc.in); got != tc.want {
			t.Errorf("tomlString(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}
