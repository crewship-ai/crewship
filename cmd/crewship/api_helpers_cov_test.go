package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// covWSCli3 is a CUID-shaped workspace ID so client.GetWorkspaceID and the
// command-side looksLikeCUID both short-circuit without a resolve call.
const covWSCli3 = "cws0aaaaaaaaaaaaaaaaaaaa"

// covAgentIDCli3 is a CUID-shaped agent ID (>= 21 chars, c + lowercase
// alnum) so resolveAgentID passes it through without hitting /agents.
const covAgentIDCli3 = "cagent0000000000000000aa"

// covStub wires a StubServer into the package-global CLI state and
// guarantees full restoration at test end. NOT parallel-safe — none of
// the tests in the *_cov_test.go files use t.Parallel().
func covStub(t *testing.T) *clitest.StubServer {
	t.Helper()
	saveCLIState(t)
	stub := clitest.NewStubServer()
	t.Cleanup(stub.Close)
	// Neutralise env overrides that would re-route the resolved server
	// or workspace away from the stub.
	t.Setenv("CREWSHIP_SERVER", "")
	t.Setenv("CREWSHIP_WORKSPACE", "")
	flagServer = ""
	flagWorkspace = ""
	cliCfg = &cli.CLIConfig{Token: "test-token", Workspace: covWSCli3, Server: stub.URL()}
	return stub
}

// covResetFlags restores every changed flag on cmd to its default at
// test end. pflag has no public "unset"; Changed is an exported field,
// so reset it directly. Without this, a later test (possibly in another
// file) that relies on flags.Changed() being false would misbehave.
func covResetFlags(t *testing.T, cmd *cobra.Command) {
	t.Helper()
	t.Cleanup(func() {
		cmd.Flags().VisitAll(func(f *pflag.Flag) {
			if f.Changed {
				_ = f.Value.Set(f.DefValue)
				f.Changed = false
			}
		})
	})
}

// covCaptureStdoutCli3 runs fn with os.Stdout redirected to a pipe and
// returns everything written. The print* helpers in cmd/crewship write
// straight to os.Stdout via fmt.Printf, so this is the only honest way
// to assert their output.
func covCaptureStdoutCli3(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		defer r.Close()
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	defer func() {
		os.Stdout = old
	}()
	fn()
	_ = w.Close()
	os.Stdout = old
	return <-done
}

// covCaptureStderr is covCaptureStdoutCli3 for os.Stderr — cli.PrintSuccess
// and friends write there.
func covCaptureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		defer r.Close()
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	defer func() {
		os.Stderr = old
	}()
	fn()
	_ = w.Close()
	os.Stderr = old
	return <-done
}

// covWithStdin replaces os.Stdin with a pipe pre-filled with content.
func covWithStdin(t *testing.T, content string) {
	t.Helper()
	old := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if _, err := w.WriteString(content); err != nil {
		t.Fatalf("write stdin pipe: %v", err)
	}
	_ = w.Close()
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = old
		_ = r.Close()
	})
}

// fakeJQCov stubs the jqRunner seam in api_helpers.go.
type fakeJQCov struct {
	gotStdin []byte
	out      []byte
	err      error
}

func (f *fakeJQCov) SetStdin(b []byte)       { f.gotStdin = b }
func (f *fakeJQCov) Output() ([]byte, error) { return f.out, f.err }

// covSwapJQ replaces the lookPath / newJQCmd injection points and
// restores them at test end.
func covSwapJQ(t *testing.T, lp func(string) (string, error), nj func(string, string) jqRunner) {
	t.Helper()
	origLook, origNew := lookPath, newJQCmd
	if lp != nil {
		lookPath = lp
	}
	if nj != nil {
		newJQCmd = nj
	}
	t.Cleanup(func() {
		lookPath = origLook
		newJQCmd = origNew
	})
}

// ─── getJSON / postJSON / patchJSON / deleteJSON ────────────────────────

func TestGetJSON_DecodesPayload(t *testing.T) {
	stub := covStub(t)
	stub.OnGet("/api/v1/things", clitest.JSONResponse(200, map[string]string{"name": "alpha"}))

	var out struct {
		Name string `json:"name"`
	}
	if err := getJSON(newAPIClient(), "/api/v1/things", &out); err != nil {
		t.Fatalf("getJSON: %v", err)
	}
	if out.Name != "alpha" {
		t.Errorf("decoded name: got %q want %q", out.Name, "alpha")
	}
	if calls := stub.CallsFor("GET", "/api/v1/things"); len(calls) != 1 {
		t.Errorf("expected exactly 1 GET, got %d", len(calls))
	}
}

func TestGetJSON_NilOutDiscardsBody(t *testing.T) {
	stub := covStub(t)
	stub.OnGet("/api/v1/ping", clitest.JSONResponse(200, map[string]bool{"ok": true}))

	if err := getJSON(newAPIClient(), "/api/v1/ping", nil); err != nil {
		t.Fatalf("getJSON with nil out: %v", err)
	}
}

func TestGetJSON_SurfacesAPIError(t *testing.T) {
	stub := covStub(t)
	stub.OnGet("/api/v1/things", clitest.ErrorResponse(404, "thing not found"))

	err := getJSON(newAPIClient(), "/api/v1/things", nil)
	if err == nil || !strings.Contains(err.Error(), "thing not found") {
		t.Fatalf("expected 'thing not found' error, got %v", err)
	}
}

func TestGetJSON_TransportError(t *testing.T) {
	saveCLIState(t)
	t.Setenv("CREWSHIP_SERVER", "")
	flagServer = ""
	// Unroutable port on localhost — connection refused, no real traffic.
	cliCfg = &cli.CLIConfig{Token: "tk", Workspace: covWSCli3, Server: "http://127.0.0.1:1"}
	if err := getJSON(newAPIClient(), "/api/v1/x", nil); err == nil {
		t.Fatal("expected transport error, got nil")
	}
}

func TestPostJSON_NilOutAndErrorPaths(t *testing.T) {
	stub := covStub(t)
	stub.OnPost("/api/v1/things", clitest.JSONResponse(201, map[string]string{"id": "t1"}))

	if err := postJSON(newAPIClient(), "/api/v1/things", map[string]string{"k": "v"}, nil); err != nil {
		t.Fatalf("postJSON nil out: %v", err)
	}
	calls := stub.CallsFor("POST", "/api/v1/things")
	if len(calls) != 1 {
		t.Fatalf("expected 1 POST, got %d", len(calls))
	}
	var sent map[string]string
	clitest.MustDecodeJSONBody(calls[0].Body, &sent)
	if sent["k"] != "v" {
		t.Errorf("request body: got %v", sent)
	}

	stub.OnPost("/api/v1/things", clitest.ErrorResponse(409, "slug taken"))
	err := postJSON(newAPIClient(), "/api/v1/things", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "slug taken") {
		t.Fatalf("expected conflict error, got %v", err)
	}
}

func TestPatchJSON_AllBranches(t *testing.T) {
	stub := covStub(t)
	stub.OnPatch("/api/v1/things/t1", clitest.JSONResponse(200, map[string]string{"id": "t1", "name": "renamed"}))

	var out struct {
		Name string `json:"name"`
	}
	if err := patchJSON(newAPIClient(), "/api/v1/things/t1", map[string]string{"name": "renamed"}, &out); err != nil {
		t.Fatalf("patchJSON: %v", err)
	}
	if out.Name != "renamed" {
		t.Errorf("decoded name: got %q", out.Name)
	}

	// nil out branch
	if err := patchJSON(newAPIClient(), "/api/v1/things/t1", map[string]string{"name": "x"}, nil); err != nil {
		t.Fatalf("patchJSON nil out: %v", err)
	}

	// API error branch
	stub.OnPatch("/api/v1/things/t1", clitest.ErrorResponse(403, "forbidden"))
	if err := patchJSON(newAPIClient(), "/api/v1/things/t1", nil, nil); err == nil {
		t.Fatal("expected forbidden error")
	}

	// transport error branch
	cliCfg.Server = "http://127.0.0.1:1"
	if err := patchJSON(newAPIClient(), "/api/v1/things/t1", nil, nil); err == nil {
		t.Fatal("expected transport error")
	}
}

func TestDeleteJSON_AllBranches(t *testing.T) {
	stub := covStub(t)
	stub.OnDelete("/api/v1/things/t1", clitest.EmptyResponse(204))

	if err := deleteJSON(newAPIClient(), "/api/v1/things/t1"); err != nil {
		t.Fatalf("deleteJSON: %v", err)
	}
	if calls := stub.CallsFor("DELETE", "/api/v1/things/t1"); len(calls) != 1 {
		t.Errorf("expected 1 DELETE, got %d", len(calls))
	}

	stub.OnDelete("/api/v1/things/t1", clitest.ErrorResponse(404, "gone already"))
	if err := deleteJSON(newAPIClient(), "/api/v1/things/t1"); err == nil {
		t.Fatal("expected error on 404")
	}

	cliCfg.Server = "http://127.0.0.1:1"
	if err := deleteJSON(newAPIClient(), "/api/v1/things/t1"); err == nil {
		t.Fatal("expected transport error")
	}
}

// ─── queryString ────────────────────────────────────────────────────────

func TestQueryString(t *testing.T) {
	cases := []struct {
		name  string
		pairs []string
		want  string
	}{
		{"no pairs", nil, ""},
		{"single key no value", []string{"k"}, ""},
		{"one pair", []string{"limit", "10"}, "?limit=10"},
		{"empty value omitted", []string{"limit", ""}, ""},
		{"two pairs sorted", []string{"b", "2", "a", "1"}, "?a=1&b=2"},
		{"trailing odd key dropped", []string{"a", "1", "orphan"}, "?a=1"},
		{"escaping", []string{"q", "a b"}, "?q=a+b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := queryString(tc.pairs...); got != tc.want {
				t.Errorf("queryString(%v) = %q, want %q", tc.pairs, got, tc.want)
			}
		})
	}
}

// ─── requireAuthAndWorkspace ────────────────────────────────────────────

func TestRequireAuthAndWorkspaceCli3(t *testing.T) {
	t.Run("no token", func(t *testing.T) {
		saveCLIState(t)
		cliCfg = &cli.CLIConfig{}
		if _, err := requireAuthAndWorkspace(); err == nil || !strings.Contains(err.Error(), "not logged in") {
			t.Fatalf("expected not-logged-in error, got %v", err)
		}
	})
	t.Run("no workspace", func(t *testing.T) {
		saveCLIState(t)
		t.Setenv("CREWSHIP_WORKSPACE", "")
		flagWorkspace = ""
		cliCfg = &cli.CLIConfig{Token: "tk"}
		if _, err := requireAuthAndWorkspace(); err == nil || !strings.Contains(err.Error(), "workspace") {
			t.Fatalf("expected workspace error, got %v", err)
		}
	})
	t.Run("success returns configured client", func(t *testing.T) {
		stub := covStub(t)
		client, err := requireAuthAndWorkspace()
		if err != nil {
			t.Fatalf("requireAuthAndWorkspace: %v", err)
		}
		if client.BaseURL != stub.URL() {
			t.Errorf("client BaseURL: got %q want %q", client.BaseURL, stub.URL())
		}
		if client.Token != "test-token" {
			t.Errorf("client Token: got %q", client.Token)
		}
	})
}

// ─── applyJQFilter / emitJSONFiltered / jq plumbing ─────────────────────

func TestApplyJQFilter_EmptyExprPassthrough(t *testing.T) {
	in := []byte(`{"a":1}`)
	out, err := applyJQFilter(in, "")
	if err != nil {
		t.Fatalf("applyJQFilter: %v", err)
	}
	if string(out) != string(in) {
		t.Errorf("passthrough mutated input: %q", out)
	}
}

func TestApplyJQFilter_JQMissingFallsBackRaw(t *testing.T) {
	covSwapJQ(t, func(string) (string, error) { return "", fmt.Errorf("not found") }, nil)
	in := []byte(`{"a":1}`)
	out, err := applyJQFilter(in, ".a")
	if err != nil {
		t.Fatalf("applyJQFilter fallback: %v", err)
	}
	if string(out) != string(in) {
		t.Errorf("fallback should return input unchanged, got %q", out)
	}
}

func TestApplyJQFilter_RunsStubbedJQ(t *testing.T) {
	fake := &fakeJQCov{out: []byte("1\n")}
	var gotPath, gotExpr string
	covSwapJQ(t,
		func(string) (string, error) { return "/fake/jq", nil },
		func(path, expr string) jqRunner {
			gotPath, gotExpr = path, expr
			return fake
		})

	out, err := applyJQFilter([]byte(`{"a":1}`), ".a")
	if err != nil {
		t.Fatalf("applyJQFilter: %v", err)
	}
	if string(out) != "1\n" {
		t.Errorf("filtered output: got %q", out)
	}
	if gotPath != "/fake/jq" || gotExpr != ".a" {
		t.Errorf("jq invocation: path=%q expr=%q", gotPath, gotExpr)
	}
	if string(fake.gotStdin) != `{"a":1}` {
		t.Errorf("jq stdin: got %q", fake.gotStdin)
	}
}

func TestRealJQRoundTripViaCat(t *testing.T) {
	// `cat -` echoes stdin — a deterministic stand-in for jq that covers
	// the real exec plumbing (newJQCommand, SetStdin, Output).
	catPath, err := exec.LookPath("cat")
	if err != nil {
		t.Skipf("cat not in PATH: %v", err)
	}
	runner := newJQCommand(catPath, "-")
	runner.SetStdin([]byte("hello-jq"))
	out, err := runner.Output()
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	if string(out) != "hello-jq" {
		t.Errorf("cat round-trip: got %q", out)
	}
}

func TestEmitJSONFiltered(t *testing.T) {
	newCmd := func() *cobra.Command {
		c := &cobra.Command{Use: "x"}
		jqExprFlag(c)
		return c
	}

	t.Run("marshal error", func(t *testing.T) {
		if err := emitJSONFiltered(newCmd(), make(chan int)); err == nil || !strings.Contains(err.Error(), "marshal") {
			t.Fatalf("expected marshal error, got %v", err)
		}
	})

	t.Run("no filter pretty-prints with trailing newline", func(t *testing.T) {
		out := covCaptureStdoutCli3(t, func() {
			if err := emitJSONFiltered(newCmd(), map[string]int{"a": 1}); err != nil {
				t.Errorf("emitJSONFiltered: %v", err)
			}
		})
		if !strings.Contains(out, `"a": 1`) {
			t.Errorf("output missing payload: %q", out)
		}
		if !strings.HasSuffix(out, "\n") {
			t.Errorf("output missing trailing newline: %q", out)
		}
	})

	t.Run("filter error propagates", func(t *testing.T) {
		covSwapJQ(t,
			func(string) (string, error) { return "/fake/jq", nil },
			func(string, string) jqRunner { return &fakeJQCov{err: fmt.Errorf("jq exploded")} })
		c := newCmd()
		if err := c.Flags().Set("filter", ".a"); err != nil {
			t.Fatalf("set filter: %v", err)
		}
		if err := emitJSONFiltered(c, map[string]int{"a": 1}); err == nil || !strings.Contains(err.Error(), "filter") {
			t.Fatalf("expected filter error, got %v", err)
		}
	})

	t.Run("filter success", func(t *testing.T) {
		covSwapJQ(t,
			func(string) (string, error) { return "/fake/jq", nil },
			func(string, string) jqRunner { return &fakeJQCov{out: []byte("42\n")} })
		c := newCmd()
		if err := c.Flags().Set("filter", ".a"); err != nil {
			t.Fatalf("set filter: %v", err)
		}
		out := covCaptureStdoutCli3(t, func() {
			if err := emitJSONFiltered(c, map[string]int{"a": 42}); err != nil {
				t.Errorf("emitJSONFiltered: %v", err)
			}
		})
		if out != "42\n" {
			t.Errorf("filtered output: got %q", out)
		}
	})
}

// Sanity check on the stub harness itself: unregistered routes 404 with
// a descriptive error envelope the CLI surfaces verbatim.
func TestCovStubFallback404(t *testing.T) {
	covStub(t)
	err := getJSON(newAPIClient(), "/api/v1/never-registered", nil)
	if err == nil || !strings.Contains(err.Error(), "no stub registered") {
		t.Fatalf("expected fallback 404 error, got %v", err)
	}
	_ = http.StatusOK // keep net/http import meaningful
}
