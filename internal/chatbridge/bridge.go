package chatbridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/logcollector"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/ws"
)

// ChatResolver provides the data layer for the chat bridge, resolving chat
// sessions to agent configurations and managing run lifecycle records.
type ChatResolver interface {
	CreateChat(ctx context.Context, req CreateChatRequest) error
	ResolveChat(ctx context.Context, chatID string) (*ChatInfo, error)
	ResolveAgent(ctx context.Context, agentID string) (*ChatInfo, error)
	GetWebhookSecret(ctx context.Context, agentID string) (string, error)
	CreateRun(ctx context.Context, runID, agentID, chatID, workspaceID, triggerType string, metadata map[string]interface{}) error
	UpdateRun(ctx context.Context, runID, status string, exitCode *int, errorMsg *string, metadata map[string]interface{}) error
	IncrementMessageCount(ctx context.Context, chatID string, delta int) error
	UpdateChatTitle(ctx context.Context, chatID, title string) error
}

// ChatInfo holds the resolved configuration for a chat session, including
// agent identity, crew context, credentials, and resource settings.
type ChatInfo struct {
	AgentID            string
	AgentSlug          string
	AgentRole          string
	CrewID             string
	CrewSlug           string
	ContainerID        string
	CLIAdapter         string
	LLMModel           string
	SystemPrompt       string
	ToolProfile        string
	Credentials        []orchestrator.Credential
	TimeoutSecs        int
	WorkspaceID        string
	MemoryEnabled      bool
	CrewMembers        []orchestrator.CrewMember
	AllCrews           []orchestrator.CrewInfo
	ActiveMissions     []orchestrator.MissionSummary
	NetworkMode        string
	AllowedDomains     []string
	MemoryMB           int
	CPUs               float64
	TTLHours           int
	MCPServers         []orchestrator.MCPServerConfig
	CrewMCPConfigJSON  string
	AgentMCPConfigJSON string
}

// Bridge connects the WebSocket chat interface to the orchestrator, resolving
// sessions, managing containers, persisting conversations, and streaming events.
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

// BridgeConfig holds default resource limits for containers created by the bridge.
type BridgeConfig struct {
	DefaultMemoryMB int
	DefaultCPUs     float64
}

// New creates a Bridge that connects WebSocket chat to the orchestrator.
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

func truncateID(id string, n int) string {
	if len(id) <= n {
		return id
	}
	return id[:n]
}

func generateMsgID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("msg_%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("msg_%d_%s", time.Now().UnixNano(), hex.EncodeToString(b))
}

// HandleChatMessage processes an incoming chat message by resolving the session,
// ensuring the container is running, persisting the message, and streaming the
// agent's response back to the client.
func (b *Bridge) HandleChatMessage(ctx context.Context, userID, chatID, content string, streamFn func(ws.ChatEvent)) error {
	b.logger.Debug("HandleChatMessage", "chat_id", chatID, "content_len", len(content))
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

	// Auto-title: use first user message (truncated) as session title
	title := content
	if len([]rune(title)) > 60 {
		title = string([]rune(title)[:57]) + "..."
	}
	if err := b.resolver.UpdateChatTitle(ctx, chatID, title); err != nil {
		b.logger.Debug("auto-title failed (non-fatal)", "error", err)
	}

	b.logger.Debug("resolving chat", "chat_id", chatID)
	info, err := b.resolver.ResolveChat(ctx, chatID)
	if err != nil {
		b.logger.Debug("ResolveChat failed", "error", err)
		streamFn(ws.ChatEvent{Type: "error", Content: "failed to resolve chat"})
		return fmt.Errorf("resolve chat: %w", err)
	}
	b.logger.Debug("chat resolved", "agent_id", info.AgentID, "crew_id", info.CrewID)

	// For COORDINATOR agents (no crew), use a synthetic crew identity for container management
	containerKey := info.CrewID
	if info.AgentRole == "COORDINATOR" && info.CrewID == "" {
		containerKey = "coordinator-" + info.WorkspaceID
		info.CrewID = containerKey
		info.CrewSlug = "coordinator"
	}

	// Look up cached container ID for this crew (avoids status noise on repeat messages)
	b.containerMu.RLock()
	containerID := b.containerCache[containerKey]
	b.containerMu.RUnlock()
	b.logger.Debug("container cache lookup", "crew_id", containerKey, "cached_id", containerID)

	// Verify cached container still exists and is running.
	// A stopped container (e.g. after network policy change) must be recreated.
	if containerID != "" && b.container != nil {
		status, err := b.container.ContainerStatus(ctx, containerID)
		if err != nil || (status != nil && status.State != "running" && status.State != "idle") {
			reason := "gone"
			if status != nil {
				reason = status.State
			}
			b.logger.Warn("cached container not usable, will recreate",
				"container_id", truncateID(containerID, 12), "state", reason)
			containerID = ""
			b.containerMu.Lock()
			delete(b.containerCache, containerKey)
			b.containerMu.Unlock()
		}
	}

	coldStart := containerID == ""

	memoryMB := info.MemoryMB
	if memoryMB <= 0 {
		memoryMB = b.cfg.DefaultMemoryMB
	}
	cpuVal := info.CPUs
	if cpuVal <= 0 {
		cpuVal = b.cfg.DefaultCPUs
	}

	if containerID == "" && b.container != nil {
		b.logger.Info("creating container", "crew_slug", info.CrewSlug)
		streamFn(ws.ChatEvent{Type: "status", Content: "Starting container..."})
		cID, err := b.container.EnsureCrewRuntime(ctx, provider.CrewConfig{
			ID:       info.CrewID,
			Slug:     info.CrewSlug,
			MemoryMB: memoryMB,
			CPUs:     cpuVal,
		})
		if err != nil {
			streamFn(ws.ChatEvent{Type: "error", Content: "failed to start agent container"})
			return fmt.Errorf("ensure team runtime: %w", err)
		}
		containerID = cID
		b.containerMu.Lock()
		b.containerCache[containerKey] = containerID
		b.containerMu.Unlock()
		streamFn(ws.ChatEvent{Type: "status", Content: "Container ready"})
		b.logger.Info("team container ensured", "crew_id", info.CrewID, "container_id", truncateID(containerID, 12))
	} else if containerID == "" {
		streamFn(ws.ChatEvent{Type: "error", Content: "container provider not configured"})
		return fmt.Errorf("no container provider and no container ID")
	}

	var fullResponse string
	var toolSummaries []string

	req := orchestrator.AgentRunRequest{
		AgentID:            info.AgentID,
		AgentSlug:          info.AgentSlug,
		AgentRole:          info.AgentRole,
		CrewID:             info.CrewID,
		CrewSlug:           info.CrewSlug,
		WorkspaceID:        info.WorkspaceID,
		ChatID:             chatID,
		ContainerID:        containerID,
		CLIAdapter:         info.CLIAdapter,
		LLMModel:           info.LLMModel,
		SystemPrompt:       info.SystemPrompt,
		UserMessage:        content,
		ToolProfile:        info.ToolProfile,
		Credentials:        info.Credentials,
		TimeoutSecs:        info.TimeoutSecs,
		MemoryEnabled:      info.MemoryEnabled,
		CrewMembers:        info.CrewMembers,
		AllCrews:           info.AllCrews,
		ActiveMissions:     info.ActiveMissions,
		NetworkMode:        info.NetworkMode,
		AllowedDomains:     info.AllowedDomains,
		MemoryMB:           memoryMB,
		CPUs:               cpuVal,
		TTLHours:           info.TTLHours,
		MCPServers:         info.MCPServers,
		CrewMCPConfigJSON:  info.CrewMCPConfigJSON,
		AgentMCPConfigJSON: info.AgentMCPConfigJSON,
	}

	// Only show "Starting agent..." on cold start (first message, container freshly created).
	// On subsequent messages the container is warm — no progress noise.
	if coldStart {
		streamFn(ws.ChatEvent{Type: "status", Content: "Starting agent..."})
	}

	logBuf := logcollector.NewOutputBuffer(b.logWriter, info.CrewID, info.AgentSlug)
	defer logBuf.Close()

	var resultMeta map[string]interface{}

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
		// Track tool calls for conversation context (compact summary, not full output).
		if event.Type == "tool_call" {
			toolSummaries = append(toolSummaries, fmt.Sprintf("[tool: %s]", event.Content))
		}
		if event.Type == "tool_result" {
			truncated := event.Content
			if len(truncated) > 200 {
				truncated = truncated[:200] + "..."
			}
			toolSummaries = append(toolSummaries, fmt.Sprintf("[result: %s]", truncated))
		}
		// Capture result metadata (cost, usage, duration) for the run record.
		if event.Type == "result" {
			if m, ok := event.Metadata.(map[string]interface{}); ok {
				resultMeta = m
			}
		}

		if err := logBuf.Append(logcollector.LogEntry{
			Timestamp: event.Timestamp,
			Level:     "info",
			Agent:     info.AgentSlug,
			Event:     event.Type,
			Content:   event.Content,
			Metadata:  event.Metadata,
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
			if err := b.resolver.UpdateRun(cleanCtx, runID, "CANCELLED", nil, &cancelMsg, map[string]interface{}{
				"duration_ms": time.Since(startedAt).Milliseconds(),
			}); err != nil {
				b.logger.Warn("failed to update run status", "run_id", runID, "status", "CANCELLED", "error", err)
			}
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
	completedMeta := map[string]interface{}{
		"duration_ms": time.Since(startedAt).Milliseconds(),
	}
	if resultMeta != nil {
		if v, ok := resultMeta["total_cost_usd"]; ok {
			completedMeta["total_cost_usd"] = v
		}
		if v, ok := resultMeta["num_turns"]; ok {
			completedMeta["num_turns"] = v
		}
		if v, ok := resultMeta["usage"]; ok {
			completedMeta["usage"] = v
		}
		if v, ok := resultMeta["model_usage"]; ok {
			completedMeta["model_usage"] = v
		}
	}
	if err := b.resolver.UpdateRun(ctx, runID, "COMPLETED", &exitCode, nil, completedMeta); err != nil {
		b.logger.Warn("failed to update run status", "run_id", runID, "error", err)
	}

	// Build compact tool summary for conversation context (max 5 entries).
	var toolSummary string
	if len(toolSummaries) > 10 {
		toolSummary = strings.Join(toolSummaries[:10], "\n") + fmt.Sprintf("\n...and %d more", len(toolSummaries)-10)
	} else if len(toolSummaries) > 0 {
		toolSummary = strings.Join(toolSummaries, "\n")
	}

	if err := b.convStore.Append(ctx, chatID, conversation.Message{
		ID:          generateMsgID(),
		Role:        conversation.RoleAssistant,
		Content:     fullResponse,
		ToolSummary: toolSummary,
		Timestamp:   time.Now().UTC(),
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
