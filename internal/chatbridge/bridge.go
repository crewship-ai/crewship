package chatbridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/logcollector"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/ws"
)

type ChatResolver interface {
	ResolveChat(ctx context.Context, chatID string) (*ChatInfo, error)
	CreateRun(ctx context.Context, runID, agentID, chatID, workspaceID, triggerType string, metadata map[string]interface{}) error
	UpdateRun(ctx context.Context, runID, status string, exitCode *int, errorMsg *string, metadata map[string]interface{}) error
	IncrementMessageCount(ctx context.Context, chatID string, delta int) error
}

type ChatInfo struct {
	AgentID       string
	AgentSlug     string
	AgentRole     string
	CrewID        string
	CrewSlug      string
	ContainerID   string
	CLIAdapter    string
	LLMModel      string
	SystemPrompt  string
	ToolProfile   string
	Credentials   []orchestrator.Credential
	TimeoutSecs   int
	WorkspaceID   string
	MemoryEnabled bool
	CrewMembers   []orchestrator.CrewMember
}

type Bridge struct {
	orch      *orchestrator.Orchestrator
	container provider.ContainerProvider
	convStore *conversation.Store
	logWriter *logcollector.Writer
	resolver  ChatResolver
	cfg       BridgeConfig
	logger    *slog.Logger

	// containerCache maps crewID → containerID so subsequent messages
	// skip the "Starting container..." status events (container is warm).
	containerMu    sync.RWMutex
	containerCache map[string]string
}

type BridgeConfig struct {
	DefaultMemoryMB int
	DefaultCPUs     float64
}

func New(
	orch *orchestrator.Orchestrator,
	container provider.ContainerProvider,
	convStore *conversation.Store,
	logWriter *logcollector.Writer,
	resolver ChatResolver,
	cfg BridgeConfig,
	logger *slog.Logger,
) *Bridge {
	if cfg.DefaultMemoryMB == 0 {
		cfg.DefaultMemoryMB = 512
	}
	if cfg.DefaultCPUs == 0 {
		cfg.DefaultCPUs = 1.0
	}
	return &Bridge{
		orch:           orch,
		container:      container,
		convStore:      convStore,
		logWriter:      logWriter,
		resolver:       resolver,
		cfg:            cfg,
		logger:         logger,
		containerCache: make(map[string]string),
	}
}

func generateMsgID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("msg_%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("msg_%d_%s", time.Now().UnixNano(), hex.EncodeToString(b))
}

func (b *Bridge) HandleChatMessage(ctx context.Context, userID, chatID, content string, streamFn func(ws.ChatEvent)) error {
	if err := b.convStore.Append(ctx, chatID, conversation.Message{
		ID:        generateMsgID(),
		Role:      conversation.RoleUser,
		Content:   content,
		Timestamp: time.Now().UTC(),
	}); err != nil {
		b.logger.Error("failed to persist user message", "error", err)
		streamFn(ws.ChatEvent{Type: "error", Content: "failed to save message"})
		return fmt.Errorf("persist user message: %w", err)
	}

	info, err := b.resolver.ResolveChat(ctx, chatID)
	if err != nil {
		streamFn(ws.ChatEvent{Type: "error", Content: "failed to resolve chat"})
		return fmt.Errorf("resolve chat: %w", err)
	}

	// Look up cached container ID for this crew (avoids status noise on repeat messages)
	b.containerMu.RLock()
	containerID := b.containerCache[info.CrewID]
	b.containerMu.RUnlock()

	// Verify cached container still exists (may have been deleted at runtime)
	if containerID != "" && b.container != nil {
		if _, err := b.container.ContainerStatus(ctx, containerID); err != nil {
			b.logger.Warn("cached container gone, will recreate",
				"container_id", containerID[:12], "error", err)
			containerID = ""
			b.containerMu.Lock()
			delete(b.containerCache, info.CrewID)
			b.containerMu.Unlock()
		}
	}

	coldStart := containerID == ""

	if containerID == "" && b.container != nil {
		streamFn(ws.ChatEvent{Type: "status", Content: "Starting container..."})
		cID, err := b.container.EnsureCrewRuntime(ctx, provider.CrewConfig{
			ID:       info.CrewID,
			Slug:     info.CrewSlug,
			MemoryMB: b.cfg.DefaultMemoryMB,
			CPUs:     b.cfg.DefaultCPUs,
		})
		if err != nil {
			streamFn(ws.ChatEvent{Type: "error", Content: "failed to start agent container"})
			return fmt.Errorf("ensure team runtime: %w", err)
		}
		containerID = cID
		b.containerMu.Lock()
		b.containerCache[info.CrewID] = containerID
		b.containerMu.Unlock()
		streamFn(ws.ChatEvent{Type: "status", Content: "Container ready"})
		b.logger.Info("team container ensured", "crew_id", info.CrewID, "container_id", containerID[:12])
	} else if containerID == "" {
		streamFn(ws.ChatEvent{Type: "error", Content: "container provider not configured"})
		return fmt.Errorf("no container provider and no container ID")
	}

	var fullResponse string

	req := orchestrator.AgentRunRequest{
		AgentID:       info.AgentID,
		AgentSlug:     info.AgentSlug,
		AgentRole:     info.AgentRole,
		CrewID:        info.CrewID,
		CrewSlug:      info.CrewSlug,
		WorkspaceID:   info.WorkspaceID,
		ChatID:        chatID,
		ContainerID:   containerID,
		CLIAdapter:    info.CLIAdapter,
		LLMModel:      info.LLMModel,
		SystemPrompt:  info.SystemPrompt,
		UserMessage:   content,
		ToolProfile:   info.ToolProfile,
		Credentials:   info.Credentials,
		TimeoutSecs:   info.TimeoutSecs,
		MemoryEnabled: info.MemoryEnabled,
		CrewMembers:   info.CrewMembers,
	}

	// Only show "Starting agent..." on cold start (first message, container freshly created).
	// On subsequent messages the container is warm — no progress noise.
	if coldStart {
		streamFn(ws.ChatEvent{Type: "status", Content: "Starting agent..."})
	}

	handler := func(event orchestrator.AgentEvent) {
		streamFn(ws.ChatEvent{
			Type:     event.Type,
			Content:  event.Content,
			Metadata: event.Metadata,
		})
		// Only accumulate actual text content for the persisted assistant message.
		// System events (sidecar security logs, etc.) and thinking events should not
		// be stored as part of the assistant response.
		if event.Type == "text" {
			fullResponse += event.Content
		}

		if err := b.logWriter.Append(info.CrewID, info.AgentSlug, logcollector.LogEntry{
			Timestamp: event.Timestamp,
			Level:     "info",
			Agent:     info.AgentSlug,
			Event:     "output",
			Content:   event.Content,
		}); err != nil {
			b.logger.Debug("log write error", "error", err)
		}
	}

	runID := generateMsgID()
	runMeta := map[string]interface{}{
		"cli_adapter": info.CLIAdapter,
		"crew_id":     info.CrewID,
		"crew_slug":   info.CrewSlug,
		"agent_slug":  info.AgentSlug,
		"tags":        []string{"chat", info.CLIAdapter},
	}
	if err := b.resolver.CreateRun(ctx, runID, info.AgentID, chatID, info.WorkspaceID, "USER", runMeta); err != nil {
		b.logger.Warn("failed to create run record", "error", err)
	}

	startedAt := time.Now()
	runErr := b.orch.RunAgent(ctx, req, handler)
	if runErr != nil {
		// If context was cancelled (user pressed stop), don't emit error -- the hub
		// sends a clean "done" event. Emitting error here would cause an error flash.
		if ctx.Err() == context.Canceled {
			b.logger.Info("run cancelled by user", "chat_id", chatID, "duration_ms", time.Since(startedAt).Milliseconds())
			cancelMsg := "cancelled"
			cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cleanCancel()
			_ = b.resolver.UpdateRun(cleanCtx, runID, "CANCELLED", nil, &cancelMsg, map[string]interface{}{
				"duration_ms": time.Since(startedAt).Milliseconds(),
			})
			// Persist partial response if any
			if fullResponse != "" {
				_ = b.convStore.Append(cleanCtx, chatID, conversation.Message{
					ID:        generateMsgID(),
					Role:      conversation.RoleAssistant,
					Content:   fullResponse,
					Timestamp: time.Now().UTC(),
				})
				_ = b.resolver.IncrementMessageCount(cleanCtx, chatID, 2)
			} else {
				_ = b.resolver.IncrementMessageCount(cleanCtx, chatID, 1)
			}
			return fmt.Errorf("run agent: %w", runErr)
		}

		errMsg := runErr.Error()
		if err := b.resolver.UpdateRun(ctx, runID, "FAILED", nil, &errMsg, map[string]interface{}{
			"duration_ms": time.Since(startedAt).Milliseconds(),
		}); err != nil {
			b.logger.Warn("failed to update run status", "run_id", runID, "error", err)
		}
		streamFn(ws.ChatEvent{Type: "error", Content: runErr.Error()})
		return fmt.Errorf("run agent: %w", runErr)
	}

	exitCode := 0
	if err := b.resolver.UpdateRun(ctx, runID, "COMPLETED", &exitCode, nil, map[string]interface{}{
		"duration_ms": time.Since(startedAt).Milliseconds(),
	}); err != nil {
		b.logger.Warn("failed to update run status", "run_id", runID, "error", err)
	}

	if err := b.convStore.Append(ctx, chatID, conversation.Message{
		ID:        generateMsgID(),
		Role:      conversation.RoleAssistant,
		Content:   fullResponse,
		Timestamp: time.Now().UTC(),
	}); err != nil {
		b.logger.Error("failed to persist assistant message", "error", err, "chat_id", chatID)
		streamFn(ws.ChatEvent{Type: "error", Content: "failed to save response"})
		return fmt.Errorf("persist assistant message: %w", err)
	}

	// Update message count in DB (user + assistant = 2 messages)
	if err := b.resolver.IncrementMessageCount(ctx, chatID, 2); err != nil {
		b.logger.Warn("failed to update message count", "chat_id", chatID, "error", err)
	}

	streamFn(ws.ChatEvent{Type: "done", Content: ""})

	return nil
}
