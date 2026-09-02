package chatbridge

// #2269 follow-up, defect 5: HandleChatMessage's deferred agentRunLock.End
// triggered no pump at all — an assignment queued behind a chat turn
// (because the chat send held AgentRunLock for the same agent) had to wait
// for an unrelated crew completion or the stuck-QUEUED sweeper even after
// the chat turn had long finished. SetAssignmentPumper wires a seam so the
// chat door can drain that agent's crew queue the instant it releases the
// lock, symmetric with api.AssignmentHandler's own post-completion pump.

import (
	"context"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/ws"
)

// recordingPumper is a chatbridge.AssignmentPumper test double that records
// every agent id it was asked to pump, without touching a real DB/crew.
type recordingPumper struct {
	mu     sync.Mutex
	agents []string
}

func (p *recordingPumper) PumpForAgent(_ context.Context, agentID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.agents = append(p.agents, agentID)
}

func (p *recordingPumper) calls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.agents))
	copy(out, p.agents)
	return out
}

// TestHandleChatMessage_ReleasingCrossSurfaceLock_PumpsAssignmentQueue
// proves the chat door pumps the agent's crew queue when it releases
// AgentRunLock — on unfixed code, PumpForAgent is never called and this
// test's recordingPumper stays empty.
func TestHandleChatMessage_ReleasingCrossSurfaceLock_PumpsAssignmentQueue(t *testing.T) {
	resolver := &mockResolver{info: exclusivityChatInfo()} // AgentID: "agent-1"
	b := testBridgeWithContainer(t, resolver, &failContainer{})

	pumper := &recordingPumper{}
	b.SetAssignmentPumper(pumper)

	if err := b.HandleChatMessage(context.Background(), "user-1", "chat-pump-1", "hello", func(ws.ChatEvent) {}); err == nil {
		t.Fatal("expected an error from container setup (failContainer)")
	}

	got := pumper.calls()
	if len(got) != 1 || got[0] != "agent-1" {
		t.Fatalf("assignment pumper calls = %v, want exactly [\"agent-1\"] — releasing the cross-surface "+
			"lock must pump that agent's crew queue", got)
	}
}

// TestHandleChatMessage_NilAssignmentPumper_IsANoop guards the fail-open
// contract: a Bridge with no pumper wired (tests, or a build that predates
// SetAssignmentPumper) must not panic on release.
func TestHandleChatMessage_NilAssignmentPumper_IsANoop(t *testing.T) {
	resolver := &mockResolver{info: exclusivityChatInfo()}
	b := testBridgeWithContainer(t, resolver, &failContainer{})
	// b.assignmentPumper left nil deliberately.

	if err := b.HandleChatMessage(context.Background(), "user-1", "chat-pump-2", "hello", func(ws.ChatEvent) {}); err == nil {
		t.Fatal("expected an error from container setup (failContainer)")
	}
}
