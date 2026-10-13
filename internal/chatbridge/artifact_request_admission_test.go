package chatbridge

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/ws"
)

type artifactCountingContainer struct {
	*scriptedContainer
	execs int
}

func (c *artifactCountingContainer) Exec(ctx context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	if len(cfg.Env) > 0 {
		c.execs++
	}
	return c.scriptedContainer.Exec(ctx, cfg)
}
func TestArtifactRequestAdmissionFailureNeverExecutesAgent(t *testing.T) {
	info := baseInfo()
	receipt := &chataudience.Receipt{UserID: "user-1", ChatID: "sess-request", MemberID: "member", MemberRevision: 1}
	info.HumanAuthority = receipt
	resolver := &capResolver{info: info, createRunErr: errors.New("artifact admission refused")}
	ctr := &artifactCountingContainer{scriptedContainer: &scriptedContainer{agentOutput: claudeProvenanceOutput}}
	bridge := testBridgeWithContainer(t, resolver, ctr)
	err := bridge.HandleChatMessage(context.Background(), "user-1", "sess-request", "change artifact", func(ws.ChatEvent) {}, ws.ChatMessageOption{HumanResume: true, HumanAuthority: receipt, ArtifactRequestID: "request-id"})
	if err == nil || !strings.Contains(err.Error(), "artifact request admission") || ctr.execs != 0 {
		t.Fatalf("admission err=%v execs=%d", err, ctr.execs)
	}
	if len(resolver.runUpdates) != 1 || resolver.runUpdates[0].status != "FAILED" {
		t.Fatalf("missing terminal admission record: %+v", resolver.runUpdates)
	}
}
func TestOrdinaryChatRunRecordErrorRemainsNonfatal(t *testing.T) {
	resolver := &capResolver{info: baseInfo(), createRunErr: errors.New("ordinary journal failure")}
	ctr := &artifactCountingContainer{scriptedContainer: &scriptedContainer{agentOutput: claudeProvenanceOutput}}
	bridge := testBridgeWithContainer(t, resolver, ctr)
	if err := bridge.HandleChatMessage(context.Background(), "user-1", "ordinary-chat", "hello", func(ws.ChatEvent) {}); err != nil {
		t.Fatal(err)
	}
	if ctr.execs == 0 {
		t.Fatal("ordinary chat was suppressed")
	}
}
