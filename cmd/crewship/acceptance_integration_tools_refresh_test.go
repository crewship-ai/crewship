package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Acceptance for `crewship integration tools refresh` (#1884), driven through
// the BUILT BINARY rather than by calling RunE in-process.
//
// The in-process tests in cmd_integration_tools_cov_test.go assert the same
// payload, but they set the cobra flags by hand. Only the binary proves the
// whole chain an agent actually scripts: argv → flag parsing → JSON body on
// the wire → exit code. That matters here because the bug being fixed was
// precisely a command that *succeeded* while sending nothing.

// refreshStub records the refresh POST body and answers with a canned status.
type refreshStub struct {
	mu sync.Mutex
	// posted is the decoded refresh body; nil until the endpoint is hit,
	// which is itself an assertion for the "refused locally" cases.
	posted map[string]any
	// status/detail let a case make the endpoint reject the call.
	status int
	detail string
}

func (s *refreshStub) start(t *testing.T) *httptest.Server {
	t.Helper()
	const crewID = "ccrewacceptance000001"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/crews":
			_, _ = w.Write([]byte(`[{"id":"` + crewID + `","slug":"backend","name":"Backend"}]`))

		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/crews/"+crewID+"/integrations/intg1/tools/refresh":
			raw, _ := io.ReadAll(r.Body)
			s.mu.Lock()
			s.posted = map[string]any{}
			_ = json.Unmarshal(raw, &s.posted)
			status, detail := s.status, s.detail
			s.mu.Unlock()
			if status != 0 {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":"` + detail + `"}`))
				return
			}
			_, _ = w.Write([]byte(`{"created":2,"updated":0}`))

		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no stub for ` + r.Method + " " + r.URL.Path + `"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// postedTools returns the `tools` array the binary sent, failing when the
// endpoint was never reached.
func (s *refreshStub) postedTools(t *testing.T) []map[string]any {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.posted == nil {
		t.Fatal("the refresh endpoint was never called")
	}
	raw, ok := s.posted["tools"].([]any)
	if !ok {
		t.Fatalf(`body has no "tools" array: %#v`, s.posted)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		m, _ := entry.(map[string]any)
		out = append(out, m)
	}
	return out
}

func (s *refreshStub) called() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.posted != nil
}

// refreshStubConfig writes a CLI config pointing at the stub. Both a token
// and a workspace are needed: the command runs requireAuth then
// requireWorkspace before it looks at a flag.
func refreshStubConfig(t *testing.T, serverURL string) string {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	cfg := "server: " + serverURL + "\nworkspace: ws_test\ntoken: fake-token\nformat: table\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath
}

// runRefreshCLI runs the built binary against the stub, optionally feeding
// stdin (for `--tools-file -`), and returns combined output plus the error.
func runRefreshCLI(t *testing.T, cfgPath, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(buildCrewshipBinary(t), args...)
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath,
		"NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// The headline contract: a discovered tool named on --tool reaches the
// endpoint, description and all.
func TestAcceptance_IntegrationToolsRefresh_SendsDiscoveredTools(t *testing.T) {
	stub := &refreshStub{}
	srv := stub.start(t)
	cfg := refreshStubConfig(t, srv.URL)

	out, err := runRefreshCLI(t, cfg, "", "integration", "tools", "refresh", "backend", "intg1",
		"--tool", "search=Full-text search over the issue tracker",
		"--tool", "create_issue")
	if err != nil {
		t.Fatalf("refresh: %v\noutput: %s", err, out)
	}

	tools := stub.postedTools(t)
	if len(tools) != 2 {
		t.Fatalf("posted %d tools, want 2: %#v", len(tools), tools)
	}
	if tools[0]["name"] != "search" {
		t.Errorf("tools[0].name = %v, want search", tools[0]["name"])
	}
	if tools[0]["description"] != "Full-text search over the issue tracker" {
		t.Errorf("tools[0].description = %v, want the text after '='", tools[0]["description"])
	}
	if tools[1]["name"] != "create_issue" {
		t.Errorf("tools[1].name = %v, want create_issue", tools[1]["name"])
	}
	if _, present := tools[1]["description"]; present {
		t.Errorf("a bare --tool must omit description: %#v", tools[1])
	}
	// The server's JSON result is what the operator sees.
	if !strings.Contains(out, `"created"`) {
		t.Errorf("server result not rendered:\n%s", out)
	}
}

// `--tools-file -` reads the discovery payload from stdin, which is how the
// MCP probe hands its `tools/list` result over without a temp file.
func TestAcceptance_IntegrationToolsRefresh_ToolsFileFromStdin(t *testing.T) {
	stub := &refreshStub{}
	srv := stub.start(t)
	cfg := refreshStubConfig(t, srv.URL)

	payload := `{"tools":[{"name":"search","description":"find things","inputSchema":{"type":"object"}}]}`
	out, err := runRefreshCLI(t, cfg, payload, "integration", "tools", "refresh", "backend", "intg1",
		"--tools-file", "-")
	if err != nil {
		t.Fatalf("refresh: %v\noutput: %s", err, out)
	}

	tools := stub.postedTools(t)
	if len(tools) != 1 || tools[0]["name"] != "search" || tools[0]["description"] != "find things" {
		t.Fatalf("posted tools = %#v, want the single unwrapped entry", tools)
	}
	if _, extra := tools[0]["inputSchema"]; extra {
		t.Errorf("inputSchema must not be forwarded: %#v", tools[0])
	}
}

// The error path the acceptance criteria asks for: a 4xx is reported, not
// swallowed, and it carries the validation exit code.
func TestAcceptance_IntegrationToolsRefresh_ReportsServerRejection(t *testing.T) {
	stub := &refreshStub{status: http.StatusBadRequest, detail: "Invalid JSON body"}
	srv := stub.start(t)
	cfg := refreshStubConfig(t, srv.URL)

	out, err := runRefreshCLI(t, cfg, "", "integration", "tools", "refresh", "backend", "intg1",
		"--tool", "search=Full-text search")
	if err == nil {
		t.Fatalf("expected a non-zero exit for a 400; output: %s", out)
	}
	if code := exitCodeOf(t, err); code != 2 {
		t.Errorf("exit code = %d, want 2 (validation)", code)
	}
	if !strings.Contains(out, "Invalid JSON body") {
		t.Errorf("server detail not reported:\n%s", out)
	}
	if !stub.called() {
		t.Error("the rejected request never reached the endpoint")
	}
}

// Running with no tools at all is refused locally: the old behaviour posted
// an empty list, which the server no-ops, and printed a success line — a
// refresh that refreshed nothing (#1884).
func TestAcceptance_IntegrationToolsRefresh_RefusesWithNoTools(t *testing.T) {
	stub := &refreshStub{}
	srv := stub.start(t)
	cfg := refreshStubConfig(t, srv.URL)

	out, err := runRefreshCLI(t, cfg, "", "integration", "tools", "refresh", "backend", "intg1")
	if err == nil {
		t.Fatalf("expected a non-zero exit with no tools supplied; output: %s", out)
	}
	if code := exitCodeOf(t, err); code != 2 {
		t.Errorf("exit code = %d, want 2 (validation)", code)
	}
	for _, want := range []string{"--tool", "--tools-file"} {
		if !strings.Contains(out, want) {
			t.Errorf("error should name %s:\n%s", want, out)
		}
	}
	if stub.called() {
		t.Error("nothing should have been posted")
	}
}
