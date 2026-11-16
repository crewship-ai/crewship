//go:build integration

package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/devcontainer"
)

// Explicit packaging/argv qualification against installed upstream releases.
// Supply an isolated directory populated with the selected public packages.
// No inherited provider credentials, host configuration or model calls are used.
func TestCurrentCLIReleaseCommands(t *testing.T) {
	dir := os.Getenv("CREWSHIP_CLI_QUALIFICATION_BIN_DIR")
	if dir == "" {
		t.Fatal("set CREWSHIP_CLI_QUALIFICATION_BIN_DIR to the isolated upstream binaries")
	}
	for _, tc := range []struct {
		adapter string
		help    []string
		flags   []string
	}{
		{"OPENCODE", []string{"run", "--help"}, []string{"--format", "--model", "--standalone"}},
		{"CLAUDE_CODE", []string{"--help"}, []string{"--print", "--output-format", "--include-partial-messages", "--setting-sources", "--strict-mcp-config", "--tools", "--no-session-persistence"}},
		{"CODEX_CLI", []string{"exec", "--help"}, []string{"--json", "--sandbox", "--skip-git-repo-check"}},
		{"GEMINI_CLI", []string{"--help"}, []string{"--prompt", "--output-format", "--model"}},
	} {
		t.Run(tc.adapter, func(t *testing.T) {
			cli, ok := devcontainer.AdapterCLIFor(tc.adapter)
			if !ok {
				t.Fatal("missing provisioner recipe")
			}
			home := t.TempDir()
			env := []string{"HOME=" + home, "PATH=" + dir + ":/usr/local/bin:/usr/bin:/bin", "XDG_CONFIG_HOME=" + home + "/config", "XDG_DATA_HOME=" + home + "/data", "XDG_CACHE_HOME=" + home + "/cache", "DISABLE_AUTOUPDATER=1", "OPENCODE_DISABLE_AUTOUPDATE=1", "OPENCODE_DISABLE_MODELS_FETCH=1", "CI=1"}
			run := func(args ...string) string {
				ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, filepath.Join(dir, cli.Binary), args...)
				cmd.Env, cmd.Dir = env, home
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("%s %v: %v\n%s", cli.Binary, args, err, out)
				}
				return string(out)
			}
			version := strings.TrimSpace(run("--version"))
			if got := devcontainer.ObservedToolVersion(cli.Binary, version); got != cli.Version {
				t.Fatalf("actual %q, expected %s", version, cli.Version)
			}
			help := run(tc.help...)
			for _, flag := range tc.flags {
				if !strings.Contains(help, flag) {
					t.Errorf("upstream removed %s", flag)
				}
			}
		})
	}
}

// Exercise the generated provider configuration and the real V2 wire format
// against a local synthetic endpoint, with no paid calls or inherited secrets.
func TestOpenCodeV2LocalEndpoint(t *testing.T) {
	if _, err := os.Stat("/etc/opencode"); err == nil {
		t.Fatal("run native qualification in an image without managed /etc/opencode configuration")
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	dir := os.Getenv("CREWSHIP_CLI_QUALIFICATION_BIN_DIR")
	if dir == "" {
		t.Fatal("set CREWSHIP_CLI_QUALIFICATION_BIN_DIR")
	}
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			requests.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Crewship fixture response\"},\"finish_reason\":null}]}\n\n")
			io.WriteString(w, "data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":3}}\n\ndata: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/api/tags") {
			io.WriteString(w, `{"models":[{"name":"crewship-fixture"}]}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/api/show") {
			io.WriteString(w, `{"capabilities":["completion","tools"]}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	req := AgentRunRequest{CLIAdapter: "OPENCODE", LLMProvider: "OLLAMA", LLMModel: "ollama/crewship-fixture", LocalModelBaseURL: server.URL + "/v1", UserMessage: "Reply with the fixture response"}
	generated, ok := localModelConfigEnv(req, false)
	if !ok {
		t.Fatal("missing config")
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(generated, "OPENCODE_CONFIG_CONTENT=")), &config); err != nil {
		t.Fatal(err)
	}
	config["agents"] = map[string]any{"title": map[string]any{"model": "ollama/crewship-fixture"}}
	config["policies"] = []map[string]string{{"action": "provider.use", "resource": "*", "effect": "deny"}, {"action": "provider.use", "resource": "ollama", "effect": "allow"}}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	command := (opencodeAdapter{}).BuildCommand(req)
	cmd := exec.CommandContext(ctx, filepath.Join(dir, "opencode"), command[1:]...)
	cmd.Dir = home
	cmd.Env = []string{"HOME=" + home, "PATH=" + dir + ":/usr/local/bin:/usr/bin:/bin", "XDG_CONFIG_HOME=" + home + "/config", "XDG_DATA_HOME=" + home + "/data", "XDG_CACHE_HOME=" + home + "/cache", "OPENCODE_DISABLE_AUTOUPDATE=1", "OPENCODE_DISABLE_MODELS_FETCH=1", "OPENCODE_CONFIG_CONTENT=" + string(raw), "OPENCODE_CONFIG_PROJECT_DISABLE=1", "OPENCODE_DISABLE_CLAUDE_CODE=1", "CI=1"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("native run: %v\n%s", err, out)
	}
	parse := (opencodeAdapter{}).NewStreamLineParser()
	var text strings.Builder
	var failed bool
	for _, line := range bytes.Split(out, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		parse(line, func(e AgentEvent) {
			if e.Type == "text" {
				text.WriteString(e.Content)
			}
			if e.Type == "error" {
				failed = true
			}
		})
	}
	if failed || requests.Load() == 0 || !strings.Contains(text.String(), "Crewship fixture response") {
		t.Fatalf("requests=%d failed=%t text=%s\n%s", requests.Load(), failed, text.String(), out)
	}
}

// --max-turns is hidden from Claude's normal help in 2.1.325. Verify the
// adapter's real argv reaches a local provider; removing the flag just to make
// a help-text assertion pass would remove Crewship's runaway-loop cap.
func TestCurrentClaudeCLIAdapterArguments(t *testing.T) {
	if _, err := os.Stat("/etc/claude-code"); err == nil {
		t.Fatal("run native qualification without managed /etc/claude-code configuration")
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	dir := os.Getenv("CREWSHIP_CLI_QUALIFICATION_BIN_DIR")
	if dir == "" {
		t.Fatal("set CREWSHIP_CLI_QUALIFICATION_BIN_DIR")
	}
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/v1/messages" {
			requests.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"Crewship local qualification endpoint"}}`)
	}))
	defer server.Close()
	req := AgentRunRequest{CLIAdapter: "CLAUDE_CODE", LLMModel: "claude-sonnet-4-6", ToolProfile: "MINIMAL", MaxTurns: 1, UserMessage: "Reply with one word"}
	argv := (claudeCodeAdapter{}).BuildCommand(req)
	home := t.TempDir()
	// Provisioning creates this file in the container's /crew/runs tree.
	// Translate that container path to the isolated host qualification fixture.
	mcpPath := filepath.Join(home, "mcp.json")
	if err := os.WriteFile(mcpPath, []byte(`{"mcpServers":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	for i, arg := range argv {
		if arg == "--mcp-config" && i+1 < len(argv) {
			argv[i+1] = mcpPath
		}
	}

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(dir, "claude"), argv[1:]...)
	cmd.Dir = home
	cmd.Env = []string{"HOME=" + home, "PATH=" + dir + ":/usr/local/bin:/usr/bin:/bin", "XDG_CONFIG_HOME=" + home + "/config", "XDG_DATA_HOME=" + home + "/data", "XDG_CACHE_HOME=" + home + "/cache", "DISABLE_AUTOUPDATER=1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "ANTHROPIC_API_KEY=crewship-local-fixture", "ANTHROPIC_BASE_URL=" + server.URL, "CI=1"}
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil || requests.Load() == 0 {
		t.Fatalf("argv did not reach local provider: %v requests=%d\n%s", err, requests.Load(), out)
	}
}
