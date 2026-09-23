package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/crewstart"
	"github.com/crewship-ai/crewship/internal/dispatch"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/logcollector"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/scheduler"
	"github.com/crewship-ai/crewship/internal/work"
)

// ScheduledRuntime shares the webhook runtime's launch registry and stop
// protocol. Both sources have the same stable agent-run locator, so a cancel
// or recovery probe must give the same answer whichever source accepted it.
type ScheduledRuntime struct {
	*WebhookRuntime
	convStore *conversation.Store
	memoryMB  int
	cpus      float64
}

func NewScheduledRuntime(base *WebhookRuntime, conv *conversation.Store, memoryMB int, cpus float64) *ScheduledRuntime {
	if memoryMB == 0 {
		memoryMB = 4096
	}
	if cpus == 0 {
		cpus = 2
	}
	return &ScheduledRuntime{WebhookRuntime: base, convStore: conv, memoryMB: memoryMB, cpus: cpus}
}

func (rt *ScheduledRuntime) Run(ctx context.Context, a dispatch.Assignment, started func()) error {
	var in scheduler.ScheduledInput
	if a.Item == nil || json.Unmarshal([]byte(a.Item.InputJSON), &in) != nil ||
		in.Version != 1 || in.AgentID != a.Item.AgentID || in.Occurrence != a.Item.SourceRef {
		return fmt.Errorf("%w: invalid scheduled input", errWebhookInputUnreadable)
	}
	h := rt.h
	// Preserve the legacy scheduler's hard cap even if an agent has no
	// per-run timeout configured. The dispatcher separately owns stop and
	// reconciliation when this deadline fires.
	runCtx, cancel := context.WithTimeout(ctx, 45*time.Minute)
	defer cancel()
	launch := &webhookLaunch{cancel: cancel, workID: a.Item.ID, runID: a.RunID,
		generation: a.Generation, store: work.NewStore(h.db)}
	rt.launches.Store(a.RunID, launch)
	defer func() {
		launch.mu.Lock()
		launch.returned = true
		launch.mu.Unlock()
	}()

	if err := launch.Enter("agent resolution"); err != nil {
		return err
	}
	chatID := "scheduled-" + a.RunID
	if err := h.resolver.CreateChat(runCtx, chatbridge.CreateChatRequest{
		ChatID: chatID, AgentID: in.AgentID, WorkspaceID: a.Item.WorkspaceID,
		Title: "Scheduled agent run",
	}); err != nil {
		return fmt.Errorf("%w: create scheduled chat: %w", errWebhookBeforeAgent, err)
	}
	info, err := h.resolver.ResolveChat(runCtx, chatID)
	if err != nil {
		return fmt.Errorf("%w: resolve scheduled chat: %w", errWebhookBeforeAgent, err)
	}
	if info.AgentID != in.AgentID || info.WorkspaceID != a.Item.WorkspaceID || info.CrewID != a.Item.CrewID {
		return fmt.Errorf("%w: scheduled chat resolved to a different agent, crew or workspace", errWebhookInputUnreadable)
	}
	if err := launch.Enter("crew container start"); err != nil {
		return err
	}
	if h.container == nil {
		return fmt.Errorf("%w: no container provider", errWebhookBeforeAgent)
	}
	cfg, cfgErr := info.CrewRuntimeConfig(rt.memoryMB, rt.cpus)
	if cfgErr != nil {
		h.logger.Warn("scheduled crew services unresolved", "agent_id", in.AgentID, "error", cfgErr)
	}
	if info.CrewID == "" {
		cfg.ID, cfg.Slug = "scheduler-"+a.Item.WorkspaceID, "scheduler"
	}
	containerID, err := crewstart.New(h.container, NewCrewConfigCompleter(h.db), h.logger).Start(runCtx, cfg)
	if err != nil {
		if stopped := launch.Enter("run record"); stopped != nil {
			return stopped
		}
		return fmt.Errorf("%w: start scheduled crew: %w", errWebhookBeforeAgent, err)
	}
	if err := launch.Enter("run record"); err != nil {
		return err
	}
	runMeta := map[string]interface{}{
		"cli_adapter": info.CLIAdapter, "crew_id": info.CrewID,
		"crew_slug": info.CrewSlug, "agent_slug": info.AgentSlug,
		"tags": []string{"scheduled", info.CLIAdapter},
	}
	if err := h.resolver.CreateRun(runCtx, a.RunID, in.AgentID, chatID, a.Item.WorkspaceID, "SCHEDULED", runMeta); err != nil {
		// A failed response does not prove the journal write was absent.
		absent, lookupErr := h.runRecordAbsent(runCtx, a.RunID)
		if lookupErr != nil || !absent {
			return fmt.Errorf("scheduled run record write is unclear: %w (lookup: %v)", err, lookupErr)
		}
		return fmt.Errorf("%w: create scheduled run record: %w", errWebhookBeforeAgent, err)
	}

	if rt.convStore != nil {
		_ = rt.convStore.Append(runCtx, chatID, conversation.Message{
			ID: a.RunID + "-prompt", Role: conversation.RoleUser,
			Content: in.Prompt, Timestamp: time.Now().UTC(),
		})
	}
	req := info.ToAgentRunRequest(chatbridge.AgentRunOverrides{
		ChatID: chatID, ContainerID: containerID, UserMessage: in.Prompt,
		LLMModel: info.LLMModel, TimeoutSecs: info.TimeoutSecs,
		MemoryMB: info.MemoryMB, CPUs: info.CPUs,
		MaxTurns: orchestrator.RoutineMaxTurns,
	})
	req.RunID = a.RunID
	location := orchestrator.RunLocation{ContainerID: req.ContainerID, AgentSlug: req.AgentSlug, RunID: req.RunID}
	if err := launch.Launch(location); err != nil {
		stageCtx, stageCancel := context.WithTimeout(context.WithoutCancel(runCtx), 30*time.Second)
		defer stageCancel()
		message := err.Error()
		if stageErr := launch.RecordResult(stageCtx, work.RunResult{ErrorMessage: &message}); stageErr != nil {
			return fmt.Errorf("%w: %v", work.ErrRunResultUnstored, stageErr)
		}
		return err
	}
	req.ExecGate = launch.RequestCreation

	var logBuf *logcollector.OutputBuffer
	if h.logWriter != nil {
		logBuf = logcollector.NewOutputBuffer(h.logWriter, info.CrewID, info.AgentSlug)
		defer logBuf.Close()
	}
	base, acc := orchestrator.NewBufferingHandler(orchestrator.BufferingHandlerOpts{
		LogBuf: logBuf, AgentSlug: info.AgentSlug, AccumulateText: true, CaptureResultMeta: true,
	})
	var once sync.Once
	handler := func(event orchestrator.AgentEvent) {
		once.Do(func() {
			launch.mu.Lock()
			launch.confirmed = true
			launch.mu.Unlock()
			if started != nil {
				started()
			}
		})
		base(event)
	}
	runCtx = journal.WithRunID(runCtx, a.RunID)
	startedAt := time.Now()
	runErr := h.orch.RunAgent(runCtx, req, handler)
	meta := map[string]interface{}{"duration_ms": time.Since(startedAt).Milliseconds()}
	resolvedModel := orchestrator.MergeRunAccumulator(meta, acc, info.LLMModel)
	var exitCode *int
	var errMsg *string
	if !errors.Is(runErr, orchestrator.ErrDetachedStillRunning) {
		code := 0
		if runErr != nil {
			code = 1
			message := runErr.Error()
			errMsg = &message
		}
		if !launch.StoppedBeforeCreation() {
			exitCode = &code
		}
	}
	settleCtx, settleCancel := context.WithTimeout(context.WithoutCancel(runCtx), 30*time.Second)
	defer settleCancel()
	if err := launch.RecordResult(settleCtx, work.RunResult{ExitCode: exitCode, ErrorMessage: errMsg, Metadata: meta}); err != nil {
		return fmt.Errorf("scheduled run result not preserved: %w: %w", work.ErrRunResultUnstored, err)
	}
	if usage, ok := chatbridge.ResultUsageForLedger(info.WorkspaceID, info.CrewID, in.AgentID, resolvedModel, acc.ResultMeta()); ok {
		if err := h.resolver.RecordCost(settleCtx, usage); err != nil {
			h.logger.Warn("failed to record scheduled cost", "run_id", a.RunID, "error", err)
		}
	}
	if rt.convStore != nil && acc.Text() != "" {
		_ = rt.convStore.Append(settleCtx, chatID, conversation.Message{
			ID: a.RunID + "-reply", Role: conversation.RoleAssistant,
			Content: acc.Text(), Timestamp: time.Now().UTC(),
		})
		_ = h.resolver.IncrementMessageCount(settleCtx, chatID, 2)
	}
	launch.mu.Lock()
	wasRequested := launch.requested
	launch.mu.Unlock()
	if wasRequested {
		if _, err := h.db.ExecContext(settleCtx,
			`UPDATE agents SET schedule_last_run = ? WHERE id = ?`, startedAt.UTC().Format(time.RFC3339), in.AgentID); err != nil {
			h.logger.Warn("scheduled last-run timestamp update failed", "agent_id", in.AgentID, "error", err)
		}
	}
	return runErr
}

var _ dispatch.Runtime = (*ScheduledRuntime)(nil)
