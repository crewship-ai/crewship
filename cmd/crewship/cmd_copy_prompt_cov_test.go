package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func stubCopyPromptEndpoints(s *clitest.StubServer, prompt string) {
	chat := "c_orig"
	s.OnGet("/api/v1/runs", clitest.JSONResponse(200, map[string]any{
		"data": []map[string]any{{
			"id": "r_1", "agent_id": "cagent7890abcdefghijklm", "chat_id": &chat,
		}},
	}))
	s.OnGet("/api/v1/chats/c_orig/messages", clitest.JSONResponse(200, map[string]any{
		"messages": []map[string]string{
			{"role": "USER", "content": prompt},
			{"role": "ASSISTANT", "content": "done"},
		},
	}))
}

func TestCopyPromptRunE_PrintsToStdoutWithNewline(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	stubCopyPromptEndpoints(s, "review this diff")
	covSetupCli10(t, s.URL())

	out, err := captureStdoutCovCli10(t, func() error {
		return copyPromptCmd.RunE(copyPromptCmd, []string{"r_1"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if out != "review this diff\n" {
		t.Errorf("stdout = %q, want prompt + trailing newline", out)
	}
}

func TestCopyPromptRunE_PreservesExistingTrailingNewline(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	stubCopyPromptEndpoints(s, "multi\nline\n")
	covSetupCli10(t, s.URL())

	out, err := captureStdoutCovCli10(t, func() error {
		return copyPromptCmd.RunE(copyPromptCmd, []string{"r_1"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if out != "multi\nline\n" {
		t.Errorf("stdout = %q, must not double the newline", out)
	}
}

func TestCopyPromptRunE_NoChatID(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/runs", clitest.JSONResponse(200, map[string]any{
		"data": []map[string]any{{"id": "r_1", "agent_id": "ca1", "chat_id": nil}},
	}))
	covSetupCli10(t, s.URL())

	err := copyPromptCmd.RunE(copyPromptCmd, []string{"r_1"})
	if err == nil || !strings.Contains(err.Error(), "has no chat_id") {
		t.Errorf("expected chat-missing error, got %v", err)
	}
}

func TestCopyPromptRunE_PromptUnrecoverable(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	chat := "c_orig"
	s.OnGet("/api/v1/runs", clitest.JSONResponse(200, map[string]any{
		"data": []map[string]any{{"id": "r_1", "agent_id": "ca1", "chat_id": &chat}},
	}))
	s.OnGet("/api/v1/chats/c_orig/messages", clitest.JSONResponse(200, map[string]any{
		"messages": []map[string]string{{"role": "ASSISTANT", "content": "no user turns"}},
	}))
	covSetupCli10(t, s.URL())

	err := copyPromptCmd.RunE(copyPromptCmd, []string{"r_1"})
	if err == nil || !strings.Contains(err.Error(), "could not recover prompt") {
		t.Errorf("expected recovery failure, got %v", err)
	}
}

func TestCopyPromptRunE_NoAuth(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	err := copyPromptCmd.RunE(copyPromptCmd, []string{"r_1"})
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in, got %v", err)
	}
}

func TestWriteClipboard_NoHelperAvailable(t *testing.T) {
	// Empty PATH → every clipboard helper lookup fails → clear error.
	t.Setenv("PATH", "")
	err := writeClipboard("data")
	if err == nil || !strings.Contains(err.Error(), "no clipboard helper found") {
		t.Errorf("expected helper-missing error, got %v", err)
	}
}

func TestCopyPromptRunE_RunNotFound(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/runs", clitest.JSONResponse(200, map[string]any{"data": []map[string]any{}}))
	covSetupCli10(t, s.URL())
	err := copyPromptCmd.RunE(copyPromptCmd, []string{"r_missing"})
	if err == nil || !strings.Contains(err.Error(), "not found in last 100 runs") {
		t.Errorf("expected fetchRun error propagated, got %v", err)
	}
}

// TestWriteClipboard_FakeHelperReceivesData installs a fake `pbcopy`
// shell script on PATH that dumps stdin into a file — proving
// writeClipboard pipes the prompt into the first available helper.
func TestWriteClipboard_FakeHelperReceivesData(t *testing.T) {
	dir := t.TempDir()
	sink := filepath.Join(dir, "clipboard.txt")
	script := "#!/bin/sh\n/bin/cat > \"" + sink + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, "pbcopy"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake pbcopy: %v", err)
	}
	t.Setenv("PATH", dir)

	if err := writeClipboard("clipboard payload"); err != nil {
		t.Fatalf("writeClipboard: %v", err)
	}
	b, err := os.ReadFile(sink)
	if err != nil {
		t.Fatalf("read sink: %v", err)
	}
	if string(b) != "clipboard payload" {
		t.Errorf("helper received %q, want %q", b, "clipboard payload")
	}
}

func TestCopyPromptRunE_ClipboardSuccessBanner(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	stubCopyPromptEndpoints(s, "copy me")
	covSetupCli10(t, s.URL())

	dir := t.TempDir()
	script := "#!/bin/sh\n/bin/cat > /dev/null\n"
	if err := os.WriteFile(filepath.Join(dir, "pbcopy"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake pbcopy: %v", err)
	}
	t.Setenv("PATH", dir)
	setFlagCovCli10(t, copyPromptCmd, "clipboard", "true")

	stderr, err := captureStderrCov(t, func() error {
		return copyPromptCmd.RunE(copyPromptCmd, []string{"r_1"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(stderr, "[copied 7 chars to clipboard]") {
		t.Errorf("clipboard banner missing: %q", stderr)
	}
}

func TestCopyPromptRunE_ClipboardFlagSurfacesHelperError(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	stubCopyPromptEndpoints(s, "copy me")
	covSetupCli10(t, s.URL())
	t.Setenv("PATH", "")
	setFlagCovCli10(t, copyPromptCmd, "clipboard", "true")

	err := copyPromptCmd.RunE(copyPromptCmd, []string{"r_1"})
	if err == nil || !strings.Contains(err.Error(), "no clipboard helper found") {
		t.Errorf("expected clipboard error propagated, got %v", err)
	}
}
