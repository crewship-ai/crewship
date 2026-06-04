package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/logcollector"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/webhook"
	"github.com/crewship-ai/crewship/internal/ws"
)

// webhookIdempotencyKeyCtxKey carries the request's Idempotency-Key header
// from ServeHTTP into trigger (the webhook.Handler.TriggerFunc contract
// doesn't pass headers, and internal/webhook is out of scope to change).
type webhookIdempotencyKeyCtxKey struct{}

// webhookIdempotencyPipelineID is the synthetic pipeline_id label written
// into the shared pipeline_run_idempotency table for webhook-originated
// reservations. The table's pipeline_id column is NOT NULL but is only a
// provenance label here — the (workspace_id, idempotency_key) PK is what
// enforces dedup, so reusing the pipeline store avoids a new table/migration.
const webhookIdempotencyPipelineID = "webhook"

// WebhookHandler receives incoming webhook events and triggers agent runs.
type WebhookHandler struct {
	db        *sql.DB
	handler   *webhook.Handler
	logger    *slog.Logger
	resolver  chatbridge.ChatResolver
	orch      *orchestrator.Orchestrator
	hub       *ws.Hub
	container provider.ContainerProvider
	logWriter *logcollector.Writer
	idem      *pipeline.IdempotencyStore
}

// NewWebhookHandler creates a WebhookHandler with the given dependencies for webhook verification and dispatch.
func NewWebhookHandler(
	db *sql.DB,
	logger *slog.Logger,
	resolver chatbridge.ChatResolver,
	orch *orchestrator.Orchestrator,
	hub *ws.Hub,
	container provider.ContainerProvider,
	logWriter *logcollector.Writer,
) *WebhookHandler {
	wh := &WebhookHandler{
		db:        db,
		logger:    logger,
		resolver:  resolver,
		orch:      orch,
		hub:       hub,
		container: container,
		logWriter: logWriter,
	}
	// Webhook re-delivery dedup reuses the pipeline idempotency primitive
	// (shared pipeline_run_idempotency table) so we don't add a new table.
	// nil db (test wiring) leaves idem nil and dedup is skipped.
	if db != nil {
		wh.idem = pipeline.NewIdempotencyStore(db)
	}

	wh.handler = webhook.NewHandler(logger, wh.lookupSecret, wh.trigger)
	return wh
}

// ServeHTTP dispatches incoming webhook requests to the underlying webhook handler.
// It stashes the Idempotency-Key header into the request context first so trigger
// (whose webhook.TriggerFunc signature can't carry headers) can read it for dedup.
func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if key := r.Header.Get("Idempotency-Key"); key != "" {
		r = r.WithContext(context.WithValue(r.Context(), webhookIdempotencyKeyCtxKey{}, key))
	}
	h.handler.ServeHTTP(w, r)
}

func (h *WebhookHandler) lookupSecret(ctx context.Context, crewID, agentID string) (string, error) {
	// Thread crewID so the server scopes the secret lookup to the (crew,
	// agent) pair the webhook URL named — without it the server-side crew
	// scoping never engages and any crew's secret is fetchable by id alone.
	return h.resolver.GetWebhookSecret(ctx, crewID, agentID)
}

// webhookIdempotencyKey resolves the dedup key for a webhook delivery.
// It returns the caller-supplied Idempotency-Key (stashed in ctx by
// ServeHTTP) when present, otherwise a deterministic synthetic key over
// the agent id + payload (event/source/data). The synthetic fallback
// means an external system that re-fires the identical event without a
// key still collapses to a single run, while distinct events produce
// distinct keys.
func agentWebhookIdempotencyKey(ctx context.Context, agentID string, payload webhook.WebhookPayload) string {
	if v, ok := ctx.Value(webhookIdempotencyKeyCtxKey{}).(string); ok && v != "" {
		return v
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%+v", agentID, payload.Event, payload.Source, payload.Data)
	return "wh-" + hex.EncodeToString(h.Sum(nil))
}

func (h *WebhookHandler) trigger(ctx context.Context, crewID, agentID string, payload webhook.WebhookPayload) error {
	h.logger.Info("webhook trigger", "crew_id", crewID, "agent_id", agentID, "event", payload.Event)

	// 1. Resolve agent config. workspaceID is "" here: the webhook path has
	// no caller-supplied tenant scope before resolve, and the request was
	// already authenticated against THIS agent's per-agent webhook secret
	// (now also crew-scoped via lookupSecret), so a by-agent resolve is
	// sound. The workspace then comes back on info.WorkspaceID for dedup.
	info, err := h.resolver.ResolveAgent(ctx, agentID, "")
	if err != nil {
		return fmt.Errorf("resolve agent: %w", err)
	}

	// Idempotency: dedup webhook re-deliveries. Prefer the caller's
	// Idempotency-Key header (stashed into ctx by ServeHTTP); fall back to
	// a synthetic key derived from agent id + payload so a provider that
	// re-fires the same event without a key still collapses to one run.
	// A matched key short-circuits before any container/run side-effect.
	idemKey := agentWebhookIdempotencyKey(ctx, agentID, payload)
	if h.idem != nil && info.WorkspaceID != "" {
		runID := generateCUID()
		resolvedID, isNew, idErr := h.idem.LookupOrReserve(
			ctx, info.WorkspaceID, idemKey, runID,
			webhookIdempotencyPipelineID, pipeline.DefaultIdempotencyTTL)
		switch {
		case errors.Is(idErr, nil) && !isNew:
			// Duplicate delivery — original run owns the result.
			h.logger.Info("webhook dedup: duplicate delivery short-circuited",
				"agent_id", agentID, "original_run_id", resolvedID)
			return nil
		case idErr != nil:
			// Idempotency failure must not drop a legitimate webhook —
			// log and fall through to dispatch (at-least-once beats
			// silently swallowing the event).
			h.logger.Warn("webhook dedup: reservation failed, dispatching anyway",
				"agent_id", agentID, "error", idErr)
		}
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
		ID:          info.CrewID,
		Slug:        info.CrewSlug,
		MemoryMB:    info.MemoryMB,
		CPUs:        info.CPUs,
		Image:       info.RuntimeImage,
		CachedImage: info.CachedImage,
	})
	if err != nil {
		return fmt.Errorf("ensure crew runtime: %w", err)
	}

	// 4. Create run record. generateCUID (not UnixNano) — two webhooks that
	// land in the same nanosecond tick would otherwise mint identical run
	// ids and collide on the runs PK.
	runID := generateCUID()
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
	// WithoutCancel preserves the request's OTel trace span + auth values so
	// the async run remains observable, while shedding the request's
	// cancellation -- the webhook library's handler returns to the caller
	// before this goroutine finishes, and the request's ctx is cancelled
	// once the response flushes. The `ctx` parameter is the request ctx
	// threaded in from the webhook handler.
	parentCtx := context.WithoutCancel(ctx)
	go func() {
		runCtx, cancel := context.WithTimeout(parentCtx, 10*time.Minute)
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
		// Guard against running while a backup holds the workspace
		// lock — otherwise this run's state would be written behind
		// the in-flight dump and miss from the bundle.
		var err error
		guardRelease, guardErr := refuseIfBackupInProgress(runCtx, h.db, req.WorkspaceID)
		if guardErr != nil {
			h.logger.Warn("webhook run refused — backup in progress", "workspace_id", req.WorkspaceID)
			err = guardErr
		} else {
			// Defer release so the guard is freed even if RunAgent
			// panics. Matches assignments.go and query_handler.go.
			defer guardRelease()
			err = h.orch.RunAgent(runCtx, req, handler)
		}

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
