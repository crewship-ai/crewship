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

// defaultAgentWebhookRatePerMin caps how many agent-webhook RunAgent
// dispatches a single agent can trigger in a 60s window. R4#3: each
// delivery spawns a 10-min RunAgent, and distinct Idempotency-Keys bypass
// the dedup window, so without a per-agent gate a signed (or distributed)
// sender could fan out unbounded concurrent runs against one agent. The
// pipeline side has the equivalent guard via pipeline.AllowWebhookFire;
// this is its agent-webhook mirror, keyed by agent id instead of token.
//
// Generous on purpose — it throttles abuse, not normal use. 60/min ≈
// one run/second per agent is far above any legitimate single-agent
// webhook cadence; the cap exists to deny a flood, not to rate-shape
// healthy traffic.
const defaultAgentWebhookRatePerMin = 60

// defaultAgentWebhookMaxConcurrent caps how many agent-webhook runs a
// single agent may have IN FLIGHT at once (process-local). RunAgent
// holds the slot for up to its 10-min timeout; the registry frees it on
// completion. This is the second layer of the R4#3 gate: even within the
// per-minute budget, a burst that all lands before any run finishes can't
// pin more than this many concurrent 10-min runs on one agent.
const defaultAgentWebhookMaxConcurrent = 8

// agentWebhookConcurrencyKey is the synthetic concurrency-key prefix for
// agent-webhook runs in the shared RunRegistry. Keyed per agent so the
// in-flight cap is per-agent, independent of pipeline runs.
const agentWebhookConcurrencyKey = "agent-webhook"

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

	// agentRatePerMin / agentMaxConcurrent gate agent-webhook dispatch
	// (R4#3). Defaulted in the constructor; overridable (tests tighten
	// them to trip the gate deterministically). agentRuns is the shared
	// in-flight registry backing the concurrency cap.
	agentRatePerMin    int
	agentMaxConcurrent int
	agentRuns          *pipeline.RunRegistry
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
		db:                 db,
		logger:             logger,
		resolver:           resolver,
		orch:               orch,
		hub:                hub,
		container:          container,
		logWriter:          logWriter,
		agentRatePerMin:    defaultAgentWebhookRatePerMin,
		agentMaxConcurrent: defaultAgentWebhookMaxConcurrent,
		agentRuns:          pipeline.NewRunRegistry(),
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

// agentWebhookRateKey namespaces the per-agent rate window inside the
// shared pipeline rate limiter so an agent id can never collide with a
// pipeline webhook token (which is an opaque hex string). The "awh:"
// prefix marks the agent-webhook namespace.
func agentWebhookRateKey(agentID string) string {
	return "awh:" + agentID
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

	// Mint the run id ONCE, up front. R6: this exact id is what we reserve
	// in the idempotency table AND what we hand to CreateRun, so the
	// idempotency row maps the event to the run that actually exists.
	// generateCUID (not UnixNano) — two webhooks that land in the same
	// nanosecond tick would otherwise mint identical ids and collide on
	// the runs PK. Mirrors the pipeline executor's pre-allocated id.
	runID := generateCUID()

	// Idempotency: dedup webhook re-deliveries. Prefer the caller's
	// Idempotency-Key header (stashed into ctx by ServeHTTP); fall back to
	// a synthetic key derived from agent id + payload so a provider that
	// re-fires the same event without a key still collapses to one run.
	// A matched key short-circuits before any container/run side-effect.
	idemKey := agentWebhookIdempotencyKey(ctx, agentID, payload)
	if h.idem != nil && info.WorkspaceID != "" {
		resolvedID, isNew, idErr := h.idem.LookupOrReserve(
			ctx, info.WorkspaceID, idemKey, runID,
			webhookIdempotencyPipelineID, pipeline.DefaultIdempotencyTTL)
		switch {
		case errors.Is(idErr, nil) && !isNew:
			// Duplicate delivery — original run owns the result. Return the
			// reserved (original) run id's outcome by short-circuiting; we
			// do NOT dispatch, do NOT create a second run.
			h.logger.Info("webhook dedup: duplicate delivery short-circuited",
				"agent_id", agentID, "original_run_id", resolvedID)
			return nil
		case errors.Is(idErr, nil) && isNew:
			// Fresh reservation — the run we're about to dispatch uses the
			// id we just reserved (runID). Nothing to do; runID already
			// carries it through to CreateRun below.
		case idErr != nil:
			// Idempotency failure must not drop a legitimate webhook —
			// log and fall through to dispatch (at-least-once beats
			// silently swallowing the event). The reserved/created ids
			// still match because both use the same runID.
			h.logger.Warn("webhook dedup: reservation failed, dispatching anyway",
				"agent_id", agentID, "error", idErr)
		}
	}

	// Rate/concurrency gate (R4#3). A fresh idempotency key gets us here,
	// so dedup alone cannot stop a distributed sender from fanning out
	// distinct-key deliveries into unbounded concurrent 10-min runs. Gate
	// per agent: an M/min rate window plus an N-in-flight concurrency cap.
	// Both are generous (abuse, not normal use) and process-local — a
	// multi-replica deployment would want a shared limiter, but this is a
	// real first layer that a single binary enforces correctly.
	//
	// On reject we Forget the just-made reservation so a legitimate retry
	// with the same key isn't poisoned for the full TTL (mirrors the
	// pipeline executor's Forget-on-concurrency-reject).
	if !pipeline.AllowWebhookFire(agentWebhookRateKey(agentID), h.agentRatePerMin) {
		if h.idem != nil && info.WorkspaceID != "" {
			if fErr := h.idem.Forget(ctx, info.WorkspaceID, idemKey); fErr != nil {
				h.logger.Warn("webhook rate gate: failed to release reservation", "agent_id", agentID, "error", fErr)
			}
		}
		h.logger.Warn("webhook rate gate: agent over per-minute limit, dropping delivery",
			"agent_id", agentID, "limit_per_min", h.agentRatePerMin)
		return fmt.Errorf("webhook: agent %s rate limit exceeded (%d/min)", agentID, h.agentRatePerMin)
	}

	// In-flight concurrency cap (R4#3, second layer) — acquired UP FRONT,
	// before any container warmup or run-record write, so a throttled
	// delivery warms no container and writes no run row (no load
	// amplification). The slot is held for the lifetime of the async run
	// (released in the goroutine below). On reject we Forget the
	// idempotency reservation (mirroring the rate gate) so a redelivery
	// with the same key can retry once capacity frees.
	var releaseSlot func()
	if h.agentRuns != nil {
		_, release, acqErr := h.agentRuns.Acquire(ctx, pipeline.AcquireOpts{
			RunID:          runID,
			WorkspaceID:    info.WorkspaceID,
			ConcurrencyKey: agentWebhookConcurrencyKey + ":" + agentID,
			MaxConcurrent:  h.agentMaxConcurrent,
		})
		if acqErr != nil {
			if h.idem != nil && info.WorkspaceID != "" {
				if fErr := h.idem.Forget(ctx, info.WorkspaceID, idemKey); fErr != nil {
					h.logger.Warn("webhook concurrency gate: failed to release reservation", "agent_id", agentID, "error", fErr)
				}
			}
			h.logger.Warn("webhook concurrency gate: agent at in-flight cap, dropping delivery",
				"agent_id", agentID, "max_concurrent", h.agentMaxConcurrent)
			return fmt.Errorf("webhook: agent %s concurrency limit reached (%d)", agentID, h.agentMaxConcurrent)
		}
		releaseSlot = release
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
		if releaseSlot != nil {
			releaseSlot()
		}
		// Forget the idempotency reservation (mirroring the rate/concurrency
		// gates above): startup failed, so no run was created or started. A
		// redelivery with the same key must be allowed to retry rather than
		// being deduped against a reservation that never produced a run.
		if h.idem != nil && info.WorkspaceID != "" {
			if fErr := h.idem.Forget(ctx, info.WorkspaceID, idemKey); fErr != nil {
				h.logger.Warn("webhook startup failure: failed to release reservation", "agent_id", agentID, "error", fErr)
			}
		}
		return fmt.Errorf("ensure crew runtime: %w", err)
	}

	// 4. Create run record. Reuses the runID minted (and idempotency-
	// reserved) at the top of trigger — R6: the run record and the
	// idempotency reservation share one id, so a redelivery resolves to a
	// run that exists.
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

		// The in-flight concurrency slot was acquired up front (before any
		// container/run work); release it when this run finishes.
		if releaseSlot != nil {
			defer releaseSlot()
		}

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

		base, _ := orchestrator.NewBufferingHandler(orchestrator.BufferingHandlerOpts{
			LogBuf:    logBuf,
			AgentSlug: info.AgentSlug,
		})
		handler := func(event orchestrator.AgentEvent) {
			base(event)

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
