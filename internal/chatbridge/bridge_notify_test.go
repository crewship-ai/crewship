package chatbridge

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/ws"
)

// fakeReplyNotifier records NotifyAssistantReply invocations so tests can
// assert the bridge announces persisted assistant replies exactly once,
// with the metadata the notifier needs to target the right users.
type fakeReplyNotifier struct {
	mu    sync.Mutex
	calls []ReplyNotification
}

func (f *fakeReplyNotifier) NotifyAssistantReply(_ context.Context, n ReplyNotification) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, n)
}

func (f *fakeReplyNotifier) snapshot() []ReplyNotification {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]ReplyNotification, len(f.calls))
	copy(out, f.calls)
	return out
}

// A successful run that persists an assistant reply must fire the reply
// notifier with the chat/agent identity and the reply text.
func TestHandleChatMessage_NotifiesOnPersistedReply(t *testing.T) {
	resolver := &capResolver{info: baseInfo()}
	ctr := &scriptedContainer{agentOutput: claudeSuccessOutput(0), exitCode: 0}
	b := testBridgeWithContainer(t, resolver, ctr)
	fn := &fakeReplyNotifier{}
	b.SetReplyNotifier(fn)

	err := b.HandleChatMessage(context.Background(), "user-1", "sess-notify", "hello", func(ws.ChatEvent) {})
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}

	calls := fn.snapshot()
	if len(calls) != 1 {
		t.Fatalf("notifier calls = %d, want 1", len(calls))
	}
	n := calls[0]
	if n.ChatID != "sess-notify" {
		t.Errorf("ChatID = %q, want sess-notify", n.ChatID)
	}
	if n.AuthorUserID != "user-1" {
		t.Errorf("AuthorUserID = %q, want user-1", n.AuthorUserID)
	}
	if n.AgentID != "agent-1" || n.AgentSlug != "valid-slug" {
		t.Errorf("agent identity = %q/%q, want agent-1/valid-slug", n.AgentID, n.AgentSlug)
	}
	if n.WorkspaceID != "ws-1" {
		t.Errorf("WorkspaceID = %q, want ws-1", n.WorkspaceID)
	}
	if n.ReplyText != "Hello world" {
		t.Errorf("ReplyText = %q, want the persisted assistant text", n.ReplyText)
	}
	// RepliedAt must carry the persist timestamp — the notifier compares
	// it against per-user read cursors to drop notifications a racing
	// mark-read already covered.
	if n.RepliedAt.IsZero() {
		t.Error("RepliedAt is zero, want the assistant message persist time")
	}
}

// A run that fails before producing a reply must not notify anyone.
func TestHandleChatMessage_NoNotifyOnRunError(t *testing.T) {
	resolver := &capResolver{info: baseInfo()}
	ctr := &scriptedContainer{agentOutput: "", exitCode: 1}
	b := testBridgeWithContainer(t, resolver, ctr)
	fn := &fakeReplyNotifier{}
	b.SetReplyNotifier(fn)

	_ = b.HandleChatMessage(context.Background(), "user-1", "sess-notify-err", "hello", func(ws.ChatEvent) {})

	if calls := fn.snapshot(); len(calls) != 0 {
		t.Fatalf("notifier calls = %d, want 0 on failed run", len(calls))
	}
}

// No notifier wired → the success path must still complete (nil-safe).
func TestHandleChatMessage_NilNotifierSafe(t *testing.T) {
	resolver := &capResolver{info: baseInfo()}
	ctr := &scriptedContainer{agentOutput: claudeSuccessOutput(0), exitCode: 0}
	b := testBridgeWithContainer(t, resolver, ctr)

	if err := b.HandleChatMessage(context.Background(), "user-1", "sess-notify-nil", "hello", func(ws.ChatEvent) {}); err != nil {
		t.Fatalf("expected success without notifier, got: %v", err)
	}
}

// Partial replies survive both confirmed and unconfirmed detached stops.
type detachedReplyContainer struct {
	scriptedContainer
	confirmed bool
	onStop    func()
}

func (c *detachedReplyContainer) ExecInspect(_ context.Context, id string) (bool, int, error) {
	return id == "agent-exec", 0, nil
}
func (c *detachedReplyContainer) Exec(ctx context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	if strings.Contains(strings.Join(cfg.Cmd, " "), "/bin/kill -TERM --") {
		if c.onStop != nil {
			c.onStop()
		}
		out := "PRESENT"
		if c.confirmed {
			out = "ABSENT"
		}
		return &provider.ExecResult{ExecID: "stop", Reader: io.NopCloser(strings.NewReader(out))}, nil
	}
	// The runtime exits on the first subsequent watcher probe.
	if strings.Contains(strings.Join(cfg.Cmd, " "), "command -v tmux") {
		return &provider.ExecResult{ExecID: "probe", Reader: io.NopCloser(strings.NewReader("ABSENT"))}, nil
	}
	return c.scriptedContainer.Exec(ctx, cfg)
}

func TestHandleChatMessage_NotifiesOnDetachedPartialReply(t *testing.T) {
	for _, tc := range []struct{ confirmed, cancel bool }{{false, false}, {true, false}, {true, true}} {
		t.Run(fmt.Sprintf("confirmed=%v/cancel=%v", tc.confirmed, tc.cancel), func(t *testing.T) {
			confirmed := tc.confirmed
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			resolver := &capResolver{info: baseInfo()}
			ctr := &detachedReplyContainer{scriptedContainer: scriptedContainer{agentOutput: claudeSuccessOutput(0)}, confirmed: confirmed}
			if tc.cancel {
				ctr.onStop = cancel
			}
			b := testBridgeWithContainer(t, resolver, ctr)
			b.orch.SetDetachedExecMonitoring(time.Millisecond, time.Millisecond)
			fn := &fakeReplyNotifier{}
			b.SetReplyNotifier(fn)
			var events []ws.ChatEvent
			err := b.HandleChatMessage(ctx, "user-1", "sess-detached-notify", "hello", func(e ws.ChatEvent) { events = append(events, e) })
			if confirmed {
				if err == nil {
					t.Error("confirmed stop must surface a terminal failure")
				}
				wantStatus := "FAILED"
				if tc.cancel {
					wantStatus = "CANCELLED"
				}
				if len(resolver.runUpdates) != 1 || resolver.runUpdates[0].status != wantStatus {
					t.Errorf("confirmed stop updates: %+v", resolver.runUpdates)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if len(resolver.runUpdates) != 0 {
					t.Errorf("unconfirmed stop must stay nonterminal: %+v", resolver.runUpdates)
				}
				done := 0
				for _, e := range events {
					meta, _ := e.Metadata.(map[string]any)
					if e.Type == "done" && meta["reason"] == "detached_still_running" {
						done++
					}
				}
				if done != 1 {
					t.Errorf("detached stream closes=%d, want 1", done)
				}
			}
			calls := fn.snapshot()
			if len(calls) != 1 || calls[0].ReplyText != "Hello world" || calls[0].RepliedAt.IsZero() {
				t.Fatalf("partial reply notifications: %+v", calls)
			}
		})
	}
}
