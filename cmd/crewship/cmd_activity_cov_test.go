package main

// Shared coverage-test helpers (covSaveState / covStubCli9 / covSetFlagCli9 /
// covCaptureStdoutCli9) live in this file and are used by the other
// *_cov_test.go files in this package. They extend saveCLIState with
// flagFormat handling and env isolation.

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"github.com/spf13/cobra"
)

// covWSCli9 is a CUID-shaped workspace id (21 chars: 'c' + 20 lowercase
// alphanumerics) so Client.GetWorkspaceID short-circuits without a
// slug-resolution round-trip.
const covWSCli9 = "cwsabcdefghijklmnopqr"

// covCrew is a CUID-shaped crew id for tests that want to skip
// resolveCrewID's /api/v1/crews lookup.
const covCrew = "ccrewabcdefghijklmnop"

// covSaveState snapshots the package-level CLI globals (including
// flagFormat, which saveCLIState does not cover) and neutralises the
// CREWSHIP_* env vars that would otherwise override cliCfg.
func covSaveState(t *testing.T) {
	t.Helper()
	origCfg := cliCfg
	origServer := flagServer
	origWorkspace := flagWorkspace
	origFormat := flagFormat
	origVerbose := flagVerbose
	t.Cleanup(func() {
		cliCfg = origCfg
		flagServer = origServer
		flagWorkspace = origWorkspace
		flagFormat = origFormat
		flagVerbose = origVerbose
	})
	t.Setenv("CREWSHIP_SERVER", "")
	t.Setenv("CREWSHIP_WORKSPACE", "")
	flagServer = ""
	flagWorkspace = ""
	flagFormat = ""
}

// covStubCli9 wires a StubServer into the global CLI config with a valid
// token + CUID workspace. Cleanup restores everything.
func covStubCli9(t *testing.T) *clitest.StubServer {
	t.Helper()
	covSaveState(t)
	s := clitest.NewStubServer()
	t.Cleanup(s.Close)
	cliCfg = &cli.CLIConfig{Token: "test-token", Workspace: covWSCli9, Server: s.URL()}
	return s
}

// covSetFlagCli9 sets a Cobra flag for the duration of the test and
// restores the default value (and the Changed marker) afterwards so
// global command objects don't leak state between tests.
func covSetFlagCli9(t *testing.T, cmd *cobra.Command, name, value string) {
	t.Helper()
	f := cmd.Flags().Lookup(name)
	if f == nil {
		t.Fatalf("command %s has no --%s flag", cmd.Name(), name)
	}
	if err := cmd.Flags().Set(name, value); err != nil {
		t.Fatalf("set --%s=%s: %v", name, value, err)
	}
	t.Cleanup(func() {
		_ = f.Value.Set(f.DefValue)
		f.Changed = false
	})
}

// covCaptureStdoutCli9 redirects os.Stdout while fn runs and returns
// everything written. Not safe for parallel tests (mutates a global).
func covCaptureStdoutCli9(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	defer func() { os.Stdout = orig }()
	fn()
	_ = w.Close()
	os.Stdout = orig
	return <-done
}

// covStubDown points the CLI at a server address that is guaranteed to
// refuse connections (an httptest server that was already closed), so
// transport-level error branches can be exercised deterministically.
func covStubDown(t *testing.T) {
	t.Helper()
	covSaveState(t)
	s := clitest.NewStubServer()
	deadURL := s.URL()
	s.Close()
	cliCfg = &cli.CLIConfig{Token: "test-token", Workspace: covWSCli9, Server: deadURL}
}

// covCaptureStderrCli9 mirrors covCaptureStdoutCli9 for os.Stderr — needed for
// cli.PrintSuccess / warning output which writes to stderr by design.
func covCaptureStderrCli9(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	defer func() { os.Stderr = orig }()
	fn()
	_ = w.Close()
	os.Stderr = orig
	return <-done
}

// ─── activity: export / filter / rendering paths ───────────────────────

func covActivityRows() []map[string]any {
	return []map[string]any{
		{"type": "ASSIGNMENT", "crew_slug": "backend", "summary": "assigned task A", "created_at": "2026-06-10T10:00:00Z", "from_slug": "lead", "to_slug": "viktor"},
		{"type": "COMPLETED", "crew_slug": "backend", "summary": "finished task A", "created_at": "2026-06-10T11:00:00Z"},
		{"type": "ESCALATION", "crew_slug": "backend", "summary": "escalated to lead", "created_at": "2026-06-10T12:00:00Z"},
		{"type": "QUERY", "crew_slug": "backend", "summary": "asked a question", "created_at": "2026-06-10T12:30:00Z"},
		{"type": "weird", "crew_slug": "backend", "summary": "unknown type", "created_at": "not-a-timestamp"},
	}
}

func TestActivityRunE_TableRendersRows(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/activity", clitest.JSONResponse(200, covActivityRows()))

	out := covCaptureStdoutCli9(t, func() {
		if err := activityCmd.RunE(activityCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	for _, want := range []string{"assigned task A", "finished task A", "escalated to lead", "asked a question", "unknown type", "2026-06-10 10:00:00", "not-a-timestamp"} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q:\n%s", want, out)
		}
	}
}

func TestActivityRunE_TypeAndSinceFilters(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/activity", clitest.JSONResponse(200, covActivityRows()))
	covSetFlagCli9(t, activityCmd, "type", "assign")
	covSetFlagCli9(t, activityCmd, "since", "2026-06-01T00:00:00Z")

	out := covCaptureStdoutCli9(t, func() {
		if err := activityCmd.RunE(activityCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "assigned task A") {
		t.Errorf("type filter should keep ASSIGNMENT row:\n%s", out)
	}
	if strings.Contains(out, "finished task A") {
		t.Errorf("type filter should drop COMPLETED row:\n%s", out)
	}
}

func TestActivityRunE_BadSince(t *testing.T) {
	covStubCli9(t)
	covSetFlagCli9(t, activityCmd, "since", "definitely-not-a-time")

	err := activityCmd.RunE(activityCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "bad --since") {
		t.Errorf("expected bad --since error; got %v", err)
	}
}

func TestActivityRunE_ExportNDJSONToFile(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/activity", clitest.JSONResponse(200, covActivityRows()[:2]))
	outPath := filepath.Join(t.TempDir(), "activity.ndjson")
	covSetFlagCli9(t, activityCmd, "export", "ndjson")
	covSetFlagCli9(t, activityCmd, "out", outPath)

	stdout := covCaptureStdoutCli9(t, func() {
		if err := activityCmd.RunE(activityCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	_ = stdout

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read export file: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("ndjson lines = %d, want 2; data=%s", len(lines), data)
	}
	var row map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &row); err != nil {
		t.Fatalf("first ndjson line is not JSON: %v", err)
	}
	if row["type"] != "ASSIGNMENT" {
		t.Errorf("first row type = %v, want ASSIGNMENT", row["type"])
	}
}

func TestActivityRunE_ExportCSVToStdout(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/activity", clitest.JSONResponse(200, covActivityRows()[:1]))
	covSetFlagCli9(t, activityCmd, "export", "csv")

	out := covCaptureStdoutCli9(t, func() {
		if err := activityCmd.RunE(activityCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "created_at,type,crew_slug,from_slug,to_slug,summary") {
		t.Errorf("csv header missing:\n%s", out)
	}
	if !strings.Contains(out, "ASSIGNMENT,backend,lead,viktor,assigned task A") {
		t.Errorf("csv row missing:\n%s", out)
	}
}

func TestActivityRunE_ExportUnknownFormat(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/activity", clitest.JSONResponse(200, []map[string]any{}))
	covSetFlagCli9(t, activityCmd, "export", "xml")

	err := activityCmd.RunE(activityCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--export must be ndjson or csv") {
		t.Errorf("expected export format error; got %v", err)
	}
}

func TestActivityRunE_JSONFormat(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/activity", clitest.JSONResponse(200, covActivityRows()[:1]))
	flagFormat = "json"

	out := covCaptureStdoutCli9(t, func() {
		if err := activityCmd.RunE(activityCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, `"ASSIGNMENT"`) {
		t.Errorf("json output should contain the row type:\n%s", out)
	}
}
