package main

// Coverage tests for cmd_diff.go — the run-vs-run RunE, the
// lastAssistantText extractor, and the line-diff printers.

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestLastAssistantText(t *testing.T) {
	msgs := []map[string]any{
		{"role": "user", "content": "question"},
		{"role": "assistant", "content": "first answer"},
		{"role": "user", "content": "follow-up"},
		{"role": "ASSISTANT", "content": "final answer"},
	}
	if got := lastAssistantText(msgs, false); got != "final answer" {
		t.Errorf("lastAssistantText: got %q", got)
	}
	if got := lastAssistantText(nil, false); got != "" {
		t.Errorf("empty messages: got %q", got)
	}
	if got := lastAssistantText([]map[string]any{{"role": "user", "content": "x"}}, false); got != "" {
		t.Errorf("no assistant message: got %q", got)
	}

	long := strings.Repeat("a", 2000)
	msgsLong := []map[string]any{{"role": "assistant", "content": long}}
	got := lastAssistantText(msgsLong, false)
	if len(got) != 1024+len("…") || !strings.HasSuffix(got, "…") {
		t.Errorf("truncation failed: len=%d", len(got))
	}
	if got := lastAssistantText(msgsLong, true); got != long {
		t.Errorf("full=true must not truncate; got len=%d", len(got))
	}
}

func TestSafeStr(t *testing.T) {
	if got := safeStr(nil); got != "" {
		t.Errorf("safeStr(nil) = %q", got)
	}
	s := "v"
	if got := safeStr(&s); got != "v" {
		t.Errorf("safeStr(&v) = %q", got)
	}
}

func TestPrintLineDiff(t *testing.T) {
	out := covCaptureStdoutCli8(t, func() {
		printLineDiff("same\nold-only\nshared", "same\nnew-only\nshared\nextra-b")
	})
	if !strings.Contains(out, "  same") {
		t.Errorf("equal line missing:\n%s", out)
	}
	if !strings.Contains(out, "- old-only") || !strings.Contains(out, "+ new-only") {
		t.Errorf("changed lines missing markers:\n%s", out)
	}
	if !strings.Contains(out, "+ extra-b") {
		t.Errorf("extra B line missing:\n%s", out)
	}
}

func TestPrintRunDiff(t *testing.T) {
	slugA, started := "viktor", "2026-06-10T10:00:00Z"
	a := &cli.RunDetail{ID: "r_a", Status: "COMPLETED", AgentSlug: &slugA, StartedAt: &started}
	b := &cli.RunDetail{ID: "r_b", Status: "FAILED"}
	out := covCaptureStdoutCli8(t, func() {
		printRunDiff("r_a", "r_b", a, b, "hello", "world")
	})
	for _, want := range []string{"Run A", "r_a", "Run B", "r_b", "viktor", "COMPLETED", "FAILED", "Output diff", "- hello", "+ world"} {
		if !strings.Contains(out, want) {
			t.Errorf("printRunDiff missing %q:\n%s", want, out)
		}
	}
	// Nil pointers render as "-" placeholders.
	if !strings.Contains(out, "agent  : -") {
		t.Errorf("nil agent should render '-':\n%s", out)
	}

	// Cover the CANCELLED colour branch too.
	c := &cli.RunDetail{ID: "r_c", Status: "CANCELLED"}
	out = covCaptureStdoutCli8(t, func() {
		printRunDiff("r_a", "r_c", a, c, "", "")
	})
	if !strings.Contains(out, "CANCELLED") {
		t.Errorf("cancelled status missing:\n%s", out)
	}
}

func covDiffStub(t *testing.T) *clitest.StubServer {
	t.Helper()
	stub := clitest.NewStubServer()
	t.Cleanup(stub.Close)
	covSetupCli8(t, stub.URL())
	slugA, slugB := "viktor", "eva"
	chatA, chatB := "chat-a", "chat-b"
	stub.OnGet("/api/v1/runs/r_a", clitest.JSONResponse(200, map[string]any{
		"id": "r_a", "status": "COMPLETED", "agent_slug": slugA, "chat_id": chatA,
	}))
	stub.OnGet("/api/v1/runs/r_b", clitest.JSONResponse(200, map[string]any{
		"id": "r_b", "status": "FAILED", "agent_slug": slugB, "chat_id": chatB,
	}))
	stub.OnGet("/api/v1/chats/chat-a/messages", clitest.JSONResponse(200, map[string]any{
		"messages": []map[string]any{{"role": "assistant", "content": "answer A"}},
	}))
	stub.OnGet("/api/v1/chats/chat-b/messages", clitest.JSONResponse(200, map[string]any{
		"messages": []map[string]any{{"role": "assistant", "content": "answer B"}},
	}))
	return stub
}

func TestDiffRunE_TableHappyPath(t *testing.T) {
	covDiffStub(t)
	diffCmd.SetContext(context.Background())

	out := covCaptureStdoutCli8(t, func() {
		if err := diffCmd.RunE(diffCmd, []string{"r_a", "r_b"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	for _, want := range []string{"Run A", "Run B", "viktor", "eva", "- answer A", "+ answer B"} {
		if !strings.Contains(out, want) {
			t.Errorf("diff output missing %q:\n%s", want, out)
		}
	}
}

func TestDiffRunE_JSONFormat(t *testing.T) {
	covDiffStub(t)
	cliCfg.Format = "json"
	diffCmd.SetContext(context.Background())

	out := covCaptureStdoutCli8(t, func() {
		if err := diffCmd.RunE(diffCmd, []string{"r_a", "r_b"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	for _, want := range []string{`"a_text"`, "answer A", `"b_text"`, "answer B"} {
		if !strings.Contains(out, want) {
			t.Errorf("json diff missing %q:\n%s", want, out)
		}
	}
}

func TestDiffRunE_RunAFetchError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/runs/r_a", clitest.ErrorResponse(404, "run not found"))
	stub.OnGet("/api/v1/runs/r_b", clitest.JSONResponse(200, map[string]any{
		"id": "r_b", "status": "COMPLETED",
	}))
	diffCmd.SetContext(context.Background())

	err := diffCmd.RunE(diffCmd, []string{"r_a", "r_b"})
	if err == nil || !strings.Contains(err.Error(), "run-a r_a") {
		t.Errorf("expected run-a error; got %v", err)
	}
}

func TestDiffRunE_RunBFetchError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/runs/r_a", clitest.JSONResponse(200, map[string]any{
		"id": "r_a", "status": "COMPLETED",
	}))
	stub.OnGet("/api/v1/runs/r_b", clitest.ErrorResponse(404, "run not found"))
	diffCmd.SetContext(context.Background())

	err := diffCmd.RunE(diffCmd, []string{"r_a", "r_b"})
	if err == nil || !strings.Contains(err.Error(), "run-b r_b") {
		t.Errorf("expected run-b error; got %v", err)
	}
}

func TestPrintRunDiff_UnknownStatusUncoloured(t *testing.T) {
	a := &cli.RunDetail{ID: "r_a", Status: "RUNNING"}
	b := &cli.RunDetail{ID: "r_b", Status: "TIMEOUT"}
	out := covCaptureStdoutCli8(t, func() {
		printRunDiff("r_a", "r_b", a, b, "", "")
	})
	// RUNNING is not a recognised terminal state → printed without colour
	// codes immediately around it.
	if !strings.Contains(out, "status : RUNNING\n") {
		t.Errorf("unknown status should be uncoloured:\n%s", out)
	}
	if !strings.Contains(out, "TIMEOUT") {
		t.Errorf("timeout status missing:\n%s", out)
	}
}

func TestDiffRunE_NoAuth(t *testing.T) {
	covSetupCli8(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	err := diffCmd.RunE(diffCmd, []string{"r_a", "r_b"})
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in; got %v", err)
	}
}
