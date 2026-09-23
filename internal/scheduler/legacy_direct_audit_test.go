package scheduler

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/crewstart"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/logcollector"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

// Legacy direct execution is retained only as an audit fixture for historical
// regression tests. No production callback can call it after the cutover.
// occurrenceBucket returns the identity of the occurrence currently firing —
// the agent's schedule_next_run (the due timestamp that triggered this fire).
// This is shared state in the DB, so two replicas (or a duplicate tick) firing
// the same occurrence derive the same idempotency key regardless of local
// clock, and it is stable across a mid-run restart. Normalized to UTC RFC3339
// so equivalent spellings collapse to one key. Falls back to the current
// wall-clock minute only when a successful read finds schedule_next_run absent (e.g. a force-fired
// agent with no persisted schedule), which still dedupes a same-minute
// duplicate on a single instance.
func (s *Scheduler) occurrenceBucket(ctx context.Context, agentID string) (string, error) {
	if s.db != nil {
		var nextRun sql.NullString
		if err := s.db.QueryRowContext(ctx,
			`SELECT schedule_next_run FROM agents WHERE id = ?`, agentID).Scan(&nextRun); err != nil {
			// A missing agent or unreadable DB is not a missing schedule value.
			// Inventing a wall-clock key here can bypass an existing reservation
			// if the following write succeeds after a transient read failure.
			return "", fmt.Errorf("read scheduled occurrence for %s: %w", agentID, err)
		}
		if nextRun.Valid && nextRun.String != "" {
			if t, perr := time.Parse(time.RFC3339, nextRun.String); perr == nil {
				return t.UTC().Format(time.RFC3339), nil
			}
			return nextRun.String, nil
		}
	}
	return s.nowFn().UTC().Truncate(time.Minute).Format(time.RFC3339), nil
}

func (s *Scheduler) triggerAgent(ag scheduledAgent) {
	// Leader gate: on a multi-replica deploy only the lease holder fires, so
	// the same cron occurrence isn't run on every replica. The idempotency
	// reservation below is the second line of defence; this skips the wasted
	// work (and the container spin-up) up front. Nil gate always passes.
	if !s.isLeader() {
		return
	}

	// Concurrency bound (#1668). Placed here rather than in addEntry's closure
	// because UpdateSchedule registers a second closure of its own, and a
	// bound the two registration sites have to remember is a bound that the
	// third one will not have.
	//
	// Blocking, not skipping: the occurrence is genuinely due, and the right
	// answer to a busy host is "later", not "never". The wait costs a parked
	// goroutine, which is what robfig/cron has already spent by getting here.
	releaseDispatch, ok := s.acquireDispatchSlot()
	if !ok {
		return // shutting down
	}
	defer releaseDispatch()

	ctx, cancel := context.WithTimeout(s.ctx, 45*time.Minute)
	defer cancel()

	s.logger.Info("scheduled trigger", "agent", ag.Slug, "crew", ag.CrewSlug)

	chatID := generateID()
	runID := generateID()
	prompt := ag.Prompt
	if prompt == "" {
		prompt = "This is a scheduled run. Execute your primary tasks."
	}

	// 0. Fire idempotency (#816). This scheduler fires through the
	// orchestrator directly, so — unlike the pipeline scheduler, which dedupes
	// at the executor's LookupOrReserve chokepoint — it must reserve the
	// occurrence itself before any side effect. Without this, a duplicate tick
	// (double registration of the same schedule) or a process restart after a
	// run started but before updateTimestamps advanced schedule_next_run would
	// re-fire the SAME occurrence and produce a second agent run. The key is
	// (agent-sched, agentID, occurrence): a re-fire of the same occurrence
	// dedupes while the next occurrence — a distinct bucket — fires normally.
	// The occurrence bucket is schedule_next_run (the due timestamp that
	// triggered this fire), read from the DB: it is SHARED state, so two
	// replicas firing the same occurrence — even with skewed clocks that
	// straddle a minute boundary — read the identical value and derive the
	// identical key (a wall-clock minute bucket would diverge and double-fire).
	// It is also stable across a mid-run restart (updateTimestamps advances it
	// only after the run completes). Shares the pipeline_run_idempotency table +
	// key scheme with the three other firing paths (#788).
	var idemKey string
	if s.idem != nil && ag.Workspace != "" {
		bucket, err := s.occurrenceBucket(ctx, ag.ID)
		if err != nil {
			s.logger.Error("scheduled: occurrence could not be read; not starting agent", "agent_id", ag.ID, "error", err)
			return
		}
		idemKey = pipeline.ScheduledFireIdempotencyKey("agent-sched", ag.ID, bucket)
		_, isNew, err := s.idem.LookupOrReserve(ctx, ag.Workspace, idemKey, runID, ag.ID, pipeline.DefaultIdempotencyTTL)
		if err != nil {
			// Fail closed — a missing/unreachable store must not cause a
			// double-fire. Skip this occurrence; the next tick retries. Do NOT
			// advance timestamps: the occurrence didn't run.
			s.logger.Error("scheduled: idempotency reserve failed, skipping fire", "agent", ag.Slug, "error", err)
			return
		}
		if !isNew {
			// Another tick/replica already owns this occurrence. Skip silently
			// (no run record, no timestamp advance — the owner handles both).
			s.logger.Info("scheduled: duplicate occurrence deduped, skipping fire", "agent", ag.Slug, "run_id", runID)
			return
		}
	}
	// releaseReservation frees the occurrence key so a legitimate retry isn't
	// poisoned when the fire fails BEFORE the agent run actually starts
	// (mirrors the executor's Forget-on-early-reject). Once RunAgent is
	// invoked the reservation stands.
	releaseReservation := func() {
		if idemKey != "" && s.idem != nil {
			if fErr := s.idem.Forget(ctx, ag.Workspace, ag.ID, idemKey); fErr != nil {
				s.logger.Warn("scheduled: failed to release idempotency reservation", "agent", ag.Slug, "error", fErr)
			}
		}
	}

	// Cross-surface exclusivity (#2269 follow-up, defect 6): claimed here,
	// after the idempotency reservation (so releaseReservation can free it
	// on a bounce, letting the NEXT tick retry the same occurrence) but
	// before any real work — chat creation, container start — is spent on
	// an occurrence that can't run yet. A busy agent is not a failure: skip
	// this occurrence and let the schedule fire again normally.
	if s.agentRunLock != nil {
		if !s.agentRunLock.TryStart(ag.ID) {
			s.logger.Info("scheduled: agent busy with another run, skipping this occurrence",
				"agent", ag.Slug, "agent_id", ag.ID)
			releaseReservation()
			s.updateTimestamps(ag.ID, ag.Cron, true)
			return
		}
		defer s.agentRunLock.End(ag.ID)
	}

	// 1. Create chat
	if err := s.resolver.CreateChat(ctx, chatbridge.CreateChatRequest{
		ChatID:      chatID,
		AgentID:     ag.ID,
		WorkspaceID: ag.Workspace,
		Title:       fmt.Sprintf("Scheduled: %s", ag.Name),
	}); err != nil {
		s.logger.Error("scheduled: create chat failed", "agent", ag.Slug, "error", err)
		releaseReservation()
		s.updateTimestamps(ag.ID, ag.Cron, true)
		return
	}

	// 2. Resolve chat → full ChatInfo (credentials, system prompt, skills, etc.)
	info, err := s.resolver.ResolveChat(ctx, chatID)
	if err != nil {
		s.logger.Error("scheduled: resolve chat failed", "agent", ag.Slug, "error", err)
		releaseReservation()
		s.updateTimestamps(ag.ID, ag.Cron, true)
		return
	}

	// 3. Ensure container is running
	var containerID string
	if s.container != nil {
		crewID := info.CrewID
		crewSlug := info.CrewSlug
		if crewID == "" {
			crewID = "scheduler-" + ag.Workspace
			crewSlug = "scheduler"
		}
		// Same assembly the chat path uses (chatbridge.ChatInfo.CrewRuntimeConfig)
		// — including the crew's declared sidecars, which a scheduled run used to
		// start without, so a nightly routine on a crew with `services: [redis]`
		// ran against nothing (#1708).
		cfg, cfgErr := info.CrewRuntimeConfig(s.cfg.DefaultMemoryMB, s.cfg.DefaultCPUs)
		if cfgErr != nil {
			s.logger.Warn("scheduled: crew services unresolved, starting without them",
				"agent", ag.Slug, "crew_id", crewID, "error", cfgErr)
		}
		// An agent with no crew gets a synthetic per-workspace one; it has no
		// crews row, no image and no services, which the starter tolerates.
		cfg.ID = crewID
		cfg.Slug = crewSlug
		cID, err := crewstart.New(s.container, nil, s.logger).Start(ctx, cfg)
		if err != nil {
			s.logger.Error("scheduled: container failed", "agent", ag.Slug, "error", err)
			releaseReservation()
			s.updateTimestamps(ag.ID, ag.Cron, true)
			return
		}
		containerID = cID
	}

	// 4. Persist user message to conversation store
	if s.convStore != nil {
		_ = s.convStore.Append(ctx, chatID, conversation.Message{
			ID:        generateID(),
			Role:      conversation.RoleUser,
			Content:   prompt,
			Timestamp: time.Now().UTC(),
		})
	}

	// 5. Build AgentRunRequest through the ONE request-builder (#810). The
	// scheduler previously hand-built this literal and silently dropped
	// MCPServers, Skills, RoleTitle, MCP-JSON, and MemoryMB/CPUs/TTL — so a
	// cron-dispatched agent ran tool-blind and without its resource limits.
	// Funnelling through ToAgentRunRequest carries the full field-set
	// (incl. the crew-policy ApprovalMode that revives the HITL gate).
	req := info.ToAgentRunRequest(chatbridge.AgentRunOverrides{
		ChatID:      chatID,
		ContainerID: containerID,
		UserMessage: prompt,
		LLMModel:    info.LLMModel,
		TimeoutSecs: info.TimeoutSecs,
		MemoryMB:    info.MemoryMB,
		CPUs:        info.CPUs,
		// Unattended runs get a tighter turn cap than interactive chat — no
		// human is watching a scheduled job, so a stuck loop would otherwise
		// burn to the wall-clock timeout. See orchestrator.RoutineMaxTurns.
		MaxTurns: orchestrator.RoutineMaxTurns,
	})
	// E0: pass through the run id already minted above — the same one
	// CreateRun records and journal.WithRunID stamps on every entry beneath
	// this run. It is what the orchestrator derives this run's tmux session
	// and /tmp file set from, so a cron tick landing while a chat run of the
	// same agent is live no longer overwrites it.
	req.RunID = runID

	// 6. Create run record
	runMeta := map[string]interface{}{
		"cli_adapter": info.CLIAdapter,
		"crew_id":     info.CrewID,
		"crew_slug":   info.CrewSlug,
		"agent_slug":  info.AgentSlug,
		"tags":        []string{"scheduled", info.CLIAdapter},
	}
	if err := s.resolver.CreateRun(ctx, runID, ag.ID, chatID, ag.Workspace, "SCHEDULED", runMeta); err != nil {
		// The IPC response can be lost after the record committed. Do not
		// execute an unrecorded run, or release the occurrence reservation
		// and mint a second identity for a potentially committed record.
		// Keep the due timestamp unchanged: no run has executed. The legacy
		// scheduler has no reconciliation queue; durable migration must give
		// this ambiguous write an explicit operator-visible state.
		s.logger.Error("scheduled: run record write failed; not starting agent; occurrence reservation retained",
			"agent_id", ag.ID, "run_id", runID, "idempotency_key", idemKey, "error", err)
		return
	}
	// Put the run on the context so every journal entry emitted beneath it
	// inherits trace_id = runID — the orchestrator's JournalEntry has no
	// TraceID field of its own and reads the id from here. Without this,
	// `crewship journal --run-id <id>` finds nothing for a SCHEDULED run,
	// including the run.session_init entry that fires at severity error when
	// the CLI dropped an MCP server at startup. That alert exists for exactly
	// this kind of run: nobody is watching a cron job, so it has to be
	// reachable from the run afterwards.
	ctx = journal.WithRunID(ctx, runID)

	// 7. Run agent
	startedAt := time.Now()

	var logBuf *logcollector.OutputBuffer
	if s.logWriter != nil {
		logBuf = logcollector.NewOutputBuffer(s.logWriter, info.CrewID, info.AgentSlug)
		defer logBuf.Close()
	}

	handler, acc := orchestrator.NewBufferingHandler(orchestrator.BufferingHandlerOpts{
		LogBuf:            logBuf,
		AgentSlug:         info.AgentSlug,
		AccumulateText:    true,
		CaptureResultMeta: true,
	})

	runErr := s.orch.RunAgent(ctx, req, handler)

	// 8. Update run record
	completedMeta := map[string]interface{}{
		"duration_ms": time.Since(startedAt).Milliseconds(),
	}
	// Everything the accumulator captured: usage, denials, and the session
	// provenance that says which CLI binary answered, which credential path it
	// used, and whether an MCP server was dropped on the way in. Nobody
	// watches a scheduled run, so the run record is the only place those
	// answers can come from afterwards (#1934). resolvedModel is session-init
	// ground truth for what the API served — what the ledger below bills.
	resolvedModel := orchestrator.MergeRunAccumulator(completedMeta, acc, info.LLMModel)

	// Forward the CLI-reported token usage to the paymaster ledger (#1205).
	// See chatbridge.resultUsageForLedger's doc for why this adapter-side
	// write is needed in addition to the sidecar's own HTTP-level cost
	// observation (which can't see OAuth-tunneled or streaming traffic).
	// Best-effort: never blocks the scheduled run from completing.
	if usage, ok := chatbridge.ResultUsageForLedger(info.WorkspaceID, info.CrewID, ag.ID, resolvedModel, acc.ResultMeta()); ok {
		if err := s.resolver.RecordCost(ctx, usage); err != nil {
			s.logger.Warn("failed to record run cost usage", "run_id", runID, "error", err)
		}
	}

	if runErr != nil {
		if errors.Is(runErr, orchestrator.ErrDetachedStillRunning) {
			// Nonterminal (#2626): the exec is still alive and the
			// orchestrator holds the run at `running` — it also already
			// attempted to stop the wedged exec inside RunAgent's own
			// ownership boundary, before any slot this goroutine holds was
			// at risk. Neither FAILED nor COMPLETED is true; leave the run
			// nonterminal for an operator to reconcile.
			s.logger.Warn("scheduled run's exec detached after RunAgent's stop attempt; leaving the run nonterminal",
				"agent", ag.Slug, "error", runErr, "duration_ms", completedMeta["duration_ms"])
		} else {
			errMsg := runErr.Error()
			if err := s.resolver.UpdateRun(ctx, runID, "FAILED", nil, &errMsg, completedMeta); err != nil {
				s.logger.Warn("failed to update run status", "run_id", runID, "status", "FAILED", "error", err)
			}
			s.logger.Error("scheduled run failed", "agent", ag.Slug, "error", runErr, "duration_ms", completedMeta["duration_ms"])
		}
	} else {
		exitCode := 0
		if err := s.resolver.UpdateRun(ctx, runID, "COMPLETED", &exitCode, nil, completedMeta); err != nil {
			s.logger.Warn("failed to update run status", "run_id", runID, "status", "COMPLETED", "error", err)
		}
		s.logger.Info("scheduled run completed", "agent", ag.Slug, "duration_ms", completedMeta["duration_ms"])
	}

	// Persist assistant response
	if s.convStore != nil && acc.Text() != "" {
		_ = s.convStore.Append(ctx, chatID, conversation.Message{
			ID:        generateID(),
			Role:      conversation.RoleAssistant,
			Content:   acc.Text(),
			Timestamp: time.Now().UTC(),
		})
		_ = s.resolver.IncrementMessageCount(ctx, chatID, 2)
	}

	// 9. Update schedule timestamps
	s.updateTimestamps(ag.ID, ag.Cron, false)
}

func (s *Scheduler) updateTimestamps(agentID, cronExpr string, errorOnly bool) {
	now := time.Now().UTC().Format(time.RFC3339)

	var nextRun *string
	if sched, err := s.parser.Parse(cronExpr); err == nil {
		next := sched.Next(time.Now()).UTC().Format(time.RFC3339)
		nextRun = &next
	} else {
		// A failed parse here means the cron stored on the agent row no
		// longer parses (stored row was corrupted, validator drift, etc.).
		// Without an explicit signal, schedule_next_run silently freezes
		// at its stale value. Log loudly and clear next_run so the UI
		// reflects the real state instead of pointing at a long-past
		// timestamp.
		s.logger.Warn("schedule cron unparsable; clearing schedule_next_run",
			"agent_id", agentID, "cron", cronExpr, "error", err)
		if _, err := s.db.ExecContext(s.ctx,
			"UPDATE agents SET schedule_next_run = NULL WHERE id = ?", agentID); err != nil {
			s.logger.Warn("clear schedule_next_run", "agent_id", agentID, "error", err)
		}
	}

	// Use the scheduler's lifecycle ctx for all timestamp DB writes so a
	// Stop() during shutdown short-circuits in-flight UPDATEs cleanly
	// instead of racing the DB pool close. context.Background here meant
	// shutdown could log "use of closed connection" warnings even on a
	// graceful stop.
	if errorOnly {
		if nextRun != nil {
			if _, err := s.db.ExecContext(s.ctx, "UPDATE agents SET schedule_next_run = ? WHERE id = ?", *nextRun, agentID); err != nil {
				s.logger.Warn("update schedule_next_run", "agent_id", agentID, "error", err)
			}
		}
		return
	}

	if nextRun != nil {
		if _, err := s.db.ExecContext(s.ctx, "UPDATE agents SET schedule_last_run = ?, schedule_next_run = ? WHERE id = ?",
			now, *nextRun, agentID); err != nil {
			s.logger.Warn("update schedule timestamps", "agent_id", agentID, "error", err)
		}
	} else {
		if _, err := s.db.ExecContext(s.ctx, "UPDATE agents SET schedule_last_run = ? WHERE id = ?", now, agentID); err != nil {
			s.logger.Warn("update schedule_last_run", "agent_id", agentID, "error", err)
		}
	}
}

func generateID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	// Direct byte-append: "sched_" + <unix-nano> + "_" + 24 hex chars.
	// Previous fmt.Sprintf + hex.EncodeToString chain paid 4 heap
	// allocations per call; this shape needs just the final string.
	var buf [64]byte
	out := append(buf[:0], "sched_"...)
	out = strconv.AppendInt(out, time.Now().UnixNano(), 10)
	out = append(out, '_')
	out = hex.AppendEncode(out, b)
	return string(out)
}
