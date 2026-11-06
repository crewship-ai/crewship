package main

import (
	"strings"
	"testing"

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
