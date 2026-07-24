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
		defer r.Close()
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
		defer r.Close()
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

// covActLeadID is the CUID-shaped agent id an assignment.created entry carries
// as its actor_id (the assigner). The activity feed has NO from_slug in the
// assignment payload — the "from" column is resolved by looking this id up in
// the journal/lookup reference table (see covLookupResponse), so the tests
// exercise that real resolution path instead of a fabricated payload slug.
const covActLeadID = "cleadabcdefghijklmnop"

func covActivityRows() []map[string]any {
	return []map[string]any{
		// assignment.created carries the assigner ONLY as actor_id (no
		// from_slug in the payload — that mirrors the real emitter); the
		// target is in payload.target_slug.
		{"id": "j1", "entry_type": "assignment.created", "severity": "info", "summary": "assigned task A", "ts": "2026-06-10T10:00:00Z", "actor_id": covActLeadID, "payload": map[string]any{"target_slug": "viktor"}},
		{"id": "j2", "entry_type": "assignment.running", "severity": "info", "summary": "assignment running", "ts": "2026-06-10T11:00:00Z"},
		{"id": "j3", "entry_type": "peer.escalation", "severity": "warn", "summary": "escalated to lead", "ts": "2026-06-10T12:00:00Z"},
		{"id": "j4", "entry_type": "peer.conversation", "severity": "info", "summary": "asked a question", "ts": "2026-06-10T12:30:00Z"},
		{"id": "j5", "entry_type": "custom.thing", "severity": "info", "summary": "unknown type", "ts": "not-a-timestamp"},
	}
}

// covLookupResponse is the /api/v1/journal/lookup reference table the CLI
// fetches to resolve actor_id → slug. Mapping covActLeadID → "lead" is what
// lets the assignment row render a real "from" column.
func covLookupResponse() map[string]any {
	return map[string]any{
		"crews": []map[string]any{},
		"agents": []map[string]any{
			{"id": covActLeadID, "name": "Lead", "slug": "lead"},
		},
		"missions": []map[string]any{},
	}
}

// covActivityResponse wraps journal rows in the List envelope the
// /api/v1/journal handler returns (entries + next_cursor + count).
func covActivityResponse(rows []map[string]any) map[string]any {
	return map[string]any{"entries": rows, "next_cursor": nil, "count": len(rows)}
}

func TestActivityRunE_TableRendersRows(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/journal", clitest.JSONResponse(200, covActivityResponse(covActivityRows())))

	out := covCaptureStdoutCli9(t, func() {
		if err := activityCmd.RunE(activityCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	for _, want := range []string{"assigned task A", "assignment running", "escalated to lead", "asked a question", "unknown type", "2026-06-10 10:00:00", "not-a-timestamp"} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q:\n%s", want, out)
		}
	}
}

func TestActivityRunE_TypeAndSinceFilters(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/journal", clitest.JSONResponse(200, covActivityResponse(covActivityRows())))
	covSetFlagCli9(t, activityCmd, "type", "assign")
	covSetFlagCli9(t, activityCmd, "since", "2026-06-01T00:00:00Z")

	out := covCaptureStdoutCli9(t, func() {
		if err := activityCmd.RunE(activityCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "assigned task A") {
		t.Errorf("type filter should keep assignment.* rows:\n%s", out)
	}
	if strings.Contains(out, "escalated to lead") {
		t.Errorf("type filter %q should drop the escalation row:\n%s", "assign", out)
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
	s.OnGet("/api/v1/journal", clitest.JSONResponse(200, covActivityResponse(covActivityRows()[:2])))
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
	if row["entry_type"] != "assignment.created" {
		t.Errorf("first row entry_type = %v, want assignment.created", row["entry_type"])
	}
}

// TestActivityRunE_ExportCSV_FromResolvedViaLookup is the real-path coverage
// for the "from" column. The assignment.created entry carries NO from_slug —
// only actor_id — so a passing `from=lead` in the CSV proves the CLI actually
// hit /api/v1/journal/lookup and resolved the id, not that a fixture baked the
// slug into the payload.
func TestActivityRunE_ExportCSV_FromResolvedViaLookup(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/journal", clitest.JSONResponse(200, covActivityResponse(covActivityRows()[:1])))
	s.OnGet("/api/v1/journal/lookup", clitest.JSONResponse(200, covLookupResponse()))
	covSetFlagCli9(t, activityCmd, "export", "csv")

	out := covCaptureStdoutCli9(t, func() {
		if err := activityCmd.RunE(activityCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "ts,entry_type,from_slug,to_slug,summary") {
		t.Errorf("csv header missing:\n%s", out)
	}
	// from=lead is resolved from actor_id via the lookup; to=viktor is the
	// payload target_slug.
	if !strings.Contains(out, "2026-06-10T10:00:00Z,assignment.created,lead,viktor,assigned task A") {
		t.Errorf("csv row missing resolved from-slug:\n%s", out)
	}
}

// TestActivityRunE_ExportCSV_NoLookup_BlankFrom proves the fallback is honest:
// with no lookup available, the id-only "from" degrades to blank rather than
// crashing or inventing a slug.
func TestActivityRunE_ExportCSV_NoLookup_BlankFrom(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/journal", clitest.JSONResponse(200, covActivityResponse(covActivityRows()[:1])))
	// No /api/v1/journal/lookup handler → fetchAgentSlugs gets a 404 and
	// returns an empty map (best-effort).
	covSetFlagCli9(t, activityCmd, "export", "csv")

	out := covCaptureStdoutCli9(t, func() {
		if err := activityCmd.RunE(activityCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "2026-06-10T10:00:00Z,assignment.created,,viktor,assigned task A") {
		t.Errorf("csv row should have a blank from with no lookup:\n%s", out)
	}
}

// TestActivityRunE_LinesOutOfRange asserts an out-of-range --lines is a hard
// error, not a silent clamp.
func TestActivityRunE_LinesOutOfRange(t *testing.T) {
	covStubCli9(t)
	for _, v := range []string{"0", "501", "-3"} {
		covSetFlagCli9(t, activityCmd, "lines", v)
		err := activityCmd.RunE(activityCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "--lines must be between 1 and 500") {
			t.Errorf("--lines=%s: expected range error, got %v", v, err)
		}
	}
}

// TestActivityRunE_DedupePeerConversation asserts a peer query's question +
// answer rows (same thread_id) collapse to a single feed row.
func TestActivityRunE_DedupePeerConversation(t *testing.T) {
	s := covStubCli9(t)
	rows := []map[string]any{
		{"id": "q1", "entry_type": "peer.conversation", "severity": "info", "summary": "api → db: index is present", "ts": "2026-06-10T12:01:00Z", "payload": map[string]any{"thread_id": "conv-1", "message_type": "answer"}},
		{"id": "q0", "entry_type": "peer.conversation", "severity": "info", "summary": "api asked db: is the index there?", "ts": "2026-06-10T12:00:00Z", "payload": map[string]any{"thread_id": "conv-1", "message_type": "question"}},
	}
	s.OnGet("/api/v1/journal", clitest.JSONResponse(200, covActivityResponse(rows)))

	out := covCaptureStdoutCli9(t, func() {
		if err := activityCmd.RunE(activityCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	// Newest-first: the answer survives, the question is dropped.
	if !strings.Contains(out, "index is present") {
		t.Errorf("expected the answer row to survive dedupe:\n%s", out)
	}
	if strings.Contains(out, "is the index there?") {
		t.Errorf("duplicate question row was not deduped:\n%s", out)
	}
}

func TestActivityRunE_ExportUnknownFormat(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/journal", clitest.JSONResponse(200, covActivityResponse([]map[string]any{})))
	covSetFlagCli9(t, activityCmd, "export", "xml")

	err := activityCmd.RunE(activityCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--export must be ndjson or csv") {
		t.Errorf("expected export format error; got %v", err)
	}
}

func TestActivityRunE_JSONFormat(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/journal", clitest.JSONResponse(200, covActivityResponse(covActivityRows()[:1])))
	flagFormat = "json"

	out := covCaptureStdoutCli9(t, func() {
		if err := activityCmd.RunE(activityCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, `"assignment.created"`) {
		t.Errorf("json output should contain the row entry_type:\n%s", out)
	}
}
