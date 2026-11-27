package chatbridge

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/logcollector"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/ws"
)

// The agent runs on another chat or assignment (cross-surface busy) and the person writes in their
// private chat: the message is saved, marked not run, and the busy answer names it, so a
// reload still shows it. "Send again" with resend_of runs that same saved
// message: no second user turn. A group chat's busy keeps the old bounce that leaves no trace.
func TestCrossSurfaceBusySavesThePersonsMessage(t *testing.T) {
	dir := t.TempDir()
	logger := slog.Default()
	convStore := conversation.NewStore(dir, logger)
	resolver := &countingResolver{mockResolver: mockResolver{info: exclusivityChatInfo()}}
	ctr := &failContainer{}
	b := New(orchestrator.New(ctr, &memState{data: make(map[string]map[string][]byte)}, logger), ctr, convStore, logcollector.NewWriter(dir, logger), resolver, BridgeConfig{}, logger)
	if !b.AgentRunLock().TryStart("agent-1") {
		t.Fatal("could not hold the agent elsewhere")
	}
	const chat = "chat-busy-saved"
	err := b.HandleChatMessage(context.Background(), "user-1", chat, "Clear the Loan ID column", func(ws.ChatEvent) {})
	var saved *ws.MessageNotRun
	if !errors.Is(err, ws.ErrAgentBusy) || !errors.As(err, &saved) || saved.MessageID == "" {
		t.Fatalf("cross-surface busy: %v, want a busy answer that names the saved message", err)
	}
	msgs, _ := convStore.Read(context.Background(), chat, 0, 100)
	var user, notRun int
	for _, m := range msgs {
		if m.Role == conversation.RoleUser && m.ID == saved.MessageID && m.Content == "Clear the Loan ID column" {
			user++
		}
		if m.ID == "notrun-"+saved.MessageID {
			notRun++
		}
	}
	if user != 1 || notRun != 1 {
		t.Fatalf("saved %d user turns and %d not-run notes, want 1 and 1: %+v", user, notRun, msgs)
	}
	b.AgentRunLock().End("agent-1")
	// Send again: the same saved message runs (the container fails here, after the busy check).
	err = b.HandleChatMessage(context.Background(), "user-1", chat, "anything", func(ws.ChatEvent) {}, ws.ChatMessageOption{Metadata: map[string]any{ResendOfMetadataKey: saved.MessageID}})
	if errors.Is(err, ws.ErrAgentBusy) || err == nil {
		t.Fatalf("send again: %v, want the run to start (and fail on the container)", err)
	}
	msgs, _ = convStore.Read(context.Background(), chat, 0, 100)
	user = 0
	for _, m := range msgs {
		if m.Role == conversation.RoleUser {
			user++
		}
	}
	if user != 1 {
		t.Fatalf("send again stored %d user turns, want the one saved", user)
	}
	// Someone else's saved message, or one that is not marked not run, cannot be sent again.
	if err = b.HandleChatMessage(context.Background(), "user-2", chat, "x", func(ws.ChatEvent) {}, ws.ChatMessageOption{Metadata: map[string]any{ResendOfMetadataKey: saved.MessageID}}); !errors.Is(err, ErrResendUnavailable) {
		t.Fatalf("send again by another person: %v", err)
	}

	group := &countingResolver{mockResolver: mockResolver{info: exclusivityChatInfo()}}
	group.info.Visibility = "group"
	g := New(orchestrator.New(ctr, &memState{data: make(map[string]map[string][]byte)}, logger), ctr, convStore, logcollector.NewWriter(dir, logger), group, BridgeConfig{}, logger)
	g.AgentRunLock().TryStart("agent-1")
	err = g.HandleChatMessage(context.Background(), "user-1", "chat-busy-group", "@test-agent hello", func(ws.ChatEvent) {})
	if !errors.Is(err, ws.ErrAgentBusy) || errors.As(err, &saved) {
		t.Fatalf("group chat busy: %v, want the bounce", err)
	}
	if msgs, _ := convStore.Read(context.Background(), "chat-busy-group", 0, 100); len(msgs) != 0 {
		t.Fatalf("group chat busy stored %d messages", len(msgs))
	}
}

// Every refusal of a run in a private chat keeps the message as not run:
// the chat already answering, a hire awaiting approval, a build that could not be queued, an
// environment that could not start after the message was saved.
func TestEveryRunRefusalKeepsThePersonsMessage(t *testing.T) {
	newBridge := func(t *testing.T, info *ChatInfo) (*Bridge, *conversation.Store) {
		dir := t.TempDir()
		logger := slog.Default()
		store := conversation.NewStore(dir, logger)
		ctr := &failContainer{}
		return New(orchestrator.New(ctr, &memState{data: make(map[string]map[string][]byte)}, logger), ctr, store, logcollector.NewWriter(dir, logger), &countingResolver{mockResolver: mockResolver{info: info}}, BridgeConfig{}, logger), store
	}
	for name, c := range map[string]struct {
		info  func(*ChatInfo)
		setup func(*Bridge)
		code  string
	}{
		"the chat is still answering": {setup: func(b *Bridge) { b.tryMarkRunStart("chat-r") }, code: "agent_busy_chat"},
		"a hire awaiting approval":    {info: func(i *ChatInfo) { i.AgentStatus = AgentStatusPendingReview }, code: "pending_review"},
		"a build that was not queued": {info: func(i *ChatInfo) {
			i.DevcontainerConfig = `{"image":"x","features":{"ghcr.io/devcontainers/features/go:1":{}}}`
		}, setup: func(b *Bridge) { b.SetProvisioningEnqueuer(&stubEnqueuer{returnError: errors.New("rate limited")}) }, code: "provision_failed"},
		"an environment that did not start": {code: "runtime_unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			info := exclusivityChatInfo()
			if c.info != nil {
				c.info(info)
			}
			b, store := newBridge(t, info)
			if c.setup != nil {
				c.setup(b)
			}
			var events []ws.ChatEvent
			err := b.HandleChatMessage(context.Background(), "user-1", "chat-r", "Clear the Loan ID column", func(e ws.ChatEvent) { events = append(events, e) })
			var kept *ws.MessageNotRun
			if !errors.As(err, &kept) || kept.Code != c.code || kept.MessageID == "" {
				t.Fatalf("refusal %v, want the message kept as not run with %s", err, c.code)
			}
			msgs, _ := store.Read(context.Background(), "chat-r", 0, 100)
			var user, note int
			for _, m := range msgs {
				if m.Role == conversation.RoleUser && m.ID == kept.MessageID {
					user++
				}
				if m.ID == "notrun-"+kept.MessageID {
					note++
				}
			}
			if user != 1 || note != 1 {
				t.Fatalf("saved %d user turns and %d not-run notes, want 1 and 1", user, note)
			}
			if c.code != "agent_busy_chat" {
				named := false
				for _, e := range events {
					if m, ok := e.Metadata.(map[string]any); ok && m["message_id"] == kept.MessageID && m["not_run_reason"] == c.code {
						named = true
					}
				}
				if !named {
					t.Fatalf("no event names the kept message: %+v", events)
				}
			}
		})
	}
}
