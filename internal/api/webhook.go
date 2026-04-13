package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/logcollector"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/webhook"
	"github.com/crewship-ai/crewship/internal/ws"
)

// WebhookHandler receives incoming webhook events and triggers agent runs.
type WebhookHandler struct {
	handler   *webhook.Handler
	logger    *slog.Logger
	resolver  chatbridge.ChatResolver
	orch      *orchestrator.Orchestrator
	hub       *ws.Hub
	container provider.ContainerProvider
	logWriter *logcollector.Writer
}

// NewWebhookHandler creates a WebhookHandler with the given dependencies for webhook verification and dispatch.
func NewWebhookHandler(
	logger *slog.Logger,
	resolver chatbridge.ChatResolver,
	orch *orchestrator.Orchestrator,
	hub *ws.Hub,
	container provider.ContainerProvider,
	logWriter *logcollector.Writer,
) *WebhookHandler {
	wh := &WebhookHandler{
		logger:    logger,
		resolver:  resolver,
		orch:      orch,
		hub:       hub,
		container: container,
		logWriter: logWriter,
	}

	wh.handler = webhook.NewHandler(logger, wh.lookupSecret, wh.trigger)
	return wh
}

// ServeHTTP dispatches incoming webhook requests to the underlying webhook handler.
func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.handler.ServeHTTP(w, r)
}

func (h *WebhookHandler) lookupSecret(ctx context.Context, _, agentID string) (string, error) {
	return h.resolver.GetWebhookSecret(ctx, agentID)
}

func (h *WebhookHandler) trigger(ctx context.Context, crewID, agentID string, payload webhook.WebhookPayload) error {
	h.logger.Info("webhook trigger", "crew_id", crewID, "agent_id", agentID, "event", payload.Event)

	// 1. Resolve agent config
	info, err := h.resolver.ResolveAgent(ctx, agentID)
	if err != nil {
		return fmt.Errorf("resolve agent: %w", err)
	}

	// 2. Create a chat session for this webhook if it doesn't exist
	// We use a deterministic chat ID based on the agent ID so we don't spam sessions,
	// or we could create a new one every time. Let's use a "webhook" suffix.
	chatID := fmt.Sprintf("webhook-%s", agentID)
	if err := h.resolver.CreateChat(ctx, chatbridge.CreateChatRequest{
		ChatID:      chatID,
		AgentID:     agentID,
		WorkspaceID: info.WorkspaceID,
		Title:       fmt.Sprintf("Webhook: %s", payload.Event),
	}); err != nil {
		h.logger.Warn("failed to create/ensure webhook chat session", "error", err)
	}

	// 3. Ensure container is running
	containerID, err := h.container.EnsureCrewRuntime(ctx, provider.CrewConfig{
		ID:       info.CrewID,
		Slug:     info.CrewSlug,
		MemoryMB: info.MemoryMB,
		CPUs:     info.CPUs,
	})
	if err != nil {
		return fmt.Errorf("ensure crew runtime: %w", err)
	}

	// 4. Create run record
	runID := fmt.Sprintf("run-wh-%d", time.Now().UnixNano())
	runMeta := map[string]interface{}{
		"event":   payload.Event,
		"source":  payload.Source,
		"trigger": "WEBHOOK",
	}
	if err := h.resolver.CreateRun(ctx, runID, agentID, chatID, info.WorkspaceID, "WEBHOOK", runMeta); err != nil {
		h.logger.Warn("failed to create run record", "error", err)
	}

	// 5. Build user message from payload
	userMsg := fmt.Sprintf("Webhook event received:\nEvent: %s\nSource: %s\nData: %+v",
		payload.Event, payload.Source, payload.Data)

	// 6. Run agent (async)
	go func() {
		// Use a fresh context for the background run
		runCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		req := orchestrator.AgentRunRequest{
			AgentID:        info.AgentID,
			AgentSlug:      info.AgentSlug,
			AgentRole:      info.AgentRole,
			CrewID:         info.CrewID,
			CrewSlug:       info.CrewSlug,
			WorkspaceID:    info.WorkspaceID,
			ChatID:         chatID,
			ContainerID:    containerID,
			CLIAdapter:     info.CLIAdapter,
			LLMModel:       info.LLMModel,
			SystemPrompt:   info.SystemPrompt,
			UserMessage:    userMsg,
			ToolProfile:    info.ToolProfile,
			Credentials:    info.Credentials,
			TimeoutSecs:    info.TimeoutSecs,
			MemoryEnabled:  info.MemoryEnabled,
			NetworkMode:    info.NetworkMode,
			AllowedDomains: info.AllowedDomains,
			MemoryMB:       info.MemoryMB,
			CPUs:           info.CPUs,
			TTLHours:       info.TTLHours,
		}

		logBuf := logcollector.NewOutputBuffer(h.logWriter, info.CrewID, info.AgentSlug)
		defer logBuf.Close()

		handler := func(event orchestrator.AgentEvent) {
			_ = logBuf.Append(logcollector.LogEntry{
				Timestamp: event.Timestamp,
				Level:     "info",
				Agent:     info.AgentSlug,
				Event:     event.Type,
				Content:   event.Content,
				Metadata:  event.Metadata,
			})

			broadcastWorkspaceEvent(h.hub, info.WorkspaceID, "agent.log",
				map[string]interface{}{
					"ts":       event.Timestamp,
					"level":    "info",
					"agent":    info.AgentSlug,
					"agent_id": info.AgentID,
					"event":    event.Type,
					"content":  event.Content,
					"metadata": event.Metadata,
				})
		}

		startedAt := time.Now()
		err := h.orch.RunAgent(runCtx, req, handler)

		exitCode := 0
		status := "COMPLETED"
		var errMsg *string
		if err != nil {
			status = "FAILED"
			s := err.Error()
			errMsg = &s
			exitCode = 1
		}

		completedMeta := map[string]interface{}{
			"duration_ms": time.Since(startedAt).Milliseconds(),
		}
		if updateErr := h.resolver.UpdateRun(runCtx, runID, status, &exitCode, errMsg, completedMeta); updateErr != nil {
			h.logger.Warn("failed to update run status", "run_id", runID, "status", status, "error", updateErr)
		}
	}()

	return nil
}
