package chatbridge

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/logcollector"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/ws"
)

// countingPublisher records whether the orchestrator opened a session
// recording for a run it dispatched.
type countingPublisher struct {
	mu    sync.Mutex
	chats []string
}

func (p *countingPublisher) BeginSessionRun(chatID string) ws.SessionRun {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.chats = append(p.chats, chatID)
	return nopRun{}
}

func (p *countingPublisher) began() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.chats...)
}

type nopRun struct{}

func (nopRun) Emit(ws.ChatEvent) {}
func (nopRun) End()              {}

// The WebSocket send path records the whole turn on `session:{chatID}` itself
// (ws.Client.handleSendMessage): container-start status events, the agent's
// output, and the terminal error/done pair AFTER RunAgent returns. #1823 added
// an orchestrator-level recording for the paths that have no such caller —
// scheduler, webhook, routine step, agent-start IPC — and the bridge must opt
// this path OUT of it.
//
// Without the opt-out every agent event is published twice with two different
// sequence numbers, and the orchestrator's synthesized `done` closes the
// stream while the turn is still finishing: a watcher sees the run end before
// the reply is persisted, and the browser finalizes its streaming turn early.
func TestHandleChatMessage_DoesNotOpenASecondSessionRecording(t *testing.T) {
	resolver := &mockResolver{
		info: &ChatInfo{
			AgentID:     "agent-1",
			AgentSlug:   "test-agent",
			CrewID:      "crew-1",
			CrewSlug:    "test-crew",
			CLIAdapter:  "CLAUDE_CODE",
			ToolProfile: "CODING",
			TimeoutSecs: 30,
		},
	}

	dir := t.TempDir()
	// Warn-level handler rather than slog.Default(): a run that reaches
	// RunAgent logs its way through container setup, and the default logger is
	// process-global state a test should not be reconfiguring.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ctr := &scriptedContainer{agentOutput: "{}\n"}
	orch := orchestrator.New(ctr, &memState{data: make(map[string]map[string][]byte)}, logger)
	pub := &countingPublisher{}
	orch.SetSessionPublisher(pub)

	b := New(orch, ctr, conversation.NewStore(dir, logger), logcollector.NewWriter(dir, logger),
		resolver, BridgeConfig{}, logger)

	_ = b.HandleChatMessage(context.Background(), "user-1", "sess-1", "hello", func(ws.ChatEvent) {})

	if got := pub.began(); len(got) != 0 {
		t.Fatalf("the WebSocket chat path opened its own orchestrator-level session recording for %v — every frame would be published twice", got)
	}
}
