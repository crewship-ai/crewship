package main

// Coverage tests for cmd_helpers.go (resolveAgentID, resolveCrewID,
// resolveIntegrationID) plus shared scaffolding used by the other
// *_cov_test.go files in this package.

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// covWorkspaceIDCli10 is a CUID-shaped workspace id so GetWorkspaceID()
// short-circuits without a slug-resolution round-trip.
const covWorkspaceIDCli10 = "cws1234567890abcdefghij"

// covSetupCli10 snapshots/neutralises all package-level CLI state the RunE
// paths consult and points the client at the given server URL. Not
// parallel-safe by design — cmd/crewship tests mutate globals.
func covSetupCli10(t *testing.T, serverURL string) {
	t.Helper()
	saveCLIState(t)
	origFormat := flagFormat
	t.Cleanup(func() { flagFormat = origFormat })
	t.Setenv("CREWSHIP_SERVER", "")
	t.Setenv("CREWSHIP_WORKSPACE", "")
	t.Setenv("CREWSHIP_DEFAULT_AGENT", "")
	// Point config IO at a throwaway file so nothing touches the real
	// ~/.crewship/cli-config.yaml.
	t.Setenv("CREWSHIP_CONFIG", filepath.Join(t.TempDir(), "cli-config.yaml"))
	cliCfg = &cli.CLIConfig{Token: "test-token", Workspace: covWorkspaceIDCli10, Server: serverURL}
	flagServer = ""
	flagWorkspace = ""
	flagFormat = ""
}

// setFlagCovCli10 sets a cobra flag and restores both value and Changed
// state at test end so global command vars don't leak across tests.
func setFlagCovCli10(t *testing.T, cmd *cobra.Command, name, value string) {
	t.Helper()
	fl := cmd.Flags().Lookup(name)
	if fl == nil {
		t.Fatalf("flag --%s not found on %s", name, cmd.Name())
	}
	orig := fl.Value.String()
	origChanged := fl.Changed
	if err := cmd.Flags().Set(name, value); err != nil {
		t.Fatalf("set --%s=%s: %v", name, value, err)
	}
	t.Cleanup(func() {
		_ = fl.Value.Set(orig)
		fl.Changed = origChanged
	})
}

// captureStdoutCovCli10 runs fn with os.Stdout redirected into a pipe and
// returns whatever was printed plus fn's error.
func captureStdoutCovCli10(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	runErr := fn()
	_ = w.Close()
	os.Stdout = orig
	out := <-done
	_ = r.Close()
	return out, runErr
}

// captureStderrCov mirrors captureStdoutCovCli10 for os.Stderr.
func captureStderrCov(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	runErr := fn()
	_ = w.Close()
	os.Stderr = orig
	out := <-done
	_ = r.Close()
	return out, runErr
}

// ─── resolveAgentID ─────────────────────────────────────────────────────

func TestResolveAgentIDCov_CUIDShortCircuit(t *testing.T) {
	// No server needed — CUID input must never hit the network.
	c := cli.NewClient("http://127.0.0.1:0", "t", covWorkspaceIDCli10)
	id := "cagent7890abcdefghijklm"
	got, err := resolveAgentID(c, id)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != id {
		t.Errorf("got %q want %q", got, id)
	}
}

func TestResolveAgentIDCov_SlugFound(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{
		{"id": "cagent7890abcdefghijklm", "slug": "viktor"},
		{"id": "cagent7890abcdefghijkln", "slug": "eva"},
	}))
	c := cli.NewClient(s.URL(), "t", covWorkspaceIDCli10)
	got, err := resolveAgentID(c, "eva")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "cagent7890abcdefghijkln" {
		t.Errorf("got %q", got)
	}
}

func TestResolveAgentIDCov_TypoSuggestsNearMatch(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{
		{"id": "ca1", "slug": "viktor"},
	}))
	c := cli.NewClient(s.URL(), "t", covWorkspaceIDCli10)
	_, err := resolveAgentID(c, "vitkor")
	if err == nil || !strings.Contains(err.Error(), "Did you mean: viktor") {
		t.Errorf("expected near-match suggestion, got %v", err)
	}
}

func TestResolveAgentIDCov_NoAgentsInWorkspace(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{}))
	c := cli.NewClient(s.URL(), "t", covWorkspaceIDCli10)
	_, err := resolveAgentID(c, "ghost")
	if err == nil || !strings.Contains(err.Error(), "no agents in this workspace") {
		t.Errorf("expected empty-workspace error, got %v", err)
	}
}

func TestResolveAgentIDCov_NoSuggestionListsAvailable(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{
		{"id": "ca1", "slug": "viktor"},
		{"id": "ca2", "slug": "eva"},
	}))
	c := cli.NewClient(s.URL(), "t", covWorkspaceIDCli10)
	// A query with no plausible near match falls through to the
	// "Available:" listing.
	_, err := resolveAgentID(c, "zzzzzzzzzzzzzzzzzzzzzz")
	if err == nil || !strings.Contains(err.Error(), "Available:") {
		t.Errorf("expected available-slug listing, got %v", err)
	}
}

func TestResolveAgentIDCov_ServerError(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/agents", clitest.ErrorResponse(500, "boom"))
	c := cli.NewClient(s.URL(), "t", covWorkspaceIDCli10)
	if _, err := resolveAgentID(c, "viktor"); err == nil {
		t.Error("expected error from 500")
	}
}

// ─── resolveCrewID ──────────────────────────────────────────────────────

func TestResolveCrewIDCov_NotFound(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{
		{"id": "cc1", "slug": "backend"},
	}))
	c := cli.NewClient(s.URL(), "t", covWorkspaceIDCli10)
	_, err := resolveCrewID(c, "frontend")
	if err == nil || !strings.Contains(err.Error(), "crew not found: frontend") {
		t.Errorf("expected not-found error, got %v", err)
	}
}

func TestResolveCrewIDCov_ServerError(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/crews", clitest.ErrorResponse(503, "down"))
	c := cli.NewClient(s.URL(), "t", covWorkspaceIDCli10)
	if _, err := resolveCrewID(c, "backend"); err == nil {
		t.Error("expected error from 503")
	}
}

// ─── resolveIntegrationID ───────────────────────────────────────────────

func TestResolveIntegrationIDCov(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/integrations", clitest.JSONResponse(200, []map[string]string{
		{"id": "ci1", "name": "github"},
		{"id": "ci2", "name": "slack"},
	}))
	c := cli.NewClient(s.URL(), "t", covWorkspaceIDCli10)

	if got, err := resolveIntegrationID(c, "slack"); err != nil || got != "ci2" {
		t.Errorf("by name: got %q err %v", got, err)
	}
	if got, err := resolveIntegrationID(c, "ci1"); err != nil || got != "ci1" {
		t.Errorf("by id: got %q err %v", got, err)
	}
	if _, err := resolveIntegrationID(c, "jira"); err == nil || !strings.Contains(err.Error(), `integration "jira" not found`) {
		t.Errorf("missing: %v", err)
	}
}

func TestResolveAgentIDCov_MalformedJSON(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/agents", clitest.TextResponse(200, `{not json`))
	c := cli.NewClient(s.URL(), "t", covWorkspaceIDCli10)
	if _, err := resolveAgentID(c, "viktor"); err == nil {
		t.Error("expected decode error")
	}
}

func TestResolveCrewIDCov_MalformedJSON(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/crews", clitest.TextResponse(200, `[{`))
	c := cli.NewClient(s.URL(), "t", covWorkspaceIDCli10)
	if _, err := resolveCrewID(c, "backend"); err == nil {
		t.Error("expected decode error")
	}
}

func TestResolveIntegrationIDCov_MalformedJSON(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/integrations", clitest.TextResponse(200, `nope`))
	c := cli.NewClient(s.URL(), "t", covWorkspaceIDCli10)
	if _, err := resolveIntegrationID(c, "github"); err == nil {
		t.Error("expected decode error")
	}
}

func TestResolveIntegrationIDCov_ServerError(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/integrations", clitest.ErrorResponse(500, "nope"))
	c := cli.NewClient(s.URL(), "t", covWorkspaceIDCli10)
	if _, err := resolveIntegrationID(c, "github"); err == nil {
		t.Error("expected error from 500")
	}
}
