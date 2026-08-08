package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/conversation"
)

// #1835 — a routine's agent_run step persisted the rendered prompt and then
// threw the agent's answer away. Invisible until #1823/#1831 made those runs
// watchable: a browser open on the step's chat sees the text stream in and
// finds it gone on reload.
//
// These tests are the reload. They drive a real RunStep and then read the
// conversation store back the way the history API does, which is the only
// vantage point from which the bug is visible at all — the step result still
// carries the text, so every existing assertion passed while history was empty.

// replyStream is an agent turn with shape: text, a tool call, more text. The
// tool_use line matters — a persist that flattens the turn to Content only
// renders on reload as one undifferentiated blob, losing the tool activity a
// live watcher saw.
const replyStream = "planning the change\n" +
	`{"type":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"file_path":"main.go"}}]}` + "\n" +
	"done — 3 services green\n" +
	`{"type":"result","subtype":"success","total_cost_usd":0.5,"usage":{"input_tokens":10,"output_tokens":20}}` + "\n"

// runStepAndReadHistory runs one step and returns the step's chat id plus the
// conversation history as a reloading client would fetch it.
func runStepAndReadHistory(t *testing.T, r *OrchestratorRunner, resolver *orchCovResolver) (string, []conversation.Message) {
	t.Helper()
	_, err := r.RunStep(context.Background(), AgentStepRequest{
		WorkspaceID:  "ws_cov",
		AuthorCrewID: "crew_cov",
		AgentSlug:    "cov-agent",
		Prompt:       "summarize the day",
		TimeoutSec:   30,
		PipelineID:   "pln_cov",
		StepID:       "s1",
	})
	if err != nil {
		t.Fatalf("RunStep: %v", err)
	}
	if len(resolver.createChatCalls) != 1 {
		t.Fatalf("CreateChat calls = %d, want 1", len(resolver.createChatCalls))
	}
	chatID := resolver.createChatCalls[0].ChatID
	msgs, err := r.convStore.Read(context.Background(), chatID, 0, 100)
	if err != nil {
		t.Fatalf("read conversation history: %v", err)
	}
	return chatID, msgs
}

func TestOrchestratorRunner_RunStep_PersistsAssistantReplyForReload(t *testing.T) {
	container := &orchCovContainer{agentStream: replyStream}
	resolver := &orchCovResolver{info: covChatInfo()}
	r := newOrchRunnerRig(t, container, resolver)

	_, msgs := runStepAndReadHistory(t, r, resolver)

	if len(msgs) != 2 {
		t.Fatalf("history has %d messages, want 2 (the prompt and the reply); roles=%v",
			len(msgs), rolesOf(msgs))
	}
	if msgs[0].Role != conversation.RoleUser {
		t.Errorf("first message role = %q, want user", msgs[0].Role)
	}
	reply := msgs[1]
	if reply.Role != conversation.RoleAssistant {
		t.Fatalf("second message role = %q, want assistant", reply.Role)
	}
	if !strings.Contains(reply.Content, "done — 3 services green") {
		t.Errorf("reply content = %q, want the agent's streamed text", reply.Content)
	}
	// agent_id is the isolation boundary conversation search filters on
	// (conversation.Store.Search). A reply persisted without it is invisible to
	// the agent's own recall.
	if reply.AgentID != "agent_cov" {
		t.Errorf("reply agent_id = %q, want agent_cov", reply.AgentID)
	}

	// Parts, not just flattened text: reload must render the turn the way it
	// streamed — text, then the tool call, then the text that followed it.
	var kinds []string
	for _, p := range reply.Parts {
		kinds = append(kinds, p.Type)
	}
	want := []string{"text", "tool_call", "text"}
	if len(kinds) != len(want) {
		t.Fatalf("reply parts = %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("reply parts = %v, want %v", kinds, want)
		}
	}
	if reply.Parts[1].ToolName != "Read" {
		t.Errorf("tool_call part tool_name = %q, want Read", reply.Parts[1].ToolName)
	}
}

// The chat's derived message count must agree with what is in the chat. Before
// #1835 the step incremented nothing at all, so a routine chat reported "0
// messages" while its history held one — and would have reported 0 while
// holding two.
func TestOrchestratorRunner_RunStep_CountsThePersistedPair(t *testing.T) {
	container := &orchCovContainer{agentStream: replyStream}
	resolver := &orchCovResolver{info: covChatInfo()}
	r := newOrchRunnerRig(t, container, resolver)

	chatID, msgs := runStepAndReadHistory(t, r, resolver)

	if got := resolver.messageCounts[chatID]; got != len(msgs) {
		t.Errorf("message count for %s = %d, want %d (one per persisted message)",
			chatID, got, len(msgs))
	}
}

// A step whose run fails after the agent already said something must keep what
// it said. The step result reports that partial output to the executor either
// way (#1426); before #1835 the chat did not, so the failure a user went to the
// chat to understand rendered as an empty transcript.
//
// An in-band failure (exit 0, CLI's own terminal event says the turn failed) is
// the failure shape that actually carries text — the same one the WebSocket
// path persists via persistInBandFailureTurn. A non-zero exit is diagnosed and
// surfaced by the orchestrator before any text reaches the handler, so there is
// nothing to persist on that path.
func TestOrchestratorRunner_RunStep_PersistsPartialReplyOnFailure(t *testing.T) {
	container := &orchCovContainer{
		agentStream: "partial answer before the crash\n" +
			`{"type":"result","subtype":"error_during_execution","is_error":true}` + "\n",
	}
	resolver := &orchCovResolver{info: covChatInfo()}
	r := newOrchRunnerRig(t, container, resolver)

	res, err := r.RunStep(context.Background(), AgentStepRequest{
		WorkspaceID: "ws_cov", AuthorCrewID: "crew_cov", AgentSlug: "cov-agent",
		Prompt: "do work", TimeoutSec: 30, PipelineID: "pln_cov", StepID: "s1",
	})
	if err == nil {
		t.Fatalf("expected the failing run to surface an error, got nil")
	}
	if !strings.Contains(res.Output, "partial answer") {
		t.Fatalf("step result lost the partial output: %q", res.Output)
	}
	chatID := resolver.createChatCalls[0].ChatID
	msgs, rerr := r.convStore.Read(context.Background(), chatID, 0, 100)
	if rerr != nil {
		t.Fatalf("read conversation history: %v", rerr)
	}
	if len(msgs) != 2 {
		t.Fatalf("history has %d messages, want 2; roles=%v", len(msgs), rolesOf(msgs))
	}
	if !strings.Contains(msgs[1].Content, "partial answer") {
		t.Errorf("persisted reply = %q, want the partial output", msgs[1].Content)
	}
}

// A run cancelled mid-turn leaves the step context already done, and
// conversation.Store.Append refuses a done context. Without an explicit detach
// the partial reply — the text the live watcher just saw — is dropped on
// exactly the path where losing it is most visible.
//
// Asserted on the helper rather than through a cancelled RunStep on purpose:
// racing a real cancel against a streaming reader needs a wall-clock sleep to
// synchronise, and a timing-dependent test in internal/pipeline is how #1597
// flakes get made.
func TestOrchestratorRunner_PersistsUnderACancelledContext(t *testing.T) {
	container := &orchCovContainer{}
	resolver := &orchCovResolver{info: covChatInfo()}
	r := newOrchRunnerRig(t, container, resolver)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if !r.persistAssistantTurn(ctx, "chat_cancelled", "agent_cov", "text the watcher saw", nil) {
		t.Fatal("persistAssistantTurn reported no write under a cancelled context")
	}

	msgs, err := r.convStore.Read(context.Background(), "chat_cancelled", 0, 10)
	if err != nil {
		t.Fatalf("read conversation history: %v", err)
	}
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "text the watcher saw") {
		t.Fatalf("cancelled-run reply not persisted: %+v", msgs)
	}
}

// An empty turn writes nothing: a run that produced neither text nor parts must
// not leave an empty assistant bubble in the transcript, nor bump the chat's
// message count and last_activity_at as if it had said something.
func TestOrchestratorRunner_EmptyReplyWritesNothing(t *testing.T) {
	container := &orchCovContainer{}
	resolver := &orchCovResolver{info: covChatInfo()}
	r := newOrchRunnerRig(t, container, resolver)

	if r.persistAssistantTurn(context.Background(), "chat_silent", "agent_cov", "", nil) {
		t.Fatal("empty turn reported a write")
	}
	r.recordChatTurn(context.Background(), "chat_silent", "agent_cov", "", nil, false)
	if got := resolver.messageCounts["chat_silent"]; got != 0 {
		t.Errorf("message count = %d, want 0 (nothing was written)", got)
	}

	// A step that stored its prompt and then got nothing back counts ONE, not
	// the bridge's coupled pair — a chat claiming two messages while holding
	// one is the same class of lie this issue is about.
	r.recordChatTurn(context.Background(), "chat_prompt_only", "agent_cov", "", nil, true)
	if got := resolver.messageCounts["chat_prompt_only"]; got != 1 {
		t.Errorf("message count = %d, want 1 (only the prompt landed)", got)
	}
}

func rolesOf(msgs []conversation.Message) []conversation.Role {
	out := make([]conversation.Role, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Role)
	}
	return out
}
