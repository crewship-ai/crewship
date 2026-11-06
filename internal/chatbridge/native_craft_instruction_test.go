package chatbridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
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

// repeatingAgent stands in for a model that quotes what it was given: it says
// the instruction back only when the instruction reached its prompt.
type repeatingAgent struct {
	scriptedContainer
	seen strings.Builder
}

func (a *repeatingAgent) Exec(ctx context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	a.seen.WriteString(strings.Join(cfg.Cmd, " "))
	a.seen.WriteString(strings.Join(cfg.Env, " "))
	if cfg.Stdin != nil {
		raw, _ := io.ReadAll(cfg.Stdin)
		a.seen.Write(raw)
	}
	reply := "I have nothing to repeat."
	for _, encoded := range strings.Fields(a.seen.String()) {
		if raw, err := base64.StdEncoding.DecodeString(strings.Trim(encoded, "'")); err == nil {
			a.seen.Write(raw)
		}
	}
	if strings.Contains(a.seen.String(), nativeInstruction) {
		reply = "You asked: " + nativeInstruction
	}
	line, _ := json.Marshal(map[string]any{"type": "stream_event", "event": map[string]any{"type": "content_block_delta", "delta": map[string]any{"type": "text_delta", "text": reply}}})
	a.agentOutput = claudeProvenanceOutput + string(line) + "\n" + `{"type":"result","subtype":"success","result":"ok"}` + "\n"
	return a.scriptedContainer.Exec(ctx, cfg)
}

type recordingEntries struct{ entries []orchestrator.JournalEntry }

func (r *recordingEntries) Emit(_ context.Context, entry orchestrator.JournalEntry) (string, error) {
	r.entries = append(r.entries, entry)
	return "j_test", nil
}

// #5009: "the instruction reaches no ordinary run, agent log or other member's
// diagnostics" also has to hold for the ordinary turn after the instruction.
// Without it, a quoted instruction lands in the agent log and the journal,
// which any workspace member reads.
func TestNativeCraftInstructionStaysOutOfLaterOrdinaryTurn(t *testing.T) {
	logs := t.TempDir()
	logger := slog.Default()
	agent := &repeatingAgent{}
	orch := orchestrator.New(agent, &memState{data: make(map[string]map[string][]byte)}, logger)
	journal := &recordingEntries{}
	orch.SetJournal(journal)
	store := conversation.NewStore(t.TempDir(), logger)
	orch.SetConversationStore(store)
	b := New(orch, agent, store, logcollector.NewWriter(logs, logger), &capResolver{info: baseInfo()}, BridgeConfig{}, logger)

	if err := b.HandleChatMessage(context.Background(), "user-1", "chat-1", nativeInstruction, func(ws.ChatEvent) {}, nativeInstructionOption()); err != nil {
		t.Fatal(err)
	}
	if err := b.HandleChatMessage(context.Background(), "user-1", "chat-1", "What did I ask you?", func(ws.ChatEvent) {}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(agent.seen.String(), nativeInstruction) {
		t.Error("a later ordinary turn received the instruction in its prompt")
	}
	for _, entry := range journal.entries {
		if entry.Type == "chat.agent_response" && strings.Contains(fmt.Sprint(entry.Summary, entry.Payload), nativeInstruction) {
			t.Errorf("journal %s repeats the instruction: %s", entry.Type, entry.Summary)
		}
	}
	err := filepath.WalkDir(logs, func(path string, entry fs.DirEntry, walk error) error {
		if walk != nil || entry.IsDir() {
			return walk
		}
		raw, read := os.ReadFile(path)
		if read == nil && strings.Contains(string(raw), "merger") {
			t.Errorf("agent log %s repeats the instruction", path)
		}
		return read
	})
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := b.convStore.Read(context.Background(), "chat-1", 0, 0)
	if err != nil || len(msgs) == 0 || msgs[0].Content != nativeInstruction {
		t.Fatalf("the author's transcript lost the instruction: %+v %v", msgs, err)
	}
}
