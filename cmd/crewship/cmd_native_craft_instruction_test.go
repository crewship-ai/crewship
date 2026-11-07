package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const markedInstructionText = "Confidential merger instruction"

func markedChatMessages(extra ...map[string]any) map[string]any {
	messages := []map[string]any{
		{"role": "user", "content": "ordinary question", "author_user_id": "u_me", "agent_id": "agent-1"},
		{"role": "assistant", "content": "ordinary answer", "agent_id": "agent-1"},
		{"role": "user", "content": markedInstructionText, "author_user_id": "u_me", "agent_id": "agent-1", "native_craft_instruction": true},
	}
	return map[string]any{"messages": append(messages, extra...)}
}

// #5009: retry is a one-click resend. It would run the instruction as an
// ordinary turn, so it stops, and it does not fall back to the turn before.
func TestChatRetryRefusesNativeCraftInstruction(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/chats/c_marked/messages", clitest.JSONResponse(200, markedChatMessages()))
	s.OnGet("/api/v1/auth/cli-token/validate", clitest.JSONResponse(200, map[string]any{"user_id": "u_me"}))
	covSetupCli10(t, s.URL())

	err := chatRetryCmd.RunE(chatRetryCmd, []string{"c_marked"})
	if err == nil || !strings.Contains(err.Error(), "native Craft instruction") {
		t.Fatalf("retry of a marked message: %v", err)
	}
}

// #5009: recap sends the transcript to an agent as an ordinary prompt.
func TestRecapLeavesOutNativeCraftInstruction(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/chats/c_marked/messages", clitest.JSONResponse(200, markedChatMessages()))
	covSetupCli10(t, s.URL())
	saveAskFlags(t)

	_ = recapCmd.RunE(recapCmd, []string{"c_marked"})
	prompt := askCmd.Flags().Lookup("prompt").Value.String()
	if strings.Contains(prompt, markedInstructionText) {
		t.Fatalf("the recap prompt carries the instruction:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Transcript (2 turns)") || !strings.Contains(prompt, "ordinary answer") {
		t.Fatalf("the recap prompt lost the ordinary turns:\n%s", prompt)
	}
}

func TestRecapOfOnlyAnInstructionHasNoMessages(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/chats/c_only/messages", clitest.JSONResponse(200, map[string]any{"messages": []map[string]any{
		{"role": "user", "content": markedInstructionText, "native_craft_instruction": true},
	}}))
	covSetupCli10(t, s.URL())
	saveAskFlags(t)

	err := recapCmd.RunE(recapCmd, []string{"c_only"})
	if err == nil || !strings.Contains(err.Error(), "has no messages") {
		t.Fatalf("recap of an instruction-only chat: %v", err)
	}
}

// An ordinary run in a chat that opens with a saved instruction: the first user
// message, which these commands recover, is the instruction.
func stubInstructionFirstRun(t *testing.T) *clitest.StubServer {
	t.Helper()
	s := clitest.NewStubServer()
	t.Cleanup(s.Close)
	agentID, slug, chatID := "cagent7890abcdefghijklm", "viktor", "c_marked"
	stubRunsList(s, "r_1", agentID, &slug, &chatID)
	s.OnGet("/api/v1/chats/c_marked/messages", clitest.JSONResponse(200, map[string]any{"messages": []map[string]any{
		{"role": "user", "content": markedInstructionText, "native_craft_instruction": true},
		{"role": "user", "content": "ordinary question"},
		{"role": "assistant", "content": "ordinary answer"},
	}}))
	s.OnPost("/api/v1/agents/"+agentID+"/chats", clitest.JSONResponse(201, map[string]string{"id": "c_new"}))
	s.OnGet("/api/v1/ws-token", clitest.JSONResponse(200, map[string]string{"token": "ws-tok"}))
	return s
}

// #5009: top-level retry recovers the first user message. It refuses that
// message when marked, in both modes, and does not fall back to a later one.
func TestRetryRefusesNativeCraftInstruction(t *testing.T) {
	for _, mode := range []string{"new chat", "continue"} {
		t.Run(mode, func(t *testing.T) {
			s := stubInstructionFirstRun(t)
			covSetupCli10(t, s.URL())
			if mode == "continue" {
				setFlagCovCli10(t, retryCmd, "continue", "true")
			}
			setFlagCovCli10(t, retryCmd, "no-stream", "true")
			err := retryCmd.RunE(retryCmd, []string{"r_1"})
			if err == nil || !strings.Contains(err.Error(), "native Craft instruction") {
				t.Fatalf("retry of a run whose prompt is marked: %v", err)
			}
		})
	}
}

// copy-prompt exists to re-issue what it recovers.
func TestCopyPromptRefusesNativeCraftInstruction(t *testing.T) {
	s := stubInstructionFirstRun(t)
	covSetupCli10(t, s.URL())
	err := copyPromptCmd.RunE(copyPromptCmd, []string{"r_1"})
	if err == nil || !strings.Contains(err.Error(), "native Craft instruction") {
		t.Fatalf("copy-prompt of a marked message: %v", err)
	}
}

// A preview names no prompt rather than attribute the instruction to a run.
func TestHistoryPreviewLeavesOutNativeCraftInstruction(t *testing.T) {
	s := stubInstructionFirstRun(t)
	got := fetchFirstUserPrompt(cli.NewClient(s.URL(), "t", ""), "c_marked")
	if got != "" {
		t.Fatalf("preview = %q", got)
	}
}

// prompt.md is the run's prompt at a glance; the instruction did not start it.
func TestExportPromptLeavesOutNativeCraftInstruction(t *testing.T) {
	prompt, _ := splitPromptResponse([]map[string]any{
		{"role": "user", "content": markedInstructionText, "native_craft_instruction": true},
		{"role": "user", "content": "ordinary question"},
		{"role": "assistant", "content": "ordinary answer"},
	})
	if prompt != "ordinary question" {
		t.Fatalf("prompt = %q", prompt)
	}
}
