package main

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// ─── Shared coverage-test scaffolding ────────────────────────────────
//
// These helpers are shared by the *_cov_test.go siblings (same
// package). They are deliberately NOT parallel-safe: they mutate the
// package-level cliCfg / flag globals, exactly like the production
// PersistentPreRun does.

// covWorkspaceIDCli4 is CUID-shaped so cli.Client.GetWorkspaceID and the
// cmd-side looksLikeCUID short-circuit without a resolution round-trip.
const covWorkspaceIDCli4 = "cworkspace0123456789abcd"

// covCrewIDCli4 / covAgentIDCli4 are CUID-shaped resource ids (c + ≥20 [a-z0-9]).
const (
	covCrewIDCli4  = "ccrew0123456789abcdefghij"
	covAgentIDCli4 = "cagent0123456789abcdefghi"
)

// covSetupCli4 snapshots CLI globals, spins a stub API server and points
// the package-level config at it. Returns the stub for route
// registration + call assertions.
func covSetupCli4(t *testing.T) *clitest.StubServer {
	t.Helper()
	saveCLIState(t)
	stub := clitest.NewStubServer()
	t.Cleanup(stub.Close)
	// Neutralise ambient env that would override cliCfg resolution.
	t.Setenv("CREWSHIP_SERVER", "")
	t.Setenv("CREWSHIP_WORKSPACE", "")
	flagServer = ""
	flagWorkspace = ""
	cliCfg = &cli.CLIConfig{Token: "test-token", Workspace: covWorkspaceIDCli4, Server: stub.URL()}
	return stub
}

// covCaptureStdoutCli4 redirects BOTH os.Stdout and os.Stderr into one
// buffer for the duration of fn — the cmd_* RunE bodies print to
// os.Stdout via fmt.Print* while cli.PrintSuccess/Warning go to
// os.Stderr; tests assert against the combined terminal transcript.
func covCaptureStdoutCli4(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout, os.Stderr = w, w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	runErr := fn()
	_ = w.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	return <-done, runErr
}

// covWithStdinCli4 substitutes os.Stdin with a pipe pre-loaded with
// content (then closed), so confirm prompts read deterministically.
func covWithStdinCli4(t *testing.T, content string, fn func() error) error {
	t.Helper()
	old := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if _, err := w.WriteString(content); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	_ = w.Close()
	os.Stdin = r
	defer func() {
		os.Stdin = old
		_ = r.Close()
	}()
	return fn()
}

// covFreshCmd builds a NEW cobra.Command around an existing command's
// RunE with freshly-declared flags. Cobra flag state (notably
// Changed()) is sticky on the package-level command vars; reusing
// them across tests would leak state between subtests.
func covFreshCmd(src *cobra.Command, declare func(c *cobra.Command)) *cobra.Command {
	c := &cobra.Command{Use: src.Use, RunE: src.RunE}
	if declare != nil {
		declare(c)
	}
	return c
}

func covSetFlagsCli4(t *testing.T, c *cobra.Command, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		if err := c.Flags().Set(k, v); err != nil {
			t.Fatalf("set --%s=%s: %v", k, v, err)
		}
	}
}

// ─── validateCrewFlags ───────────────────────────────────────────────

func TestValidateCrewFlags_Table(t *testing.T) {
	cases := []struct {
		name        string
		memoryMB    int
		cpus        float64
		ttl         int
		ttlSet      bool
		networkMode string
		wantErr     string
	}{
		{name: "all defaults ok"},
		{name: "negative memory", memoryMB: -1, wantErr: "--memory-mb must be >= 0"},
		{name: "negative cpus", cpus: -0.5, wantErr: "--cpus must be >= 0"},
		{name: "negative ttl set", ttl: -2, ttlSet: true, wantErr: "--ttl must be >= 0"},
		{name: "negative ttl NOT set is ignored", ttl: -2, ttlSet: false},
		{name: "network free ok", networkMode: "free"},
		{name: "network restricted ok", networkMode: "restricted"},
		{name: "network junk rejected", networkMode: "host", wantErr: "--network-mode must be one of"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCrewFlags(tc.memoryMB, tc.cpus, tc.ttl, tc.ttlSet, tc.networkMode)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("got %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

// ─── sanitizeTerminal ────────────────────────────────────────────────

func TestSanitizeTerminal_StripsControlKeepsWhitespace(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"plain text", "plain text"},
		{"line1\nline2", "line1\nline2"},     // newline preserved
		{"col1\tcol2", "col1\tcol2"},         // tab preserved
		{"evil\rcarriage", "evilcarriage"},   // CR stripped
		{"\x1b[31mred\x1b[0m", "[31mred[0m"}, // ESC stripped, printable remains
		{"bell\x07ding", "bellding"},         // BEL stripped
		{"null\x00byte", "nullbyte"},         // NUL stripped
		{"emoji 🛠 stays", "emoji 🛠 stays"},   // non-control unicode kept
	}
	for _, tc := range cases {
		if got := sanitizeTerminal(tc.in); got != tc.want {
			t.Errorf("sanitizeTerminal(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ─── crew create ─────────────────────────────────────────────────────

func declareCrewCreateFlags(c *cobra.Command) {
	c.Flags().String("name", "", "")
	c.Flags().String("slug", "", "")
	c.Flags().String("description", "", "")
	c.Flags().String("color", "", "")
	c.Flags().String("icon", "", "")
	c.Flags().Int("memory-mb", 0, "")
	c.Flags().Float64("cpus", 0, "")
	c.Flags().Int("ttl", 0, "")
	c.Flags().String("network-mode", "", "")
	c.Flags().String("allowed-domains", "", "")
}

func TestCrewCreateRunE_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}
	c := covFreshCmd(crewCreateCmd, declareCrewCreateFlags)
	err := c.RunE(c, nil)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("want 'not logged in', got %v", err)
	}
}

func TestCrewCreateRunE_NoWorkspace(t *testing.T) {
	saveCLIState(t)
	t.Setenv("CREWSHIP_WORKSPACE", "")
	flagWorkspace = ""
	cliCfg = &cli.CLIConfig{Token: "tok"}
	c := covFreshCmd(crewCreateCmd, declareCrewCreateFlags)
	err := c.RunE(c, nil)
	if err == nil || !strings.Contains(err.Error(), "no workspace set") {
		t.Fatalf("want workspace error, got %v", err)
	}
}

func TestCrewCreateRunE_NameRequired(t *testing.T) {
	covSetupCli4(t)
	c := covFreshCmd(crewCreateCmd, declareCrewCreateFlags)
	err := c.RunE(c, nil)
	if err == nil || !strings.Contains(err.Error(), "--name is required") {
		t.Fatalf("want --name required, got %v", err)
	}
}

func TestCrewCreateRunE_InvalidResourceFlags(t *testing.T) {
	covSetupCli4(t)
	c := covFreshCmd(crewCreateCmd, declareCrewCreateFlags)
	covSetFlagsCli4(t, c, map[string]string{"name": "Engineering", "memory-mb": "-5"})
	err := c.RunE(c, nil)
	if err == nil || !strings.Contains(err.Error(), "--memory-mb") {
		t.Fatalf("want memory validation error, got %v", err)
	}
}

func TestCrewCreateRunE_HappyPath_BodyAndSlugDerivation(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnPost("/api/v1/crews", clitest.JSONResponse(201, map[string]string{
		"id": covCrewIDCli4, "slug": "engineering",
	}))

	c := covFreshCmd(crewCreateCmd, declareCrewCreateFlags)
	covSetFlagsCli4(t, c, map[string]string{
		"name":            "Engineering 🛠", // slug must be derived without the emoji
		"description":     "builds things",
		"color":           "#3B82F6",
		"icon":            "🛠",
		"memory-mb":       "2048",
		"cpus":            "1.5",
		"ttl":             "4",
		"network-mode":    "restricted",
		"allowed-domains": " github.com , api.anthropic.com ,",
	})

	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Crew created: engineering ("+covCrewIDCli4+")") {
		t.Errorf("success line missing: %q", out)
	}

	calls := stub.CallsFor("POST", "/api/v1/crews")
	if len(calls) != 1 {
		t.Fatalf("POST /crews calls = %d, want 1", len(calls))
	}
	var body map[string]any
	if err := json.Unmarshal(calls[0].Body, &body); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	if body["name"] != "Engineering 🛠" {
		t.Errorf("name = %v", body["name"])
	}
	if body["slug"] != "engineering" {
		t.Errorf("derived slug = %v, want engineering", body["slug"])
	}
	if body["description"] != "builds things" || body["color"] != "#3B82F6" || body["icon"] != "🛠" {
		t.Errorf("optional strings not forwarded: %v", body)
	}
	if body["container_memory_mb"] != float64(2048) || body["container_cpus"] != 1.5 || body["container_ttl_hours"] != float64(4) {
		t.Errorf("resource limits not forwarded: %v", body)
	}
	if body["network_mode"] != "restricted" {
		t.Errorf("network_mode = %v", body["network_mode"])
	}
	domains, ok := body["allowed_domains"].([]any)
	if !ok || len(domains) != 2 || domains[0] != "github.com" || domains[1] != "api.anthropic.com" {
		t.Errorf("allowed_domains not trimmed/split: %v", body["allowed_domains"])
	}
}

func TestCrewCreateRunE_ServerError(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnPost("/api/v1/crews", clitest.ErrorResponse(400, "slug must be 2-50 characters"))
	c := covFreshCmd(crewCreateCmd, declareCrewCreateFlags)
	covSetFlagsCli4(t, c, map[string]string{"name": "X Y"})
	err := c.RunE(c, nil)
	if err == nil || !strings.Contains(err.Error(), "slug must be 2-50 characters") {
		t.Fatalf("want server error surfaced, got %v", err)
	}
}

// ─── crew update ─────────────────────────────────────────────────────

func declareCrewUpdateFlags(c *cobra.Command) {
	c.Flags().String("name", "", "")
	c.Flags().String("description", "", "")
	c.Flags().String("color", "", "")
	c.Flags().String("icon", "", "")
	c.Flags().Int("memory-mb", 0, "")
	c.Flags().Float64("cpus", 0, "")
	c.Flags().Int("ttl", -1, "")
	c.Flags().String("network-mode", "", "")
	c.Flags().String("allowed-domains", "", "")
}

func TestCrewUpdateRunE_NoFields(t *testing.T) {
	covSetupCli4(t)
	c := covFreshCmd(crewUpdateCmd, declareCrewUpdateFlags)
	err := c.RunE(c, []string{covCrewIDCli4})
	if err == nil || !strings.Contains(err.Error(), "no fields to update") {
		t.Fatalf("want 'no fields to update', got %v", err)
	}
}

func TestCrewUpdateRunE_InvalidNetworkMode(t *testing.T) {
	covSetupCli4(t)
	c := covFreshCmd(crewUpdateCmd, declareCrewUpdateFlags)
	covSetFlagsCli4(t, c, map[string]string{"network-mode": "bridge"})
	err := c.RunE(c, []string{covCrewIDCli4})
	if err == nil || !strings.Contains(err.Error(), "--network-mode") {
		t.Fatalf("want network-mode error, got %v", err)
	}
}

func TestCrewUpdateRunE_HappyPath_SlugResolutionAndBody(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{
		{"id": covCrewIDCli4, "slug": "engineering"},
	}))
	stub.OnPatch("/api/v1/crews/"+covCrewIDCli4, clitest.JSONResponse(200, map[string]string{"id": covCrewIDCli4}))

	c := covFreshCmd(crewUpdateCmd, declareCrewUpdateFlags)
	covSetFlagsCli4(t, c, map[string]string{
		"name":            "Engineering v2",
		"ttl":             "0", // explicit 0 clears TTL server-side
		"allowed-domains": "",  // explicit empty list
	})

	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{"engineering"}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Crew updated successfully.") {
		t.Errorf("missing success message: %q", out)
	}

	patches := stub.CallsFor("PATCH", "/api/v1/crews/"+covCrewIDCli4)
	if len(patches) != 1 {
		t.Fatalf("PATCH calls = %d, want 1", len(patches))
	}
	var body map[string]any
	if err := json.Unmarshal(patches[0].Body, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["name"] != "Engineering v2" {
		t.Errorf("name = %v", body["name"])
	}
	if body["container_ttl_hours"] != float64(0) {
		t.Errorf("ttl = %v, want 0", body["container_ttl_hours"])
	}
	if domains, ok := body["allowed_domains"].([]any); !ok || len(domains) != 0 {
		t.Errorf("allowed_domains = %v, want empty list", body["allowed_domains"])
	}
}

func TestCrewUpdateRunE_CrewNotFound(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{}))
	c := covFreshCmd(crewUpdateCmd, declareCrewUpdateFlags)
	covSetFlagsCli4(t, c, map[string]string{"name": "x"})
	err := c.RunE(c, []string{"nope"})
	if err == nil || !strings.Contains(err.Error(), "crew not found: nope") {
		t.Fatalf("want crew-not-found, got %v", err)
	}
}

// ─── crew delete ─────────────────────────────────────────────────────

func TestCrewDeleteRunE_YesSkipsPromptAndDeletes(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnDelete("/api/v1/crews/"+covCrewIDCli4, clitest.EmptyResponse(204))

	c := covFreshCmd(crewDeleteCmd, func(c *cobra.Command) {
		c.Flags().BoolP("yes", "y", false, "")
	})
	covSetFlagsCli4(t, c, map[string]string{"yes": "true"})

	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{covCrewIDCli4}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Crew deleted.") {
		t.Errorf("missing success: %q", out)
	}
	if got := len(stub.CallsFor("DELETE", "/api/v1/crews/"+covCrewIDCli4)); got != 1 {
		t.Errorf("DELETE calls = %d, want 1", got)
	}
}

func TestCrewDeleteRunE_PromptAborts(t *testing.T) {
	stub := covSetupCli4(t)
	c := covFreshCmd(crewDeleteCmd, func(c *cobra.Command) {
		c.Flags().BoolP("yes", "y", false, "")
	})
	// Piped (non-TTY) stdin answering "n" → confirmAction must abort
	// before any API call happens.
	err := covWithStdinCli4(t, "n\n", func() error { return c.RunE(c, []string{covCrewIDCli4}) })
	if err == nil || err.Error() != "aborted" {
		t.Fatalf("want aborted, got %v", err)
	}
	if got := len(stub.Calls()); got != 0 {
		t.Errorf("no API calls expected on abort, got %d", got)
	}
}

// ─── crew suggest ────────────────────────────────────────────────────

func TestCrewSuggestRunE_GoalRequired(t *testing.T) {
	covSetupCli4(t)
	c := covFreshCmd(crewSuggestCmd, func(c *cobra.Command) {
		c.Flags().String("goal", "", "")
	})
	err := c.RunE(c, nil)
	if err == nil || !strings.Contains(err.Error(), "--goal is required") {
		t.Fatalf("want --goal required, got %v", err)
	}
}

func TestCrewSuggestRunE_HappyPath_SanitizesOutput(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnPost("/api/v1/crew-ai-suggest", clitest.JSONResponse(200, map[string]any{
		"crew_name":   "Growth\x1b[2JTeam", // hostile control chars from the LLM
		"description": "does\rthings",
		"agents": []map[string]string{
			{"name": "Eva", "role_title": "Lead Marketer", "agent_role": "LEAD"},
		},
	}))

	c := covFreshCmd(crewSuggestCmd, func(c *cobra.Command) {
		c.Flags().String("goal", "", "")
	})
	covSetFlagsCli4(t, c, map[string]string{"goal": "grow the userbase"})

	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if strings.ContainsRune(out, '\x1b') || strings.ContainsRune(out, '\r') {
		t.Errorf("control characters leaked to terminal: %q", out)
	}
	if !strings.Contains(out, "Growth[2JTeam") || !strings.Contains(out, "doesthings") {
		t.Errorf("sanitized fields missing: %q", out)
	}
	if !strings.Contains(out, "Eva (Lead Marketer, LEAD)") {
		t.Errorf("agent line missing: %q", out)
	}

	calls := stub.CallsFor("POST", "/api/v1/crew-ai-suggest")
	if len(calls) != 1 {
		t.Fatalf("suggest calls = %d, want 1", len(calls))
	}
	if !strings.Contains(string(calls[0].Body), `"goal":"grow the userbase"`) {
		t.Errorf("goal not forwarded: %s", calls[0].Body)
	}
}

// ─── remaining error branches ────────────────────────────────────────

// covDeadServerCli4 points the CLI at a port nothing listens on, so the
// FIRST HTTP call every RunE makes fails with a transport error.
func covDeadServerCli4(t *testing.T) {
	t.Helper()
	saveCLIState(t)
	t.Setenv("CREWSHIP_SERVER", "")
	t.Setenv("CREWSHIP_WORKSPACE", "")
	flagServer = ""
	flagWorkspace = ""
	cliCfg = &cli.CLIConfig{Token: "test-token", Workspace: covWorkspaceIDCli4, Server: "http://127.0.0.1:1"}
}

func TestDeriveSlugFromName_TruncatesAt50(t *testing.T) {
	long := strings.Repeat("abcde ", 20) // slugifies to 100+ chars
	got := deriveSlugFromName(long)
	if len(got) > 50 {
		t.Errorf("slug length = %d, want <= 50 (%q)", len(got), got)
	}
	if strings.HasSuffix(got, "-") || strings.HasSuffix(got, "_") {
		t.Errorf("slug must not end in separator after truncation: %q", got)
	}
}

func TestCrewManageGates_AuthAndWorkspace(t *testing.T) {
	builders := map[string]func() *cobra.Command{
		"update": func() *cobra.Command { return covFreshCmd(crewUpdateCmd, declareCrewUpdateFlags) },
		"delete": func() *cobra.Command {
			return covFreshCmd(crewDeleteCmd, func(c *cobra.Command) { c.Flags().BoolP("yes", "y", false, "") })
		},
		"suggest": func() *cobra.Command {
			return covFreshCmd(crewSuggestCmd, func(c *cobra.Command) { c.Flags().String("goal", "", "") })
		},
	}
	for name, build := range builders {
		t.Run(name+" no auth", func(t *testing.T) {
			saveCLIState(t)
			cliCfg = &cli.CLIConfig{}
			c := build()
			if err := c.RunE(c, []string{covCrewIDCli4}); err == nil || !strings.Contains(err.Error(), "not logged in") {
				t.Errorf("want not-logged-in, got %v", err)
			}
		})
		t.Run(name+" no workspace", func(t *testing.T) {
			saveCLIState(t)
			t.Setenv("CREWSHIP_WORKSPACE", "")
			flagWorkspace = ""
			cliCfg = &cli.CLIConfig{Token: "tok"}
			c := build()
			if err := c.RunE(c, []string{covCrewIDCli4}); err == nil || !strings.Contains(err.Error(), "no workspace set") {
				t.Errorf("want workspace error, got %v", err)
			}
		})
	}
}

func TestCrewUpdateRunE_AllMutableFlags(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnPatch("/api/v1/crews/"+covCrewIDCli4, clitest.JSONResponse(200, map[string]string{"id": covCrewIDCli4}))

	c := covFreshCmd(crewUpdateCmd, declareCrewUpdateFlags)
	covSetFlagsCli4(t, c, map[string]string{
		"name":            "N2",
		"description":     "D2",
		"color":           "#000000",
		"icon":            "⚙️",
		"memory-mb":       "4096",
		"cpus":            "2.5",
		"network-mode":    "free",
		"allowed-domains": "a.com, b.com",
	})

	if _, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{covCrewIDCli4}) }); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	calls := stub.CallsFor("PATCH", "/api/v1/crews/"+covCrewIDCli4)
	if len(calls) != 1 {
		t.Fatalf("PATCH calls = %d", len(calls))
	}
	var body map[string]any
	if err := json.Unmarshal(calls[0].Body, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := map[string]any{
		"name": "N2", "description": "D2", "color": "#000000", "icon": "⚙️",
		"container_memory_mb": float64(4096), "container_cpus": 2.5,
		"network_mode": "free",
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("body[%s] = %v, want %v", k, body[k], v)
		}
	}
	domains, ok := body["allowed_domains"].([]any)
	if !ok || len(domains) != 2 || domains[0] != "a.com" || domains[1] != "b.com" {
		t.Errorf("allowed_domains = %v", body["allowed_domains"])
	}
}

func TestCrewCreateRunE_TransportError(t *testing.T) {
	covDeadServerCli4(t)
	c := covFreshCmd(crewCreateCmd, declareCrewCreateFlags)
	covSetFlagsCli4(t, c, map[string]string{"name": "Engineering"})
	if err := c.RunE(c, nil); err == nil || !strings.Contains(err.Error(), "connect") {
		t.Fatalf("want transport error, got %v", err)
	}
}

func TestCrewCreateRunE_MalformedResponse(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnPost("/api/v1/crews", clitest.TextResponse(201, "not json at all"))
	c := covFreshCmd(crewCreateCmd, declareCrewCreateFlags)
	covSetFlagsCli4(t, c, map[string]string{"name": "Engineering"})
	if err := c.RunE(c, nil); err == nil {
		t.Fatal("want decode error for malformed create response")
	}
}

func TestCrewDeleteRunE_ResolveAndServerErrors(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{}))

	c := covFreshCmd(crewDeleteCmd, func(c *cobra.Command) { c.Flags().BoolP("yes", "y", false, "") })
	covSetFlagsCli4(t, c, map[string]string{"yes": "true"})
	if err := c.RunE(c, []string{"ghost"}); err == nil || !strings.Contains(err.Error(), "crew not found: ghost") {
		t.Fatalf("want resolve error, got %v", err)
	}

	stub.OnDelete("/api/v1/crews/"+covCrewIDCli4, clitest.ErrorResponse(409, "crew busy"))
	if err := c.RunE(c, []string{covCrewIDCli4}); err == nil || !strings.Contains(err.Error(), "crew busy") {
		t.Fatalf("want server error, got %v", err)
	}
}

func TestCrewSuggestRunE_ServerAndDecodeErrors(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnPost("/api/v1/crew-ai-suggest", clitest.ErrorResponse(503, "no LLM configured"))
	c := covFreshCmd(crewSuggestCmd, func(c *cobra.Command) { c.Flags().String("goal", "", "") })
	covSetFlagsCli4(t, c, map[string]string{"goal": "anything"})
	if err := c.RunE(c, nil); err == nil || !strings.Contains(err.Error(), "no LLM configured") {
		t.Fatalf("want server error, got %v", err)
	}

	stub.OnPost("/api/v1/crew-ai-suggest", clitest.TextResponse(200, "<html>nope</html>"))
	if err := c.RunE(c, nil); err == nil {
		t.Fatal("want decode error for malformed suggest response")
	}
}
