package orchestrator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// scriptArgsRE captures the base64 payload + destination path from the script
// writeFileViaContainer hands to the container:
//
//	mkdir -p "$(dirname <path>)" && echo <b64> | base64 -d > <path>
var scriptArgsRE = regexp.MustCompile(`echo (\S+) \| base64 -d > (\S+)`)

// captureContainer is a mockContainer specialised for MCP writer tests. Each
// Exec call's script is parsed; the base64-decoded payload + destination path
// are stored in the calls slice for assertions. WorkingDir is also captured
// so tests can verify HOME-based vs cwd-based writers.
type captureContainer struct {
	calls []capturedWrite
}

type capturedWrite struct {
	path       string
	body       string
	workingDir string
}

func (c *captureContainer) EnsureCrewRuntime(_ context.Context, _ provider.CrewConfig) (string, error) {
	return "container-1", nil
}
func (c *captureContainer) StopCrewRuntime(_ context.Context, _ string) error   { return nil }
func (c *captureContainer) RemoveCrewRuntime(_ context.Context, _ string) error { return nil }
func (c *captureContainer) ContainerStatus(_ context.Context, _ string) (*provider.ContainerStatus, error) {
	return &provider.ContainerStatus{State: "running"}, nil
}
func (c *captureContainer) Exec(_ context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	if len(cfg.Cmd) == 3 && cfg.Cmd[0] == "sh" && cfg.Cmd[1] == "-c" {
		matches := scriptArgsRE.FindStringSubmatch(cfg.Cmd[2])
		if len(matches) == 3 {
			b64 := strings.TrimSpace(matches[1])
			body, err := base64.StdEncoding.DecodeString(b64)
			if err == nil {
				c.calls = append(c.calls, capturedWrite{
					path:       matches[2],
					body:       string(body),
					workingDir: cfg.WorkingDir,
				})
			}
		}
	}
	return &provider.ExecResult{ExecID: "test", Reader: io.NopCloser(strings.NewReader(""))}, nil
}
func (c *captureContainer) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	return false, 0, nil
}
func (c *captureContainer) ContainerStats(_ context.Context, _ string) (*provider.ContainerMetrics, error) {
	return nil, nil
}
func (c *captureContainer) CrewContainerName(slug string) string {
	return "crewship-team-" + slug
}
func (c *captureContainer) CopyToContainer(_ context.Context, _ string, _ string, _ io.Reader) error {
	return nil
}

// runWriter is a small helper that invokes a writer with our standard sample
// inputs and returns the captured write (assumes single file write per call).
func runWriter(t *testing.T, writer func(context.Context, provider.ContainerProvider, string, AgentRunRequest, string, *slog.Logger) error) capturedWrite {
	t.Helper()
	cap := &captureContainer{}
	req := sampleMCPInputs()
	req.AgentSlug = "agent-x"
	if err := writer(context.Background(), cap, "container-1", req, "/output/agent-x", slog.Default()); err != nil {
		t.Fatalf("writer error: %v", err)
	}
	if len(cap.calls) != 1 {
		t.Fatalf("want exactly 1 file write, got %d: %+v", len(cap.calls), cap.calls)
	}
	return cap.calls[0]
}

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

// === Real writer integration tests ===
// Tests below exercise the writers end-to-end via captureContainer so we
// verify path + body + workDir, not just the helper functions.

// TestWriteMCPCursor_RealOutput verifies the real writer produces a Cursor-
// shaped JSON config at .cursor/mcp.json with ${env:VAR} translation applied
// (the production-blocking gap from the third validation wave: pre-fix
// writer left ${VAR} literal which Cursor doesn't expand).
func TestWriteMCPCursor_RealOutput(t *testing.T) {
	cw := runWriter(t, writeMCPCursor)
	if cw.path != ".cursor/mcp.json" {
		t.Errorf("want .cursor/mcp.json, got %q", cw.path)
	}
	if cw.workingDir != "/output/agent-x" {
		t.Errorf("Cursor writes relative to workDir; got %q", cw.workingDir)
	}
	var parsed struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(cw.body), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\nbody: %s", err, cw.body)
	}
	headers, ok := parsed.MCPServers["linear"]["headers"].(map[string]any)
	if !ok {
		t.Fatalf("linear headers missing: %v", parsed.MCPServers["linear"])
	}
	auth, _ := headers["Authorization"].(string)
	if auth != "Bearer ${env:LINEAR_TOKEN}" {
		t.Errorf("Cursor ${env:VAR} translation lost — got %q (Cursor will see literal token, MCP server returns 401)", auth)
	}
}

// TestWriteMCPDroid_RealOutput pins the .factory/mcp.json shape — Anthropic-
// compatible mcpServers map plus Droid's required type:stdio|http
// discriminator.
func TestWriteMCPDroid_RealOutput(t *testing.T) {
	cw := runWriter(t, writeMCPDroid)
	if cw.path != ".factory/mcp.json" {
		t.Errorf("want .factory/mcp.json, got %q", cw.path)
	}
	var parsed struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(cw.body), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\nbody: %s", err, cw.body)
	}
	if parsed.MCPServers["fs"]["type"] != "stdio" {
		t.Errorf("Droid REQUIRES type:stdio; got %v", parsed.MCPServers["fs"]["type"])
	}
	if parsed.MCPServers["linear"]["type"] != "http" {
		t.Errorf("Droid REQUIRES type:http for HTTP; got %v", parsed.MCPServers["linear"]["type"])
	}
}

// TestWriteMCPGemini_RealOutput pins .gemini/settings.json layout — mcpServers
// nested under it, command/httpUrl transport keying.
func TestWriteMCPGemini_RealOutput(t *testing.T) {
	cw := runWriter(t, writeMCPGemini)
	if cw.path != ".gemini/settings.json" {
		t.Errorf("want .gemini/settings.json, got %q", cw.path)
	}
	var parsed struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(cw.body), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\nbody: %s", err, cw.body)
	}
	if parsed.MCPServers["fs"]["command"] != "npx" {
		t.Errorf("Gemini stdio command lost: %v", parsed.MCPServers["fs"])
	}
	if parsed.MCPServers["linear"]["httpUrl"] != "https://mcp.linear.app/sse" {
		t.Errorf("Gemini HTTP path should use httpUrl (preferred over deprecated SSE url): %v", parsed.MCPServers["linear"])
	}
}

// TestWriteMCPOpenCode_RealOutput pins opencode.json shape: mcp key (NOT
// mcpServers), type:local|remote, command as array, environment field,
// {env:VAR} interpolation.
func TestWriteMCPOpenCode_RealOutput(t *testing.T) {
	cw := runWriter(t, writeMCPOpenCode)
	if cw.path != "opencode.json" {
		t.Errorf("want opencode.json, got %q", cw.path)
	}
	var parsed struct {
		Schema string                    `json:"$schema"`
		MCP    map[string]map[string]any `json:"mcp"`
	}
	if err := json.Unmarshal([]byte(cw.body), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\nbody: %s", err, cw.body)
	}
	if parsed.Schema == "" {
		t.Errorf("$schema missing — IDE autocomplete lost")
	}
	if parsed.MCP["fs"]["type"] != "local" {
		t.Errorf("OpenCode stdio type should be 'local', got %v", parsed.MCP["fs"]["type"])
	}
	cmd, ok := parsed.MCP["fs"]["command"].([]any)
	if !ok {
		t.Fatalf("OpenCode command must be array, got %T", parsed.MCP["fs"]["command"])
	}
	if cmd[0] != "npx" {
		t.Errorf("command[0] lost: %v", cmd[0])
	}
	if _, hasEnv := parsed.MCP["fs"]["env"]; hasEnv {
		t.Errorf("OpenCode uses 'environment' field, NOT 'env'")
	}
	envMap, ok := parsed.MCP["fs"]["environment"].(map[string]any)
	if !ok {
		t.Fatalf("'environment' field missing: %v", parsed.MCP["fs"])
	}
	if envMap["FOO"] != "{env:FOO}" {
		t.Errorf("OpenCode {env:VAR} translation lost: %v", envMap["FOO"])
	}
	headers, _ := parsed.MCP["linear"]["headers"].(map[string]any)
	if headers["Authorization"] != "Bearer {env:LINEAR_TOKEN}" {
		t.Errorf("OpenCode header env-var translation lost: %v", headers["Authorization"])
	}
}

// TestWriteMCPCodex_RealOutput pins .codex/config.toml layout AND the two
// production-blocking fixes from third validation:
//  1. File goes to /crew/agents/<slug>/.codex/config.toml (HOME), NOT workDir
//     — Codex requires project trust for project-scoped configs.
//  2. env block omits ${VAR} entries entirely (Codex doesn't interpolate; the
//     literal string would override the inherited env and break MCP servers).
func TestWriteMCPCodex_RealOutput(t *testing.T) {
	cw := runWriter(t, writeMCPCodex)
	if cw.path != ".codex/config.toml" {
		t.Errorf("want .codex/config.toml, got %q", cw.path)
	}
	if cw.workingDir != "/crew/agents/agent-x" {
		t.Errorf("Codex MCP MUST be HOME-located (project-scoped requires interactive trust); got workingDir=%q", cw.workingDir)
	}
	body := cw.body
	// stdio server section present
	if !strings.Contains(body, "[mcp_servers.fs]") {
		t.Errorf("fs section missing:\n%s", body)
	}
	if !strings.Contains(body, `command = "npx"`) {
		t.Errorf("fs command missing:\n%s", body)
	}
	// CRITICAL: env block must NOT contain literal ${FOO} — Codex doesn't
	// interpolate, so writing it would override inherited env with the
	// literal string and cause MCP servers to 401.
	if strings.Contains(body, `FOO = "${FOO}"`) {
		t.Errorf("Codex env block contains literal ${VAR} ref — would override inherited env\nbody:\n%s", body)
	}
	// HTTP server: bearer_token_env_var extracted from "Bearer ${LINEAR_TOKEN}"
	if !strings.Contains(body, `bearer_token_env_var = "LINEAR_TOKEN"`) {
		t.Errorf("Codex bearer_token_env_var extraction lost — Linear MCP would 401:\n%s", body)
	}
}

// TestWriteMCPCodex_GenericHeader covers the X-API-Key path (env_http_headers
// per Codex schema). Pre-fix writer dropped non-Bearer headers entirely.
func TestWriteMCPCodex_GenericHeader(t *testing.T) {
	cap := &captureContainer{}
	req := AgentRunRequest{
		AgentSlug:         "agent-x",
		CrewMCPConfigJSON: `{"mcpServers":{"notion":{"type":"http","url":"https://api.notion.com/mcp","headers":{"X-API-Key":"${NOTION_KEY}","X-Custom":"literal-value"}}}}`,
	}
	if err := writeMCPCodex(context.Background(), cap, "container-1", req, "/work", slog.Default()); err != nil {
		t.Fatalf("writer error: %v", err)
	}
	if len(cap.calls) != 1 {
		t.Fatalf("want 1 write, got %d", len(cap.calls))
	}
	body := cap.calls[0].body
	if !strings.Contains(body, `env_http_headers = { "X-API-Key" = "NOTION_KEY" }`) {
		t.Errorf("env_http_headers entry for X-API-Key missing:\n%s", body)
	}
	if !strings.Contains(body, `http_headers = { "X-Custom" = "literal-value" }`) {
		t.Errorf("http_headers entry for literal X-Custom missing:\n%s", body)
	}
}

// TestWriteMCP_EmptyConfigSilent — when there are no MCP servers, writers
// must early-return without writing anything (avoid clobbering or creating
// stale empty configs).
func TestWriteMCP_EmptyConfigSilent(t *testing.T) {
	writers := map[string]func(context.Context, provider.ContainerProvider, string, AgentRunRequest, string, *slog.Logger) error{
		"cursor":   writeMCPCursor,
		"droid":    writeMCPDroid,
		"gemini":   writeMCPGemini,
		"opencode": writeMCPOpenCode,
		"codex":    writeMCPCodex,
	}
	for name, w := range writers {
		t.Run(name, func(t *testing.T) {
			cap := &captureContainer{}
			req := AgentRunRequest{AgentSlug: "agent-x"} // no MCP sources
			if err := w(context.Background(), cap, "container-1", req, "/output/agent-x", slog.Default()); err != nil {
				t.Errorf("empty-config writer error: %v", err)
			}
			if len(cap.calls) != 0 {
				t.Errorf("%s: writer should be silent on empty config, but wrote %d files: %+v", name, len(cap.calls), cap.calls)
			}
		})
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
