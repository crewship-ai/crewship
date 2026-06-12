package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func covExportStubs(t *testing.T, s *clitest.StubServer) {
	t.Helper()
	s.OnGet("/api/v1/runs", clitest.JSONResponse(200, map[string]any{
		"data": []map[string]any{
			{"id": "r1", "agent_id": "ag1", "agent_slug": "viktor", "chat_id": "ch1", "created_at": "2026-06-10T10:00:00Z"},
		},
	}))
	s.OnGet("/api/v1/chats/ch1/messages", clitest.JSONResponse(200, map[string]any{
		"messages": []map[string]any{
			{"role": "user", "content": "please summarise the report"},
			{"role": "assistant", "content": "first part"},
			{"role": "user", "content": "go on"},
			{"role": "assistant", "content": "second part"},
		},
	}))
	s.OnGet("/api/v1/journal", clitest.JSONResponse(200, map[string]any{
		"entries": []map[string]any{
			{"ts": "2026-06-10T10:01:00Z", "entry_type": "run.finished", "severity": "info", "summary": "done"},
		},
	}))
}

func TestExportRunE_WritesBundle(t *testing.T) {
	s := covStubCli9(t)
	covExportStubs(t, s)
	out := filepath.Join(t.TempDir(), "bundle")
	covSetFlagCli9(t, exportCmd, "out", out)

	if err := exportCmd.RunE(exportCmd, []string{"r1"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	// Directory locked to owner-only.
	if info, err := os.Stat(out); err != nil {
		t.Fatalf("stat bundle dir: %v", err)
	} else if info.Mode().Perm() != 0o700 {
		t.Errorf("bundle dir perms = %o, want 700", info.Mode().Perm())
	}

	// run.json carries the metadata.
	var meta map[string]any
	data, err := os.ReadFile(filepath.Join(out, "run.json"))
	if err != nil {
		t.Fatalf("read run.json: %v", err)
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("run.json not JSON: %v", err)
	}
	if meta["run_id"] != "r1" || meta["agent_slug"] != "viktor" || meta["chat_id"] != "ch1" {
		t.Errorf("run.json metadata wrong: %v", meta)
	}

	// prompt.md = first user message; response.md = concatenated assistant text.
	prompt, err := os.ReadFile(filepath.Join(out, "prompt.md"))
	if err != nil {
		t.Fatalf("read prompt.md: %v", err)
	}
	if string(prompt) != "please summarise the report\n" {
		t.Errorf("prompt.md = %q", prompt)
	}
	response, err := os.ReadFile(filepath.Join(out, "response.md"))
	if err != nil {
		t.Fatalf("read response.md: %v", err)
	}
	if string(response) != "first part\n\nsecond part\n" {
		t.Errorf("response.md = %q", response)
	}

	// timeline.txt renders the journal entry.
	timeline, err := os.ReadFile(filepath.Join(out, "timeline.txt"))
	if err != nil {
		t.Fatalf("read timeline.txt: %v", err)
	}
	if !strings.Contains(string(timeline), "[info/run.finished]  done") {
		t.Errorf("timeline.txt = %q", timeline)
	}

	// Artifacts are 0600.
	for _, name := range []string{"run.json", "prompt.md", "response.md", "messages.json", "journal.json", "timeline.txt"} {
		info, err := os.Stat(filepath.Join(out, name))
		if err != nil {
			t.Errorf("missing artifact %s: %v", name, err)
			continue
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s perms = %o, want 600", name, info.Mode().Perm())
		}
	}
}

func TestExportRunE_NoJournalFlagSkipsJournal(t *testing.T) {
	s := covStubCli9(t)
	covExportStubs(t, s)
	out := filepath.Join(t.TempDir(), "bundle")
	covSetFlagCli9(t, exportCmd, "out", out)
	covSetFlagCli9(t, exportCmd, "no-journal", "true")

	if err := exportCmd.RunE(exportCmd, []string{"r1"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "journal.json")); !os.IsNotExist(err) {
		t.Errorf("journal.json should not exist with --no-journal (err=%v)", err)
	}
	if len(s.CallsFor("GET", "/api/v1/journal")) != 0 {
		t.Error("journal endpoint must not be called with --no-journal")
	}
}

func TestExportRunE_MessageFetchFailureIsNonFatal(t *testing.T) {
	s := covStubCli9(t)
	covExportStubs(t, s)
	s.OnGet("/api/v1/chats/ch1/messages", clitest.ErrorResponse(500, "chat down"))
	out := filepath.Join(t.TempDir(), "bundle")
	covSetFlagCli9(t, exportCmd, "out", out)

	if err := exportCmd.RunE(exportCmd, []string{"r1"}); err != nil {
		t.Fatalf("messages failure should be a warning, not an error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "prompt.md")); !os.IsNotExist(err) {
		t.Error("prompt.md should not exist when messages fetch fails")
	}
	// Journal half still lands.
	if _, err := os.Stat(filepath.Join(out, "journal.json")); err != nil {
		t.Errorf("journal.json should still be written: %v", err)
	}
}

func TestExportRunE_RunNotFound(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/runs", clitest.JSONResponse(200, map[string]any{"data": []map[string]any{}}))
	covSetFlagCli9(t, exportCmd, "out", filepath.Join(t.TempDir(), "bundle"))

	err := exportCmd.RunE(exportCmd, []string{"ghost"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected run-not-found; got %v", err)
	}
}

func TestExportRunE_NoAuth(t *testing.T) {
	covSaveState(t)
	cliCfg = &cli.CLIConfig{}
	if err := exportCmd.RunE(exportCmd, []string{"r1"}); err == nil {
		t.Error("expected not-logged-in error")
	}
}

func TestExportRunE_WindowFallbackAndJournalWarn(t *testing.T) {
	s := covStubCli9(t)
	covExportStubs(t, s)
	// Unparsable created_at → runWindowStart errors → 1h fallback warn.
	s.OnGet("/api/v1/runs", clitest.JSONResponse(200, map[string]any{
		"data": []map[string]any{
			{"id": "r1", "agent_id": "ag1", "agent_slug": "viktor", "chat_id": "ch1", "created_at": "garbled"},
		},
	}))
	s.OnGet("/api/v1/journal", clitest.ErrorResponse(500, "journal down"))
	out := filepath.Join(t.TempDir(), "bundle")
	covSetFlagCli9(t, exportCmd, "out", out)

	var err error
	stderr := covCaptureStderrCli9(t, func() {
		err = exportCmd.RunE(exportCmd, []string{"r1"})
	})
	if err != nil {
		t.Fatalf("warnings must not fail the export: %v", err)
	}
	if !strings.Contains(stderr, "could not resolve run window") {
		t.Errorf("window fallback warning missing:\n%s", stderr)
	}
	if !strings.Contains(stderr, "could not fetch journal") {
		t.Errorf("journal warning missing:\n%s", stderr)
	}
	// Bundle still carries the chat half.
	if _, statErr := os.Stat(filepath.Join(out, "prompt.md")); statErr != nil {
		t.Errorf("prompt.md should still exist: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(out, "journal.json")); !os.IsNotExist(statErr) {
		t.Errorf("journal.json should be absent when journal fetch failed (err=%v)", statErr)
	}
}

func TestExportRunE_OutDirCreationFails(t *testing.T) {
	s := covStubCli9(t)
	covExportStubs(t, s)
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	covSetFlagCli9(t, exportCmd, "out", filepath.Join(blocker, "sub"))

	err := exportCmd.RunE(exportCmd, []string{"r1"})
	if err == nil || !strings.Contains(err.Error(), "create ") {
		t.Errorf("expected mkdir error; got %v", err)
	}
}

func TestExportRunE_RunsEndpointError(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/runs", clitest.ErrorResponse(500, "runs down"))
	covSetFlagCli9(t, exportCmd, "out", filepath.Join(t.TempDir(), "bundle"))

	err := exportCmd.RunE(exportCmd, []string{"r1"})
	if err == nil || !strings.Contains(err.Error(), "runs down") {
		t.Errorf("expected runs error; got %v", err)
	}
}

func TestExportFileHelpers_Errors(t *testing.T) {
	t.Parallel()
	if err := exportMkdir("/dev/null/sub"); err == nil {
		t.Error("exportMkdir under a non-directory should fail")
	}
	if err := writeArtifactFile(filepath.Join(t.TempDir(), "missing", "f.txt"), []byte("x")); err == nil {
		t.Error("writeArtifactFile into a missing directory should fail")
	}
	if err := writeJSONFile(filepath.Join(t.TempDir(), "f.json"), make(chan int)); err == nil || !strings.Contains(err.Error(), "marshal") {
		t.Errorf("writeJSONFile of an unmarshalable value should fail; got %v", err)
	}
}

func TestSplitPromptResponse(t *testing.T) {
	t.Parallel()
	messages := []map[string]any{
		{"role": "system", "content": "ignored"},
		{"role": "Human", "content": "the prompt"},
		{"role": "assistant", "content": "part one"},
		{"role": "user", "content": "follow-up is not the prompt"},
		{"role": "model", "content": "part two"},
	}
	prompt, response := splitPromptResponse(messages)
	if prompt != "the prompt" {
		t.Errorf("prompt = %q", prompt)
	}
	if response != "part one\n\npart two" {
		t.Errorf("response = %q", response)
	}

	prompt, response = splitPromptResponse(nil)
	if prompt != "" || response != "" {
		t.Errorf("empty input should produce empty outputs, got %q / %q", prompt, response)
	}
}

func TestFormatJournalTimeline(t *testing.T) {
	t.Parallel()
	entries := []map[string]any{
		{"ts": "2026-06-10T10:01:00Z", "entry_type": "a.b", "severity": "warn", "summary": "first"},
		{"ts": "garbled", "entry_type": "c.d", "severity": "info", "summary": "second"},
	}
	got := formatJournalTimeline(entries)
	if !strings.Contains(got, "2026-06-10 10:01:00  [warn/a.b]  first") {
		t.Errorf("formatted line missing: %q", got)
	}
	// Unparsable timestamps pass through verbatim.
	if !strings.Contains(got, "garbled  [info/c.d]  second") {
		t.Errorf("raw timestamp line missing: %q", got)
	}
}

func TestFetchAllMessages(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/chats/chx/messages", clitest.JSONResponse(200, map[string]any{
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	}))
	client := cli.NewClient(s.URL(), "tok", covWSCli9)

	msgs, err := fetchAllMessages(client, "chx")
	if err != nil {
		t.Fatalf("fetchAllMessages: %v", err)
	}
	if len(msgs) != 1 || msgs[0]["content"] != "hi" {
		t.Errorf("messages = %v", msgs)
	}
	calls := s.CallsFor("GET", "/api/v1/chats/chx/messages")
	if len(calls) != 1 || !strings.Contains(calls[0].Query, "limit=500") {
		t.Errorf("expected limit=500 query: %+v", calls)
	}

	s.OnGet("/api/v1/chats/chx/messages", clitest.ErrorResponse(404, "gone"))
	if _, err := fetchAllMessages(client, "chx"); err == nil {
		t.Error("404 should surface as error")
	}
}
