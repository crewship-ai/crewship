package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/cli"
)

func executeAPITest(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newAPICommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestAPICatalogCoversEmbeddedContract(t *testing.T) {
	doc, err := loadAPIDocument()
	if err != nil {
		t.Fatal(err)
	}
	ops, err := doc.operations("", "")
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(api.OpenAPISpecJSON(), &raw); err != nil {
		t.Fatal(err)
	}
	expected := 0
	for _, item := range raw.Paths {
		for method := range item {
			switch method {
			case "get", "head", "options", "post", "put", "patch", "delete":
				expected++
			}
		}
	}
	if len(ops) != expected || len(ops) < 700 {
		t.Fatalf("operations=%d, contract=%d", len(ops), expected)
	}
	seen := map[string]bool{}
	for _, op := range ops {
		if op.ID == "" || seen[op.ID] {
			t.Fatalf("empty or duplicate operation ID: %+v", op)
		}
		seen[op.ID] = true
		value, err := doc.selectOperation(op)
		if err != nil {
			t.Fatalf("%s: %v", op.ID, err)
		}
		if len(value["paths"].(map[string]any)) != 1 {
			t.Fatalf("selection included unrelated paths: %s", op.ID)
		}
		if op.RequiresYes != (op.Method != "GET" && op.Method != "HEAD" && op.Method != "OPTIONS") {
			t.Fatalf("mutation policy: %+v", op)
		}
	}
	filtered, err := doc.operations("AgEnTs", "GET")
	if err != nil || len(filtered) == 0 {
		t.Fatalf("filter: %v, %d", err, len(filtered))
	}
	for _, op := range filtered {
		if op.Method != "GET" {
			t.Fatalf("filter leaked %s", op.Method)
		}
	}
}

func TestAPISchemaReferenceClosure(t *testing.T) {
	doc := &apiDocument{OpenAPI: "3.0.3", Paths: map[string]map[string]json.RawMessage{"/api/v1/example": {"get": json.RawMessage(`{"responses":{"200":{"$ref":"#/components/schemas/A"}}}`)}}, Components: map[string]map[string]json.RawMessage{"schemas": {"A": json.RawMessage(`{"properties":{"b":{"$ref":"#/components/schemas/B"}}}`), "B": json.RawMessage(`{"$ref":"#/components/schemas/A"}`), "Unrelated": json.RawMessage(`{}`)}}}
	value, err := doc.selectOperation(apiOperation{Method: "GET", Path: "/api/v1/example"})
	if err != nil {
		t.Fatal(err)
	}
	components := value["components"].(map[string]map[string]json.RawMessage)
	if len(components["schemas"]) != 2 {
		t.Fatalf("closure = %v", components)
	}
	delete(doc.Components["schemas"], "B")
	if _, err := doc.selectOperation(apiOperation{Method: "GET", Path: "/api/v1/example"}); err == nil {
		t.Fatal("accepted missing schema")
	}
}

func TestAPIDiscoveryFormatsAndOffline(t *testing.T) {
	saveCLIState(t)
	cliCfg = nil
	flagServer = "http://127.0.0.1:1"
	for _, format := range []string{"json", "yaml", "ndjson"} {
		flagFormat = format
		for _, args := range [][]string{{"operations", "agents", "--method", "GET"}, {"schema", "GET", "/api/v1/agents"}} {
			out, err := executeAPITest(t, args...)
			if err != nil {
				t.Fatalf("%s %v: %v", format, args, err)
			}
			if format == "yaml" {
				var v any
				if err := yaml.Unmarshal([]byte(out), &v); err != nil {
					t.Fatal(err)
				}
				if args[0] == "schema" && !strings.Contains(out, "operationId:") {
					t.Fatalf("YAML schema not rendered: %.300s", out)
				}
			} else {
				var v any
				if err := json.NewDecoder(strings.NewReader(out)).Decode(&v); err != nil {
					t.Fatalf("invalid JSON: %v", err)
				}
			}
		}
	}
	_, err := executeAPITest(t, "schema", "nonexistent")
	if cli.ExitCodeFor(err) != cli.ExitNotFound {
		t.Fatalf("missing operation: %v", err)
	}
}

func TestAPIRequestPathValidation(t *testing.T) {
	for _, path := range []string{"https://example.com/api/v1/agents", "//example.com/api/v1/agents", "api/v1/agents", "/api/../secret", "/api/%2e%2e/secret", "/api/v1/agents#fragment", "/api\\secret", "/api/%00secret", "/api/v1/agents/{id}", "/api?bad=%zz"} {
		if _, err := apiRequestPath(path, nil); cli.ExitCodeFor(err) != cli.ExitValidation {
			t.Errorf("accepted %q: %v", path, err)
		}
	}
	got, err := apiRequestPath("/api/v1/agents?tag=a", []string{"tag=b c", "q=x&y=z"})
	if err != nil || got != "/api/v1/agents?q=x%26y%3Dz&tag=a&tag=b+c" {
		t.Fatalf("path = %q, %v", got, err)
	}
}

func TestAPIRequestSafetyBeforeNetwork(t *testing.T) {
	saveCLIState(t)
	cliCfg = nil
	flagFormat = "json"
	flagServer = "http://127.0.0.1:1"
	out, err := executeAPITest(t, "request", "POST", "/api/v1/agents?token=secret", "--input", "/does-not-exist", "--dry-run")
	if err != nil || !strings.Contains(out, `"dry_run": true`) || strings.Contains(out, "secret") || strings.Contains(out, "does-not-exist") {
		t.Fatalf("dry run = %s, %v", out, err)
	}
	for _, args := range [][]string{
		{"POST", "/api/v1/agents"},
		{"GET", "/api/v1/agents", "--input", "-"},
		{"GET", "/api/v1/agents", "--timeout", "0s"},
		{"GET", "/api/v1/agents", "--max-response-bytes", "0"},
		{"TRACE", "/api/v1/agents"},
	} {
		out, err := executeAPITest(t, append([]string{"request"}, args...)...)
		if cli.ExitCodeFor(err) != cli.ExitValidation || out != "" {
			t.Fatalf("%v: output=%q error=%v", args, out, err)
		}
	}
}

func setupAPIServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	saveCLIState(t)
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	flagServer = s.URL
	flagFormat = "json"
	flagWorkspace = "cworkspace123456789012345"
	flagProfile = ""
	cliCfg = &cli.CLIConfig{Server: s.URL, Token: "test-cli-token"}
	t.Setenv("CREWSHIP_TOKEN", "")
	return s
}

func TestAPIRequestWireAndLargeInteger(t *testing.T) {
	calls := 0
	setupAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/api/v1/example" || r.Header.Get("Authorization") != "Bearer test-cli-token" || r.Header.Get("Idempotency-Key") != "request-123" || r.URL.Query().Get("workspace_id") != "cworkspace123456789012345" || r.URL.Query().Get("q") != "x&y" {
			t.Errorf("unexpected request %s %s headers=%v", r.Method, r.URL, r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"value":9007199254740993}` {
			t.Errorf("body=%s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":9007199254740993}`)
	})
	cmd := newAPICommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetIn(strings.NewReader(`{"value":9007199254740993}`))
	cmd.SetArgs([]string{"request", "POST", "/api/v1/example", "--input", "-", "--yes", "--query", "q=x&y", "--idempotency-key", "request-123"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "9007199254740993") || calls != 1 {
		t.Fatalf("output=%s calls=%d", out.String(), calls)
	}
}

func TestAPIRequestRefusesRedirectAndPreservesOutput(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			redirected := 0
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected++ }))
			defer target.Close()
			setupAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", target.URL)
				w.WriteHeader(status)
			})
			filename := filepath.Join(t.TempDir(), "response")
			os.WriteFile(filename, []byte("original"), 0600)
			out, err := executeAPITest(t, "request", "GET", "/api/v1/example", "--output", filename)
			data, _ := os.ReadFile(filename)
			if err == nil || out != "" || redirected != 0 || string(data) != "original" {
				t.Fatalf("redirect=%d out=%q error=%v file=%q", redirected, out, err, data)
			}
		})
	}
}

func TestAPIRequestBodyAndResponseBoundaries(t *testing.T) {
	tests := []struct {
		name, body string
		status     int
		limit      string
		wantCode   int
		wantText   string
	}{
		{"forbidden", `{"error":"denied"}`, 403, "1024", cli.ExitAuth, ""},
		{"rate limited", `{"error":"slow down"}`, 429, "1024", cli.ExitRateLimited, ""},
		{"too large", `{"ok":true}`, 200, "4", cli.ExitGeneric, ""},
		{"trailing", `{"ok":true} {}`, 200, "1024", cli.ExitGeneric, ""},
		{"not JSON", "<html>oops</html>", 200, "1024", cli.ExitGeneric, ""},
		{"empty", "", 204, "1024", cli.ExitOK, ""},
		{"success", `{"ok":true}`, 200, "1024", cli.ExitOK, `"ok": true`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupAPIServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tt.status); io.WriteString(w, tt.body) })
			out, err := executeAPITest(t, "request", "GET", "/api/v1/example", "--max-response-bytes", tt.limit)
			if cli.ExitCodeFor(err) != tt.wantCode || !strings.Contains(out, tt.wantText) || (err != nil && out != "") {
				t.Fatalf("out=%q err=%v", out, err)
			}
		})
	}
}

func TestAPIRequestAtomicDownload(t *testing.T) {
	setupAPIServer(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "binary\x00data") })
	filename := filepath.Join(t.TempDir(), "response")
	out, err := executeAPITest(t, "request", "GET", "/api/v1/example", "--output", filename)
	if err != nil || out != "" {
		t.Fatalf("%q %v", out, err)
	}
	data, _ := os.ReadFile(filename)
	st, _ := os.Stat(filename)
	if string(data) != "binary\x00data" || st.Mode().Perm() != 0600 {
		t.Fatalf("data=%q mode=%v", data, st.Mode())
	}
	out, err = executeAPITest(t, "request", "GET", "/api/v1/example", "--output", filename, "--max-response-bytes", "2")
	data, _ = os.ReadFile(filename)
	if err == nil || out != "" || string(data) != "binary\x00data" {
		t.Fatalf("failed download replaced file: %q %v", data, err)
	}
}

func TestAPIRequestValidatesInputBeforeSending(t *testing.T) {
	calls := 0
	setupAPIServer(t, func(w http.ResponseWriter, r *http.Request) { calls++ })
	for _, body := range []string{"", `{"unfinished":`, "{} {}", strings.Repeat(" ", apiInputLimit+1)} {
		cmd := newAPICommand()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetIn(strings.NewReader(body))
		cmd.SetArgs([]string{"request", "POST", "/api/v1/example", "--input", "-", "--yes"})
		if err := cmd.Execute(); cli.ExitCodeFor(err) != cli.ExitValidation {
			t.Fatalf("input accepted: %v", err)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid bodies sent %d requests", calls)
	}
}

func TestAPIRequestContextAndHostGuard(t *testing.T) {
	calls := 0
	setupAPIServer(t, func(w http.ResponseWriter, r *http.Request) { calls++ })
	cliCfg.Server = "https://different.invalid"
	_, err := executeAPITest(t, "request", "GET", "/api/v1/example")
	if cli.ExitCodeFor(err) != cli.ExitAuth || calls != 0 {
		t.Fatalf("host guard: %v calls=%d", err, calls)
	}
	cliCfg.Server = flagServer
	cmd := newAPICommand()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"request", "GET", "/api/v1/example"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := cmd.ExecuteContext(ctx); cli.ExitCodeFor(err) != cli.ExitConnection || calls != 0 {
		t.Fatalf("cancel: %v calls=%d", err, calls)
	}
}

func TestCommandManifestIncludesExecutionContract(t *testing.T) {
	root := &cobra.Command{Use: "test"}
	root.PersistentFlags().String("profile", "", "Target")
	parent := &cobra.Command{Use: "parent"}
	parent.PersistentFlags().String("scope", "", "Scope")
	child := &cobra.Command{Use: "child", Example: "test parent child --name example", RunE: func(*cobra.Command, []string) error { return nil }}
	child.Flags().String("name", "", "Name")
	child.MarkFlagRequired("name")
	root.AddCommand(parent)
	parent.AddCommand(child)
	manifest := collectCommands(root, "")
	found := findCommandManifest(manifest, "parent child")
	if found == nil || !found.Runnable || found.Example == "" || len(found.Flags) != 1 || !found.Flags[0].Required {
		t.Fatalf("manifest=%+v", found)
	}
	inherited := map[string]bool{}
	for _, flag := range found.InheritedFlags {
		inherited[flag.Name] = true
	}
	if !inherited["scope"] || inherited["profile"] {
		t.Fatalf("inherited=%v", inherited)
	}
}

func TestAPIRequestHeadersAndInputLimit(t *testing.T) {
	for _, header := range []string{"Authorization=secret", "cookie=session=secret", "Host=other.invalid", "Proxy-Authorization=secret", "Content-Length=1", "Connection=close", "Transfer-Encoding=chunked", "X-Workspace-ID=other", "Content-Type=text/plain", "Idempotency-Key=another", "Bad Name=value", "If-Match=bad\r\nHeader: injected", "missing-separator"} {
		if _, err := apiRequestHeaders([]string{header}); cli.ExitCodeFor(err) != cli.ExitValidation {
			t.Errorf("accepted header %q: %v", header, err)
		}
	}
	calls := 0
	setupAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("If-Match") != `"revision=1"` || r.Header.Get("Content-Type") != "text/plain" {
			t.Errorf("headers=%v", r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != "hello" {
			t.Errorf("body=%q", body)
		}
		w.WriteHeader(204)
	})
	run := func(limit string) error {
		cmd := newAPICommand()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetIn(strings.NewReader("hello"))
		cmd.SetArgs([]string{"request", "PUT", "/api/v1/example", "--yes", "--input", "-", "--content-type", "text/plain", "--header", `If-Match="revision=1"`, "--max-input-bytes", limit})
		return cmd.Execute()
	}
	if err := run("4"); cli.ExitCodeFor(err) != cli.ExitValidation || calls != 0 {
		t.Fatalf("oversize sent: calls=%d error=%v", calls, err)
	}
	if err := run("5"); err != nil || calls != 1 {
		t.Fatalf("boundary failed: calls=%d error=%v", calls, err)
	}
}

func TestCommandsFocusedManifest(t *testing.T) {
	saveCLIState(t)
	cliCfg = nil
	flagFormat = "json"
	out, err := captureStdoutCovCli10(t, func() error { return commandsCmd.RunE(commandsCmd, []string{"routine", "run"}) })
	if err != nil {
		t.Fatal(err)
	}
	var manifest commandsManifest
	if err := json.Unmarshal([]byte(out), &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Commands) != 1 || manifest.Commands[0].Path != "routine run" || len(manifest.GlobalFlags) == 0 {
		t.Fatalf("unexpected manifest: %+v", manifest)
	}
	out, err = captureStdoutCovCli10(t, func() error { return commandsCmd.RunE(commandsCmd, []string{"does-not-exist"}) })
	if cli.ExitCodeFor(err) != cli.ExitNotFound || out != "" {
		t.Fatalf("missing command: %q %v", out, err)
	}
}

func TestAPICompletionAndValidation(t *testing.T) {
	methods, directive := completeAPIMethod(nil, nil, "p")
	if directive != cobra.ShellCompDirectiveNoFileComp || strings.Join(methods, ",") != "POST,PUT,PATCH" {
		t.Fatalf("methods=%v directive=%v", methods, directive)
	}
	paths, directive := completeAPISchema(nil, []string{"GET"}, "/api/v1/agents")
	if len(paths) == 0 || directive != cobra.ShellCompDirectiveNoFileComp {
		t.Fatalf("paths=%v directive=%v", paths, directive)
	}
	for _, path := range paths {
		if !strings.HasPrefix(path, "/api/v1/agents") {
			t.Fatalf("unrelated completion %s", path)
		}
	}
	saveCLIState(t)
	cliCfg = nil
	flagFormat = "json"
	for _, args := range [][]string{{"request"}, {"request", "GET"}, {"operations", "one", "two"}} {
		out, err := executeAPITest(t, args...)
		if out != "" || cli.ExitCodeFor(err) != cli.ExitValidation {
			t.Fatalf("%v: %q %v", args, out, err)
		}
	}
	flagFormat = "invalid"
	for _, args := range [][]string{{"operations"}, {"schema", "GET", "/api/v1/agents"}, {"request", "GET", "/api/v1/agents", "--dry-run"}} {
		out, err := executeAPITest(t, args...)
		if out != "" || cli.ExitCodeFor(err) != cli.ExitValidation {
			t.Fatalf("%v: %q %v", args, out, err)
		}
	}
}

func TestAPIRequestResponseMetadata(t *testing.T) {
	setupAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"revision-1"`)
		w.WriteHeader(204)
	})
	out, err := executeAPITest(t, "request", "HEAD", "/api/v1/example", "--include")
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Status  int         `json:"status"`
		Headers http.Header `json:"headers"`
		Body    any         `json:"body"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != 204 || response.Headers.Get("ETag") != `"revision-1"` || response.Body != nil {
		t.Fatalf("response=%s", out)
	}
	out, err = executeAPITest(t, "request", "GET", "/api/v1/example", "--include", "--output", "ignored")
	if cli.ExitCodeFor(err) != cli.ExitValidation || out != "" {
		t.Fatalf("conflicting flags: %q %v", out, err)
	}
}

func TestAPIRequestAnonymousOmitsConfiguredIdentity(t *testing.T) {
	calls := 0
	setupAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "" || r.URL.Query().Has("workspace_id") {
			t.Errorf("anonymous request carried configured identity: %s", r.URL)
		}
		io.WriteString(w, `{"ok":true}`)
	})
	// The token belongs to another origin. Anonymous mode must not carry it or
	// resolve the configured workspace against the public destination.
	cliCfg.Server = "https://different.invalid"
	out, err := executeAPITest(t, "request", "GET", "/api/health", "--anonymous")
	if err != nil || calls != 1 || !strings.Contains(out, `"ok": true`) {
		t.Fatalf("anonymous: calls=%d out=%q err=%v", calls, out, err)
	}
	cliCfg = nil
	out, err = executeAPITest(t, "request", "GET", "/api/health", "--anonymous")
	if err != nil || calls != 2 {
		t.Fatalf("no config: calls=%d out=%q err=%v", calls, out, err)
	}
	flagProfile = "missing-profile"
	out, err = executeAPITest(t, "request", "GET", "/api/health", "--anonymous")
	if cli.ExitCodeFor(err) != cli.ExitValidation || calls != 2 || out != "" {
		t.Fatalf("unknown profile fell back to another target: calls=%d out=%q err=%v", calls, out, err)
	}
}
