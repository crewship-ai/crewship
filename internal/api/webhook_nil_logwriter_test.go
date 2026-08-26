package api

// A webhook-triggered run must survive a handler built without a log writer.
//
// The route is only mounted when r.logWriter != nil (router_orchestration.go),
// so today the nil never reaches production — but NewWebhookHandler accepts it,
// every other run driver in the tree guards for it, and the dispatch goroutine
// dereferenced it with no check at all. A panic there is not a failed run: it
// is an unrecovered panic on a background goroutine, which takes the daemon
// down from an unauthenticated, externally-triggered code path.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/webhook"
)

// nilLogWriterResolver records the terminal UpdateRun so the test can prove the
// dispatch goroutine ran to completion rather than merely not crashing yet.
type nilLogWriterResolver struct {
	fakeChatResolver
	mu     sync.Mutex
	called bool
	status string
}

func (r *nilLogWriterResolver) UpdateRun(_ context.Context, _, status string, _ *int, _ *string, _ map[string]interface{}) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.called, r.status = true, status
	return nil
}

func TestWebhookTrigger_NilLogWriterDoesNotPanic(t *testing.T) {
	resolver := &nilLogWriterResolver{}
	resolver.resolveReturnInfo = &chatbridge.ChatInfo{
		AgentID: "agent-wh", AgentSlug: "ag", AgentRole: "AGENT",
		CrewID: "crew-wh", CrewSlug: "c", WorkspaceID: "ws-nil", CLIAdapter: "CLAUDE_CODE",
	}
	// A stream with both a streamed event (buffered, flushed on newline) and a
	// terminal result, so every route into the buffer's writer is exercised.
	prov := inbandAsgProvider{stream: provenanceTextLine + provenanceOKLine}

	// The last argument is the log writer — nil on purpose.
	h := NewWebhookHandler(setupTestDB(t), newTestLogger(), resolver,
		orchestrator.New(prov, newInbandAsgState(), newTestLogger()), nil, prov, nil)

	if err := h.trigger(context.Background(), "crew-wh", "agent-wh",
		webhook.WebhookPayload{Event: "deploy", Source: "gh"}); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	if !waitForBackgroundWork(60 * time.Second) {
		t.Fatal("webhook dispatch goroutine did not finish")
	}

	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	if !resolver.called {
		t.Fatal("run never finalized — the dispatch goroutine did not reach UpdateRun")
	}
	if resolver.status != "COMPLETED" {
		t.Errorf("status = %q, want COMPLETED: losing the log sink must not fail the run", resolver.status)
	}
}
