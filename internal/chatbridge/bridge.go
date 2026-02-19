package chatbridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/logcollector"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/ws"
)

type ChatResolver interface {
	ResolveChat(ctx context.Context, chatID string) (*ChatInfo, error)
	CreateRun(ctx context.Context, runID, agentID, chatID, workspaceID, triggerType string) error
	UpdateRun(ctx context.Context, runID, status string, exitCode *int, errorMsg *string) error
}

type ChatInfo struct {
	AgentID      string
	AgentSlug    string
	CrewID       string
	CrewSlug     string
	ContainerID  string
	CLIAdapter   string
	SystemPrompt string
	ToolProfile  string
	Credentials  []orchestrator.Credential
	TimeoutSecs  int
	WorkspaceID  string
}

type Bridge struct {
	orch      *orchestrator.Orchestrator
	container provider.ContainerProvider
	convStore *conversation.Store
	logWriter *logcollector.Writer
	resolver  ChatResolver
	cfg       BridgeConfig
	logger    *slog.Logger
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
		orch:      orch,
		container: container,
		convStore: convStore,
		logWriter: logWriter,
		resolver:  resolver,
		cfg:       cfg,
		logger:    logger,
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

	streamFn(ws.ChatEvent{Type: "thinking", Content: "Processing..."})

	containerID := info.ContainerID
	if containerID == "" && b.container != nil {
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
		b.logger.Info("team container ensured", "crew_id", info.CrewID, "container_id", containerID[:12])
	} else if containerID == "" {
		streamFn(ws.ChatEvent{Type: "error", Content: "container provider not configured"})
		return fmt.Errorf("no container provider and no container ID")
	}

	var fullResponse string

	req := orchestrator.AgentRunRequest{
		AgentID:      info.AgentID,
		AgentSlug:    info.AgentSlug,
		CrewID:       info.CrewID,
		CrewSlug:     info.CrewSlug,
		ChatID:    chatID,
		ContainerID:  containerID,
		CLIAdapter:   info.CLIAdapter,
		SystemPrompt: info.SystemPrompt,
		UserMessage:  content,
		ToolProfile:  info.ToolProfile,
		Credentials:  info.Credentials,
		TimeoutSecs:  info.TimeoutSecs,
	}

	handler := func(event orchestrator.AgentEvent) {
		streamFn(ws.ChatEvent{
			Type:    event.Type,
			Content: event.Content,
		})
		fullResponse += event.Content

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
	if err := b.resolver.CreateRun(ctx, runID, info.AgentID, chatID, info.WorkspaceID, "USER"); err != nil {
		b.logger.Warn("failed to create run record", "error", err)
	}

	runErr := b.orch.RunAgent(ctx, req, handler)
	if runErr != nil {
		errMsg := runErr.Error()
		_ = b.resolver.UpdateRun(ctx, runID, "FAILED", nil, &errMsg)
		streamFn(ws.ChatEvent{Type: "error", Content: runErr.Error()})
		return fmt.Errorf("run agent: %w", runErr)
	}

	exitCode := 0
	_ = b.resolver.UpdateRun(ctx, runID, "COMPLETED", &exitCode, nil)

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

	streamFn(ws.ChatEvent{Type: "done", Content: ""})

	return nil
}
