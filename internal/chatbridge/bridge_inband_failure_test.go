package chatbridge

// An in-band failure — the agent CLI exits 0 and then reports in its own result
// event that the turn failed — is now recorded FAILED instead of COMPLETED.
// That is the correct status, but on its own it would have made the chat WORSE:
// the FAILED branch streams an error event and returns, skipping the persisted
// assistant turn, so a reload would show the user's message with nothing after
// it. Before, the status was wrong but the text was at least visible.
//
// These tests pin both halves: the run is FAILED *and* the turn is persisted,
// mirroring the #545 zero-output branch (see bridge_silence_test.go).

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/ws"
)

// A refusal is the common shape: the agent DID speak, then flagged the turn as
// failed. Its words must survive into the transcript.
func TestHandleChatMessageInBandFailurePersistsAgentText(t *testing.T) {
	const refusal = "I cannot help with that request."
	resolver := &capResolver{info: baseInfo()}
	ctr := &scriptedContainer{
		agentOutput: `{"type":"stream_event","event":{"type":"content_block_delta",` +
			`"delta":{"type":"text_delta","text":"` + refusal + `"}}}` + "\n" +
			`{"type":"result","subtype":"error_during_execution","is_error":true,` +
			`"result":"` + refusal + `"}` + "\n",
		exitCode: 0, // the whole point: a clean exit with a failed turn
	}
	b := testBridgeWithContainer(t, resolver, ctr)

	var events []ws.ChatEvent
	err := b.HandleChatMessage(context.Background(), "user-1", "sess-inband", "do a bad thing", func(e ws.ChatEvent) {
		events = append(events, e)
	})
	if err == nil {
		t.Fatal("expected HandleChatMessage to return the run error for an in-band failure")
	}

	// 1. Live viewers get the error, quoting the CLI's own message.
	var errEvt *ws.ChatEvent
	for i := range events {
		if events[i].Type == "error" {
			errEvt = &events[i]
		}
	}
	if errEvt == nil {
		t.Fatalf("expected an error event, got %+v", events)
	}
	if !strings.Contains(errEvt.Content, refusal) {
		t.Errorf("error copy = %q, want it to quote the agent's message", errEvt.Content)
	}

	// 2. The run is FAILED, not COMPLETED.
	if len(resolver.runUpdates) != 1 {
		t.Fatalf("runUpdates = %+v, want exactly one", resolver.runUpdates)
	}
	if got := resolver.runUpdates[0].status; got != "FAILED" {
		t.Errorf("run status = %q, want FAILED", got)
	}

	// 3. A later reload still shows what the agent said AND why the run failed.
	msgs, rerr := b.convStore.Read(context.Background(), "sess-inband", 0, 0)
	if rerr != nil {
		t.Fatalf("read conversation: %v", rerr)
	}
	if len(msgs) != 2 {
		t.Fatalf("persisted messages = %d (%+v), want user + failure turn", len(msgs), msgs)
	}
	last := msgs[1]
	if last.Role != conversation.RoleAssistant {
		t.Errorf("persisted role = %q, want assistant — the agent did speak", last.Role)
	}
	if !strings.Contains(last.Content, refusal) {
		t.Errorf("persisted content = %q, want the agent's text preserved", last.Content)
	}
	var errPart *conversation.Part
	for i := range last.Parts {
		if last.Parts[i].Type == "error" {
			errPart = &last.Parts[i]
		}
	}
	if errPart == nil {
		t.Fatalf("persisted parts = %+v, want an error part carrying the failure reason", last.Parts)
	}
	if !strings.Contains(errPart.Content, "agent reported a failed run") {
		t.Errorf("error part = %q, want the in-band failure reason", errPart.Content)
	}

	// 4. Message count reflects user turn + failure turn.
	if len(resolver.increments) != 1 || resolver.increments[0] != 2 {
		t.Errorf("increments = %v, want [2]", resolver.increments)
	}
}

// The silent variant: the CLI reported a failed turn without saying anything.
// There is no reply to attribute to the agent, so the turn is a system notice
// whose content is the reason — otherwise a reload shows an empty bubble.
func TestHandleChatMessageInBandFailureNoOutputPersistsSystemTurn(t *testing.T) {
	resolver := &capResolver{info: baseInfo()}
	ctr := &scriptedContainer{
		agentOutput: `{"type":"result","subtype":"error_during_execution","is_error":true}` + "\n",
		exitCode:    0,
	}
	b := testBridgeWithContainer(t, resolver, ctr)

	err := b.HandleChatMessage(context.Background(), "user-1", "sess-inband-silent", "hi", func(ws.ChatEvent) {})
	if err == nil {
		t.Fatal("expected HandleChatMessage to return the run error")
	}

	msgs, rerr := b.convStore.Read(context.Background(), "sess-inband-silent", 0, 0)
	if rerr != nil {
		t.Fatalf("read conversation: %v", rerr)
	}
	if len(msgs) != 2 {
		t.Fatalf("persisted messages = %d (%+v), want user + failure turn", len(msgs), msgs)
	}
	last := msgs[1]
	if last.Role != conversation.RoleSystem {
		t.Errorf("persisted role = %q, want system — the agent said nothing", last.Role)
	}
	if !strings.Contains(last.Content, "agent reported a failed run") {
		t.Errorf("persisted content = %q, want the failure reason as the turn body", last.Content)
	}
	if len(last.Parts) != 1 || last.Parts[0].Type != "error" {
		t.Errorf("persisted parts = %+v, want a single error part", last.Parts)
	}
}
