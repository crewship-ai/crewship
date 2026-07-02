package chatbridge

// Issue #545 — a chat run can finish "successfully" with ZERO emitted
// output (safety refusal swallowed by the adapter, prompt-budget
// pressure, or the agent CLI exiting cleanly with no stdout). The old
// behavior only logged a server-side Warn: no error event on the
// session channel and an empty assistant row in the conversation, so
// the user's message sat with no reply and no error — indistinguishable
// from a broken app. These tests pin the fix: an explicit error event
// (then a terminal done) for live viewers, a persisted system/error
// turn for later reloads, and a FAILED run record instead of a clean
// COMPLETED.

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/ws"
)

func TestHandleChatMessageZeroOutputSurfacesExplicitError(t *testing.T) {
	resolver := &capResolver{info: baseInfo()}
	// Agent CLI exits 0 with no stdout at all — the #545 shape.
	ctr := &scriptedContainer{agentOutput: "", exitCode: 0}
	b := testBridgeWithContainer(t, resolver, ctr)

	var events []ws.ChatEvent
	err := b.HandleChatMessage(context.Background(), "user-1", "sess-noout", "hi", func(e ws.ChatEvent) {
		events = append(events, e)
	})
	if err != nil {
		t.Fatalf("zero-output run must not fail the handler, got: %v", err)
	}

	// 1. Live viewers get an explicit, actionable error event on the
	// stream — not silence.
	var errEvt *ws.ChatEvent
	for i := range events {
		if events[i].Type == "error" {
			errEvt = &events[i]
		}
	}
	if errEvt == nil {
		t.Fatalf("expected an error event for a zero-output run, got %+v", events)
	}
	if !strings.Contains(errEvt.Content, "no output") {
		t.Errorf("error copy = %q, want the 'returned no output' message", errEvt.Content)
	}
	meta, _ := errEvt.Metadata.(map[string]any)
	if meta == nil || meta["reason"] != "no_output" {
		t.Errorf("error metadata = %+v, want reason=no_output", errEvt.Metadata)
	}
	// ...followed by a terminal done so clients leave the streaming state.
	if len(events) == 0 || events[len(events)-1].Type != "done" {
		t.Errorf("last event = %+v, want terminal done", events[len(events)-1:])
	}

	// 2. The run must not be recorded as a clean COMPLETED.
	if len(resolver.runUpdates) != 1 {
		t.Fatalf("runUpdates = %+v, want exactly one", resolver.runUpdates)
	}
	upd := resolver.runUpdates[0]
	if upd.status != "FAILED" {
		t.Errorf("run status = %q, want FAILED", upd.status)
	}
	if upd.errorMsg == nil || !strings.Contains(*upd.errorMsg, "no output") {
		t.Errorf("run errorMsg = %v, want no-output reason", upd.errorMsg)
	}

	// 3. A later reload sees the failure too: a persisted system turn
	// with an error part instead of an empty assistant bubble.
	msgs, rerr := b.convStore.Read(context.Background(), "sess-noout", 0, 0)
	if rerr != nil {
		t.Fatalf("read conversation: %v", rerr)
	}
	if len(msgs) != 2 {
		t.Fatalf("persisted messages = %d (%+v), want user + error turn", len(msgs), msgs)
	}
	last := msgs[1]
	if last.Role != conversation.RoleSystem {
		t.Errorf("persisted role = %q, want system", last.Role)
	}
	if !strings.Contains(last.Content, "no output") {
		t.Errorf("persisted content = %q, want the 'returned no output' copy", last.Content)
	}
	if len(last.Parts) != 1 || last.Parts[0].Type != "error" {
		t.Errorf("persisted parts = %+v, want a single error part", last.Parts)
	}

	// 4. Message count reflects user turn + error turn.
	if len(resolver.increments) != 1 || resolver.increments[0] != 2 {
		t.Errorf("increments = %v, want [2]", resolver.increments)
	}
}

// A group chat line that doesn't @mention the agent legitimately ends
// with no assistant reply. Its done event must be marked no_reply so
// the frontend's "done with no reply → show no-output error" fallback
// doesn't misfire on the sender.
func TestHandleChatMessageGroupNoMentionDoneMarksNoReply(t *testing.T) {
	info := baseInfo()
	info.Visibility = "group"
	resolver := &capResolver{info: info}
	// No container needed: the turn-taking gate short-circuits before
	// any provisioning side-effect.
	b, _ := testBridge(t, resolver)

	var done *ws.ChatEvent
	err := b.HandleChatMessage(context.Background(), "user-1", "sess-group", "hello everyone", func(e ws.ChatEvent) {
		if e.Type == "done" {
			cp := e
			done = &cp
		}
	})
	if err != nil {
		t.Fatalf("group no-mention message must succeed, got: %v", err)
	}
	if done == nil {
		t.Fatal("expected a done event")
	}
	meta, _ := done.Metadata.(map[string]any)
	if meta == nil || meta["no_reply"] != true {
		t.Errorf("done metadata = %+v, want no_reply=true", done.Metadata)
	}
}
