package chatbridge

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/logcollector"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/ws"
)

const nativeInstruction = "Confidential: rewrite the merger summary"

type countingContainer struct {
	failContainer
	ensured atomic.Int32
}

func (c *countingContainer) EnsureCrewRuntime(ctx context.Context, cfg provider.CrewConfig) (string, error) {
	c.ensured.Add(1)
	return c.failContainer.EnsureCrewRuntime(ctx, cfg)
}

// Agent logs live in their own directory here, so the assertion below reads
// exactly what GET /api/v1/agents/{agentId}/logs serves to any workspace member.
func nativeInstructionBridge(t *testing.T) (*Bridge, *countingContainer, string) {
	t.Helper()
	logs := t.TempDir()
	logger := slog.Default()
	container := &countingContainer{}
	orch := orchestrator.New(container, &memState{data: make(map[string]map[string][]byte)}, logger)
	return New(orch, container, conversation.NewStore(t.TempDir(), logger), logcollector.NewWriter(logs, logger), &capResolver{info: baseInfo()}, BridgeConfig{}, logger), container, logs
}

func nativeInstructionOption() ws.ChatMessageOption {
	return ws.ChatMessageOption{Metadata: map[string]any{NativeCraftInstructionMetadataKey: true}}
}

// Another workspace member can read an agent's logs. A private chat has no
// protected provenance, so an instruction must leave no diagnostic copy there.
func TestNativeCraftInstructionPersistsForAuthorWithoutRunOrDiagnosticCopy(t *testing.T) {
	b, container, logs := nativeInstructionBridge(t)
	var events []ws.ChatEvent
	if err := b.HandleChatMessage(context.Background(), "user-1", "chat-1", nativeInstruction, func(e ws.ChatEvent) { events = append(events, e) }, nativeInstructionOption()); err != nil {
		t.Fatal(err)
	}
	if got := container.ensured.Load(); got != 0 {
		t.Fatalf("an instruction started a runtime %d times", got)
	}
	msgs, err := b.convStore.Read(context.Background(), "chat-1", 0, 0)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("persisted %d messages: %v", len(msgs), err)
	}
	// The fields task admission binds: this human's own user message for this agent.
	msg := msgs[0]
	if msg.Role != conversation.RoleUser || msg.AuthorUserID != "user-1" || msg.AgentID != "agent-1" || msg.Content != nativeInstruction {
		t.Fatalf("persisted message cannot bind a task: %+v", msg)
	}
	if len(events) != 2 || events[0].Type != "user_message" || events[1].Type != "done" {
		t.Fatalf("events = %+v, want the saved echo and a terminal done", events)
	}
	echo, _ := events[0].Metadata.(map[string]any)
	if echo["message_id"] != msg.ID || echo["author_user_id"] != "user-1" {
		t.Fatalf("echo does not name the persisted message: %+v", echo)
	}
	if done, _ := events[1].Metadata.(map[string]any); done["no_reply"] != true {
		t.Fatalf("done = %+v, want no_reply", events[1])
	}
	err = filepath.WalkDir(logs, func(path string, entry fs.DirEntry, walk error) error {
		if walk != nil || entry.IsDir() {
			return walk
		}
		raw, read := os.ReadFile(path)
		if read != nil {
			return read
		}
		if strings.Contains(string(raw), "merger") {
			t.Errorf("agent log %s copies the instruction", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The same send without the option is the path the option must leave.
func TestNativeCraftInstructionOrdinarySendStillRuns(t *testing.T) {
	b, container, _ := nativeInstructionBridge(t)
	_ = b.HandleChatMessage(context.Background(), "user-1", "chat-1", nativeInstruction, func(ws.ChatEvent) {})
	if container.ensured.Load() == 0 {
		t.Fatal("an ordinary private send did not reach the runtime")
	}
}

func TestNativeCraftInstructionRefusesAmbiguousSend(t *testing.T) {
	for name, metadata := range map[string]map[string]any{
		"non-literal flag": {NativeCraftInstructionMetadataKey: "true"},
		"false flag":       {NativeCraftInstructionMetadataKey: false},
		"page context":     {NativeCraftInstructionMetadataKey: true, "page_context": map[string]any{"slug": "page"}},
	} {
		t.Run(name, func(t *testing.T) {
			b, container, _ := nativeInstructionBridge(t)
			b.pageContextResolver = &pageResolverStub{}
			err := b.HandleChatMessage(context.Background(), "user-1", "chat-1", nativeInstruction, func(ws.ChatEvent) {}, ws.ChatMessageOption{Metadata: metadata})
			msgs, read := b.convStore.Read(context.Background(), "chat-1", 0, 0)
			if err == nil || read != nil || len(msgs) != 0 || container.ensured.Load() != 0 {
				t.Fatalf("ambiguous send acted: err=%v read=%v messages=%d ensured=%d", err, read, len(msgs), container.ensured.Load())
			}
		})
	}
}
