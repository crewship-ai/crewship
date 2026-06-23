package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// saveAskFlags snapshots the ask command flags recap mutates so a
// failed recap doesn't leak prompt/quiet state into later ask tests.
func saveAskFlags(t *testing.T) {
	t.Helper()
	for _, name := range []string{"prompt", "quiet", "agent"} {
		fl := askCmd.Flags().Lookup(name)
		if fl == nil {
			t.Fatalf("ask flag --%s missing", name)
		}
		orig := fl.Value.String()
		origChanged := fl.Changed
		t.Cleanup(func() {
			_ = fl.Value.Set(orig)
			fl.Changed = origChanged
		})
	}
}

func TestRecapRunE_NoAuth(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	err := recapCmd.RunE(recapCmd, []string{"c_1"})
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in, got %v", err)
	}
}

func TestRecapRunE_EmptyChatErrors(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/chats/c_empty/messages", clitest.JSONResponse(200, map[string]any{
		"messages": []map[string]any{},
	}))
	covSetupCli10(t, s.URL())
	saveAskFlags(t)

	err := recapCmd.RunE(recapCmd, []string{"c_empty"})
	if err == nil || !strings.Contains(err.Error(), "has no messages") {
		t.Errorf("expected empty-chat error, got %v", err)
	}
}

func TestRecapRunE_TranscriptFetchError(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/chats/c_gone/messages", clitest.ErrorResponse(404, "chat not found"))
	covSetupCli10(t, s.URL())
	saveAskFlags(t)

	err := recapCmd.RunE(recapCmd, []string{"c_gone"})
	if err == nil || !strings.Contains(err.Error(), "chat not found") {
		t.Errorf("expected 404 surfaced, got %v", err)
	}
}

// TestRecapRunE_BuildsPromptAndDispatchesToAsk drives recap through the
// full transcript-build path. ask's RunE then fails fast in this
// non-TTY test environment ("no default agent set"), which both proves
// the dispatch happened and keeps the test offline.
func TestRecapRunE_BuildsPromptAndDispatchesToAsk(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	long := strings.Repeat("x", 900) // exercises the 800-char truncation
	s.OnGet("/api/v1/chats/c_full/messages", clitest.JSONResponse(200, map[string]any{
		"messages": []map[string]any{
			{"role": "USER", "content": "summarise the deploy"},
			{"role": "", "content": long},
		},
	}))
	covSetupCli10(t, s.URL())
	saveAskFlags(t)
	setFlagCovCli10(t, recapCmd, "bullets", "0") // <=0 falls back to 8

	_, err := captureStderrCov(t, func() error {
		return recapCmd.RunE(recapCmd, []string{"c_full"})
	})
	if err == nil || !strings.Contains(err.Error(), "no default agent set") {
		t.Fatalf("expected ask dispatch to fail on default-agent resolution, got %v", err)
	}

	// The transcript prompt recap built must be parked on ask's flags.
	prompt := askCmd.Flags().Lookup("prompt").Value.String()
	if !strings.Contains(prompt, "Transcript (2 turns):") {
		t.Errorf("transcript header missing:\n%s", prompt)
	}
	if !strings.Contains(prompt, "[user] summarise the deploy") {
		t.Errorf("user turn missing:\n%s", prompt)
	}
	if !strings.Contains(prompt, "[unknown] "+strings.Repeat("x", 800)+"…") {
		t.Errorf("long unknown-role turn not truncated to 800 chars:\n%s", prompt[:400])
	}
	if !strings.Contains(prompt, "up to 8 bullets") {
		t.Errorf("bullets fallback (8) missing:\n%s", prompt[:400])
	}
	if got := askCmd.Flags().Lookup("quiet").Value.String(); got != "true" {
		t.Errorf("recap must set ask --quiet, got %q", got)
	}
}

func TestRecapRunE_AgentFlagForwarded(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/chats/c_a/messages", clitest.JSONResponse(200, map[string]any{
		"messages": []map[string]any{{"role": "USER", "content": "hi"}},
	}))
	// ask will resolve the explicit agent against /api/v1/agents — make
	// the lookup fail with an empty workspace so RunE errors fast.
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{}))
	covSetupCli10(t, s.URL())
	saveAskFlags(t)
	setFlagCovCli10(t, recapCmd, "agent", "viktor")

	err := recapCmd.RunE(recapCmd, []string{"c_a"})
	if err == nil || !strings.Contains(err.Error(), "agent not found: viktor") {
		t.Fatalf("expected ask to resolve forwarded agent, got %v", err)
	}
	if got := askCmd.Flags().Lookup("agent").Value.String(); got != "viktor" {
		t.Errorf("agent flag not forwarded to ask: %q", got)
	}
}
