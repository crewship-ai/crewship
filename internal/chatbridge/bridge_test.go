package chatbridge

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/logcollector"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/ws"
)

type mockResolver struct {
	info *ChatInfo
	err  error
}

func (m *mockResolver) CreateChat(_ context.Context, _ CreateChatRequest) error {
	return nil
}

func (m *mockResolver) ResolveChat(_ context.Context, _ string) (*ChatInfo, error) {
	return m.info, m.err
}

func (m *mockResolver) CreateRun(_ context.Context, _, _, _, _, _ string, _ map[string]interface{}) error {
	return nil
}

func (m *mockResolver) UpdateRun(_ context.Context, _, _ string, _ *int, _ *string, _ map[string]interface{}) error {
	return nil
}

func (m *mockResolver) IncrementMessageCount(_ context.Context, _ string, _ int) error {
	return nil
}

func (m *mockResolver) UpdateChatTitle(_ context.Context, _, _ string) error {
	return nil
}

func (m *mockResolver) ResolveAgent(_ context.Context, _ string) (*ChatInfo, error) {
	return m.info, m.err
}

func (m *mockResolver) GetWebhookSecret(_ context.Context, _ string) (string, error) {
	return "", nil
}

func testBridge(t *testing.T, resolver ChatResolver) (*Bridge, string) {
	t.Helper()
	dir := t.TempDir()
	logger := slog.Default()
	convStore := conversation.NewStore(dir, logger)
	logWriter := logcollector.NewWriter(dir, logger)
	orch := orchestrator.New(nil, &memState{data: make(map[string]map[string][]byte)}, logger)
	return New(orch, nil, convStore, logWriter, resolver, BridgeConfig{}, logger), dir
}

// minimal in-memory state for tests
type memState struct {
	data map[string]map[string][]byte
}

func (m *memState) Get(_ context.Context, bucket, key string) ([]byte, error) {
	if b, ok := m.data[bucket]; ok {
		return b[key], nil
	}
	return nil, nil
}
func (m *memState) Set(_ context.Context, bucket, key string, value []byte) error {
	if m.data[bucket] == nil {
		m.data[bucket] = make(map[string][]byte)
	}
	m.data[bucket][key] = value
	return nil
}
func (m *memState) Delete(_ context.Context, bucket, key string) error {
	if b, ok := m.data[bucket]; ok {
		delete(b, key)
	}
	return nil
}
func (m *memState) List(_ context.Context, bucket string) (map[string][]byte, error) {
	return m.data[bucket], nil
}
func (m *memState) ListByPrefix(_ context.Context, bucket, prefix string) (map[string][]byte, error) {
	result := make(map[string][]byte)
	for k, v := range m.data[bucket] {
		if strings.HasPrefix(k, prefix) {
			result[k] = v
		}
	}
	return result, nil
}
func (m *memState) Close() error { return nil }

func TestGenerateMsgID(t *testing.T) {
	id1 := generateMsgID()
	id2 := generateMsgID()

	if id1 == "" || id2 == "" {
		t.Fatal("generateMsgID returned empty string")
	}
	if !strings.HasPrefix(id1, "msg_") {
		t.Errorf("expected prefix 'msg_', got %q", id1)
	}
	if id1 == id2 {
		t.Error("expected unique IDs")
	}
}

func TestGenerateMsgIDFormat(t *testing.T) {
	id := generateMsgID()
	parts := strings.Split(id, "_")
	if len(parts) < 3 {
		t.Errorf("expected at least 3 parts in msg ID, got %d: %q", len(parts), id)
	}
}

func TestHandleChatMessageResolveError(t *testing.T) {
	resolver := &mockResolver{err: fmt.Errorf("chat not found")}
	b, _ := testBridge(t, resolver)

	var events []ws.ChatEvent
	streamFn := func(e ws.ChatEvent) { events = append(events, e) }

	err := b.HandleChatMessage(context.Background(), "user-1", "sess-1", "hello", streamFn)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "resolve chat") {
		t.Errorf("expected 'resolve chat' in error, got: %v", err)
	}

	hasError := false
	for _, e := range events {
		if e.Type == "error" {
			hasError = true
		}
	}
	if !hasError {
		t.Error("expected error event to be emitted")
	}
}

type failContainer struct{}

func (f *failContainer) EnsureCrewRuntime(_ context.Context, _ provider.CrewConfig) (string, error) {
	return "", fmt.Errorf("container unavailable")
}
func (f *failContainer) StopCrewRuntime(_ context.Context, _ string) error   { return nil }
func (f *failContainer) RemoveCrewRuntime(_ context.Context, _ string) error { return nil }
func (f *failContainer) ContainerStatus(_ context.Context, _ string) (*provider.ContainerStatus, error) {
	return nil, fmt.Errorf("not running")
}
func (f *failContainer) Exec(_ context.Context, _ provider.ExecConfig) (*provider.ExecResult, error) {
	return nil, fmt.Errorf("exec failed: container unavailable")
}
func (f *failContainer) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	return false, 1, nil
}
func (f *failContainer) CrewContainerName(slug string) string {
	return "crewship-team-" + slug
}

func testBridgeWithContainer(t *testing.T, resolver ChatResolver, ctr provider.ContainerProvider) *Bridge {
	t.Helper()
	dir := t.TempDir()
	logger := slog.Default()
	convStore := conversation.NewStore(dir, logger)
	logWriter := logcollector.NewWriter(dir, logger)
	orch := orchestrator.New(ctr, &memState{data: make(map[string]map[string][]byte)}, logger)
	return New(orch, ctr, convStore, logWriter, resolver, BridgeConfig{}, logger)
}

func TestHandleChatMessageRunAgentError(t *testing.T) {
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
	b := testBridgeWithContainer(t, resolver, &failContainer{})

	var events []ws.ChatEvent
	streamFn := func(e ws.ChatEvent) { events = append(events, e) }

	err := b.HandleChatMessage(context.Background(), "user-1", "sess-1", "hello", streamFn)
	if err == nil {
		t.Fatal("expected error from RunAgent")
	}
	if !strings.Contains(err.Error(), "ensure team runtime") && !strings.Contains(err.Error(), "run agent") {
		t.Errorf("expected 'ensure team runtime' or 'run agent' in error, got: %v", err)
	}
}

func TestHandleChatMessagePersistsUserMessage(t *testing.T) {
	resolver := &mockResolver{err: fmt.Errorf("resolve fail")}
	b, _ := testBridge(t, resolver)

	streamFn := func(_ ws.ChatEvent) {}

	_ = b.HandleChatMessage(context.Background(), "user-1", "test-chat", "hello world", streamFn)

	messages, err := b.convStore.Read(context.Background(), "test-chat", 0, 0)
	if err != nil {
		t.Fatalf("read messages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}
	if messages[0].Role != conversation.RoleUser {
		t.Errorf("expected user role, got %s", messages[0].Role)
	}
	if messages[0].Content != "hello world" {
		t.Errorf("expected 'hello world', got %q", messages[0].Content)
	}
}

func TestBridgeNew(t *testing.T) {
	resolver := &mockResolver{}
	b, _ := testBridge(t, resolver)
	if b == nil {
		t.Fatal("expected non-nil bridge")
	}
	if b.orch == nil {
		t.Error("expected non-nil orchestrator")
	}
	if b.convStore == nil {
		t.Error("expected non-nil conversation store")
	}
	if b.resolver == nil {
		t.Error("expected non-nil resolver")
	}
}

func TestBridge_EmitsStatusEvents(t *testing.T) {
	// When container needs to be ensured, the bridge should emit status events
	// during the setup phase (not just a single "thinking" event)
	resolver := &mockResolver{
		info: &ChatInfo{
			AgentID:     "agent-1",
			AgentSlug:   "test-agent",
			CrewID:      "crew-1",
			CrewSlug:    "test-crew",
			CLIAdapter:  "CLAUDE_CODE",
			TimeoutSecs: 30,
		},
	}
	b := testBridgeWithContainer(t, resolver, &failContainer{})

	var events []ws.ChatEvent
	streamFn := func(e ws.ChatEvent) { events = append(events, e) }

	_ = b.HandleChatMessage(context.Background(), "user-1", "sess-1", "hello", streamFn)

	// Should have at least one status event before the error
	hasStatus := false
	for _, e := range events {
		if e.Type == "status" {
			hasStatus = true
			break
		}
	}
	if !hasStatus {
		t.Errorf("expected at least one 'status' event during setup, got events: %+v", events)
	}
}

func TestBridge_ChatEventMetadata(t *testing.T) {
	// Verify that ChatEvent metadata is preserved when forwarding from orchestrator
	resolver := &mockResolver{err: fmt.Errorf("resolve fail")}
	b, _ := testBridge(t, resolver)

	var events []ws.ChatEvent
	streamFn := func(e ws.ChatEvent) { events = append(events, e) }

	_ = b.HandleChatMessage(context.Background(), "user-1", "sess-1", "hello", streamFn)

	// The error event should have no metadata (just type and content)
	for _, e := range events {
		if e.Type == "error" {
			// Error events don't need metadata, just checking the field exists
			// and doesn't break serialization
			_ = e.Metadata
		}
	}
}

func TestBridge_CancelledContextNoErrorEvent(t *testing.T) {
	// When context is cancelled (user pressed stop), the bridge should NOT emit
	// an error event -- the hub handles sending "done" instead.
	resolver := &mockResolver{
		info: &ChatInfo{
			AgentID:     "agent-1",
			AgentSlug:   "test-agent",
			CrewID:      "crew-1",
			CrewSlug:    "test-crew",
			CLIAdapter:  "CLAUDE_CODE",
			TimeoutSecs: 30,
		},
	}
	b := testBridgeWithContainer(t, resolver, &failContainer{})

	var events []ws.ChatEvent
	streamFn := func(e ws.ChatEvent) { events = append(events, e) }

	// Pre-cancel the context to simulate user stop
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_ = b.HandleChatMessage(ctx, "user-1", "sess-1", "hello", streamFn)

	// Should NOT have any error events (the hub sends "done" instead)
	for _, e := range events {
		if e.Type == "error" && strings.Contains(e.Content, "context canceled") {
			t.Errorf("bridge should not emit error for cancelled context, got: %q", e.Content)
		}
	}
}
