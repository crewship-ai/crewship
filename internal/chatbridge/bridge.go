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
	"github.com/crewship-ai/crewship/internal/ws"
)

type SessionResolver interface {
	ResolveSession(ctx context.Context, sessionID string) (*SessionInfo, error)
}

type SessionInfo struct {
	AgentID      string
	AgentSlug    string
	TeamID       string
	TeamSlug     string
	ContainerID  string
	CLIAdapter   string
	SystemPrompt string
	ToolProfile  string
	Credentials  []orchestrator.Credential
	TimeoutSecs  int
}

type Bridge struct {
	orch      *orchestrator.Orchestrator
	convStore *conversation.Store
	logWriter *logcollector.Writer
	resolver  SessionResolver
	logger    *slog.Logger
}

func New(
	orch *orchestrator.Orchestrator,
	convStore *conversation.Store,
	logWriter *logcollector.Writer,
	resolver SessionResolver,
	logger *slog.Logger,
) *Bridge {
	return &Bridge{
		orch:      orch,
		convStore: convStore,
		logWriter: logWriter,
		resolver:  resolver,
		logger:    logger,
	}
}

func generateMsgID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return fmt.Sprintf("msg_%d_%s", time.Now().UnixNano(), hex.EncodeToString(b))
}

func (b *Bridge) HandleChatMessage(ctx context.Context, userID, sessionID, content string, streamFn func(ws.ChatEvent)) error {
	if err := b.convStore.Append(ctx, sessionID, conversation.Message{
		ID:        generateMsgID(),
		Role:      conversation.RoleUser,
		Content:   content,
		Timestamp: time.Now().UTC(),
	}); err != nil {
		b.logger.Error("failed to persist user message", "error", err)
		streamFn(ws.ChatEvent{Type: "error", Content: "failed to save message"})
		return fmt.Errorf("persist user message: %w", err)
	}

	info, err := b.resolver.ResolveSession(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("resolve session: %w", err)
	}

	streamFn(ws.ChatEvent{Type: "thinking", Content: "Processing..."})

	var fullResponse string

	req := orchestrator.AgentRunRequest{
		AgentID:      info.AgentID,
		AgentSlug:    info.AgentSlug,
		TeamID:       info.TeamID,
		TeamSlug:     info.TeamSlug,
		SessionID:    sessionID,
		ContainerID:  info.ContainerID,
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

		if err := b.logWriter.Append(info.TeamID, info.AgentSlug, logcollector.LogEntry{
			Timestamp: event.Timestamp,
			Level:     "info",
			Agent:     info.AgentSlug,
			Event:     "output",
			Content:   event.Content,
		}); err != nil {
			b.logger.Debug("log write error", "error", err)
		}
	}

	if err := b.orch.RunAgent(ctx, req, handler); err != nil {
		streamFn(ws.ChatEvent{Type: "error", Content: err.Error()})
		return fmt.Errorf("run agent: %w", err)
	}

	if err := b.convStore.Append(ctx, sessionID, conversation.Message{
		ID:        generateMsgID(),
		Role:      conversation.RoleAssistant,
		Content:   fullResponse,
		Timestamp: time.Now().UTC(),
	}); err != nil {
		b.logger.Error("failed to persist assistant message", "error", err, "session_id", sessionID)
		streamFn(ws.ChatEvent{Type: "error", Content: "failed to save response"})
		return fmt.Errorf("persist assistant message: %w", err)
	}

	streamFn(ws.ChatEvent{Type: "done", Content: ""})

	return nil
}
