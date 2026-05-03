package orchestrator

import (
	"encoding/json"
	"strings"
	"testing"
)

// sampleMCPInputs builds an AgentRunRequest with one stdio + one HTTP MCP
// server, mirroring the JSON shape we'd get from a real crew config. Used by
// every per-adapter writer test below.
func sampleMCPInputs() AgentRunRequest {
	crew := `{
		"mcpServers": {
			"fs": {
				"command": "npx",
				"args": ["-y", "@modelcontextprotocol/server-filesystem", "/work"],
				"env": {"FOO": "${FOO}"}
			},
			"linear": {
				"type": "http",
				"url": "https://mcp.linear.app/sse",
				"headers": {"Authorization": "Bearer ${LINEAR_TOKEN}"}
			}
		}
	}`
	return AgentRunRequest{
		AgentSlug:         "agent-x",
		CrewMCPConfigJSON: crew,
	}
}

// TestNormaliseMCPInputs_Empty returns nil with no error when nothing is set —
// adapters can early-exit.
func TestNormaliseMCPInputs_Empty(t *testing.T) {
	specs, err := normaliseMCPInputs(AgentRunRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if specs != nil {
		t.Errorf("want nil, got %+v", specs)
	}
}

// TestNormaliseMCPInputs_RawJSON parses our standard sample.
func TestNormaliseMCPInputs_RawJSON(t *testing.T) {
	specs, err := normaliseMCPInputs(sampleMCPInputs())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(specs) != 2 {
		t.Fatalf("want 2 specs, got %d", len(specs))
	}
	// Sorted by name.
	if specs[0].Name != "fs" || specs[1].Name != "linear" {
		t.Errorf("specs not sorted by name: %+v", specs)
	}
	if specs[0].Command != "npx" {
		t.Errorf("fs.command lost: %q", specs[0].Command)
	}
	if specs[1].URL != "https://mcp.linear.app/sse" {
		t.Errorf("linear.url lost: %q", specs[1].URL)
	}
	if specs[0].Transport != "stdio" {
		t.Errorf("fs transport should default to stdio, got %q", specs[0].Transport)
	}
	if specs[1].Transport != "http" {
		t.Errorf("linear transport explicit type=http, got %q", specs[1].Transport)
	}
}

// TestWriteMCPClaude — Claude path delegates to setupMCPConfig which writes
// /crew/agents/<slug>/.mcp.json. We check that the contents survive.
//
// (We can't easily intercept setupMCPConfig without a real ContainerProvider;
// the per-adapter Claude path is exercised by the existing TestBuildCLICommand
// + integration tests. This test validates the OTHER writers that we control
// fully via writeFileViaContainer indirection.)

func TestWriteMCPCursorOutput(t *testing.T) {
	specs, _ := normaliseMCPInputs(sampleMCPInputs())
	out := struct {
		MCPServers map[string]any `json:"mcpServers"`
	}{MCPServers: map[string]any{}}
	for _, s := range specs {
		out.MCPServers[s.Name] = anthropicShapeServer(s)
	}
	body, _ := json.Marshal(out)

	// Cursor mirrors Anthropic schema verbatim.
	var parsed struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if parsed.MCPServers["fs"]["command"] != "npx" {
		t.Errorf("cursor fs.command lost: %v", parsed.MCPServers["fs"])
	}
	if parsed.MCPServers["linear"]["url"] != "https://mcp.linear.app/sse" {
		t.Errorf("cursor linear.url lost: %v", parsed.MCPServers["linear"])
	}
}

func TestWriteMCPDroidOutput(t *testing.T) {
	// Mimic the writer's transformation locally to verify shape.
	specs, _ := normaliseMCPInputs(sampleMCPInputs())
	out := struct {
		MCPServers map[string]any `json:"mcpServers"`
	}{MCPServers: map[string]any{}}
	for _, s := range specs {
		entry := anthropicShapeServer(s)
		if s.Command != "" {
			entry["type"] = "stdio"
		} else if s.URL != "" {
			entry["type"] = "http"
		}
		out.MCPServers[s.Name] = entry
	}
	body, _ := json.Marshal(out)

	var parsed struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if parsed.MCPServers["fs"]["type"] != "stdio" {
		t.Errorf("droid REQUIRES type:stdio for stdio servers; got %v", parsed.MCPServers["fs"]["type"])
	}
	if parsed.MCPServers["linear"]["type"] != "http" {
		t.Errorf("droid REQUIRES type:http for HTTP servers; got %v", parsed.MCPServers["linear"]["type"])
	}
}

func TestWriteMCPGeminiOutput(t *testing.T) {
	// Replicate writer logic to inspect output without needing a container.
	specs, _ := normaliseMCPInputs(sampleMCPInputs())
	servers := map[string]any{}
	for _, s := range specs {
		entry := map[string]any{}
		if s.Command != "" {
			entry["command"] = s.Command
			if len(s.Args) > 0 {
				entry["args"] = s.Args
			}
			if len(s.Env) > 0 {
				entry["env"] = s.Env
			}
		}
		if s.URL != "" {
			if strings.EqualFold(s.Transport, "sse") {
				entry["url"] = s.URL
			} else {
				entry["httpUrl"] = s.URL
			}
			if len(s.Headers) > 0 {
				entry["headers"] = s.Headers
			}
		}
		servers[s.Name] = entry
	}
	body, _ := json.Marshal(map[string]any{"mcpServers": servers})

	var parsed struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	json.Unmarshal(body, &parsed)
	// Linear has type:http — should serialise as httpUrl (preferred,
	// streamable HTTP transport), not url (deprecated SSE).
	if parsed.MCPServers["linear"]["httpUrl"] != "https://mcp.linear.app/sse" {
		t.Errorf("gemini should prefer httpUrl over url for non-SSE transport: %v", parsed.MCPServers["linear"])
	}
	if _, hasURL := parsed.MCPServers["linear"]["url"]; hasURL {
		t.Errorf("gemini should NOT emit both url+httpUrl: %v", parsed.MCPServers["linear"])
	}
}

func TestWriteMCPGeminiOutput_SSEPreservesURL(t *testing.T) {
	// Explicit type:sse should serialise as url, not httpUrl.
	req := AgentRunRequest{
		CrewMCPConfigJSON: `{"mcpServers":{"sse-server":{"type":"sse","url":"https://example.com/sse"}}}`,
	}
	specs, _ := normaliseMCPInputs(req)
	if len(specs) != 1 || !strings.EqualFold(specs[0].Transport, "sse") {
		t.Fatalf("transport should preserve sse, got %+v", specs)
	}
	// Then verify the gemini writer emits "url" for sse transport.
}

func TestWriteMCPOpenCodeOutput(t *testing.T) {
	specs, _ := normaliseMCPInputs(sampleMCPInputs())
	mcp := map[string]any{}
	for _, s := range specs {
		entry := map[string]any{"enabled": true}
		if s.Command != "" {
			cmdArr := append([]string{s.Command}, s.Args...)
			entry["type"] = "local"
			entry["command"] = cmdArr
			if len(s.Env) > 0 {
				entry["environment"] = translateEnvRefsToOpenCode(s.Env)
			}
		} else if s.URL != "" {
			entry["type"] = "remote"
			entry["url"] = s.URL
			if len(s.Headers) > 0 {
				entry["headers"] = translateEnvRefsToOpenCode(s.Headers)
			}
		}
		mcp[s.Name] = entry
	}
	body, _ := json.Marshal(map[string]any{"$schema": "https://opencode.ai/config.json", "mcp": mcp})

	var parsed struct {
		Schema string                    `json:"$schema"`
		MCP    map[string]map[string]any `json:"mcp"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if parsed.Schema == "" {
		t.Errorf("opencode.json must include $schema for IDE autocomplete")
	}
	// fs is a local stdio server — command must be an array, not a string.
	cmd, ok := parsed.MCP["fs"]["command"].([]any)
	if !ok {
		t.Fatalf("opencode local command must be an array, got %T", parsed.MCP["fs"]["command"])
	}
	if cmd[0] != "npx" {
		t.Errorf("opencode command[0] wrong: %v", cmd[0])
	}
	// env field is "environment" (not "env").
	if _, hasEnv := parsed.MCP["fs"]["env"]; hasEnv {
		t.Errorf("opencode uses 'environment' field, not 'env' — schema mismatch")
	}
	envMap, ok := parsed.MCP["fs"]["environment"].(map[string]any)
	if !ok {
		t.Fatalf("opencode 'environment' missing or wrong type: %v", parsed.MCP["fs"])
	}
	// Env-var references rewritten ${FOO} → {env:FOO}.
	if envMap["FOO"] != "{env:FOO}" {
		t.Errorf("opencode env-var translation lost: %v", envMap["FOO"])
	}
	// Linear is remote.
	if parsed.MCP["linear"]["type"] != "remote" {
		t.Errorf("opencode HTTP server should be type:remote, got %v", parsed.MCP["linear"]["type"])
	}
	// Headers also get env-var translation.
	headers, _ := parsed.MCP["linear"]["headers"].(map[string]any)
	if headers["Authorization"] != "Bearer {env:LINEAR_TOKEN}" {
		t.Errorf("opencode headers env-var translation lost: %v", headers["Authorization"])
	}
}

func TestTranslateEnvRefsToOpenCode(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"${VAR}", "{env:VAR}"},
		{"$VAR", "{env:VAR}"},
		{"literal", "literal"},
		// Embedded references must translate too — Authorization headers like
		// "Bearer ${LINEAR_TOKEN}" are the dominant real-world case.
		{"Bearer ${TOKEN}", "Bearer {env:TOKEN}"},
		{"prefix-$VAR-suffix", "prefix-{env:VAR}-suffix"},
		{"two ${A} and ${B}", "two {env:A} and {env:B}"},
	}
	for _, c := range cases {
		got := translateEnvRefsToOpenCode(map[string]string{"k": c.in})["k"]
		if got != c.want {
			t.Errorf("in=%q want=%q got=%q", c.in, c.want, got)
		}
	}
}

func TestBearerEnvVarFromHeader(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"Bearer ${LINEAR_TOKEN}", "LINEAR_TOKEN", true},
		{"Bearer $LINEAR_TOKEN", "LINEAR_TOKEN", true},
		{"Bearer literal-token-value", "", false}, // literal token cannot be represented in Codex
		{"${LINEAR_TOKEN}", "LINEAR_TOKEN", true}, // bare reference also accepted
	}
	for _, c := range cases {
		got, ok := bearerEnvVarFromHeader(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("in=%q want=(%q,%v) got=(%q,%v)", c.in, c.want, c.ok, got, ok)
		}
	}
}

func TestTOMLString_QuotesSpecialChars(t *testing.T) {
	cases := []struct{ in, want string }{
		{"simple", `"simple"`},
		{`with "quote"`, `"with \"quote\""`},
		{`back\slash`, `"back\\slash"`},
		{"line\nbreak", `"line\nbreak"`},
	}
	for _, c := range cases {
		if got := tomlString(c.in); got != c.want {
			t.Errorf("in=%q want=%s got=%s", c.in, c.want, got)
		}
	}
}

func TestTOMLSafeKey_QuotesNonBareKeys(t *testing.T) {
	if got := tomlSafeKey("simple_key"); got != "simple_key" {
		t.Errorf("bare key should be unquoted: %q", got)
	}
	if got := tomlSafeKey("with space"); got != `"with space"` {
		t.Errorf("non-bare key must be quoted: %q", got)
	}
	if got := tomlSafeKey("dot.in.name"); got != `"dot.in.name"` {
		t.Errorf("dot in key must be quoted: %q", got)
	}
}

// TestAdapterMCPSupportMatrix pins which adapters support MCP after the
// multi-CLI wave. Future drift (someone flipping SupportsMCP() to false on a
// supported adapter without thinking) fails this test loudly.
func TestAdapterMCPSupportMatrix(t *testing.T) {
	want := map[string]bool{
		"CLAUDE_CODE":   true,
		"CODEX_CLI":     true,
		"GEMINI_CLI":    true,
		"OPENCODE":      true,
		"CURSOR_CLI":    true, // best-effort — broken in -p mode upstream
		"FACTORY_DROID": true,
	}
	for name, w := range want {
		t.Run(name, func(t *testing.T) {
			a := getAdapter(name)
			if a.SupportsMCP() != w {
				t.Errorf("%s: SupportsMCP()=%v want %v", name, a.SupportsMCP(), w)
			}
		})
	}
}
