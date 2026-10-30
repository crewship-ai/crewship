package api

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/crewstart"
	"github.com/crewship-ai/crewship/internal/dispatch"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/mooring"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/quiesce"
	"github.com/crewship-ai/crewship/internal/runauthority"
	"github.com/crewship-ai/crewship/internal/work"
)

var errNativeAgentNoReceipt = errors.New("native agent finished without a committed native receipt")

type craftNativeAgentRuntime struct {
	*WebhookRuntime
	manager     *craftNativeAgentManager
	environment collaborationAgentPrivateEnvironment
}

func (m *craftNativeAgentManager) start(h *WebhookHandler) error {
	if !m.enabled || m.human == nil || !m.human.enabled || m.human.clock == nil || m.config.AgentApply == nil || m.config.AgentInspect == nil || h == nil || h.db != m.human.pages.db || (collaborationDocumentEnvironment{h}).BeforeStart(m.ctx) != nil {
		return orchestrator.ErrPrivateRunUnqualified
	}
	rt := &craftNativeAgentRuntime{WebhookRuntime: NewWebhookRuntime(h), manager: m, environment: collaborationDocumentEnvironment{h}}
	d := dispatch.New(work.NewStore(h.db), rt, m, dispatch.Config{Owner: "native-craft-task-dispatcher", Kinds: []work.Kind{{Source: work.SourceChat, DomainKind: craftNativeAgentDomain}}, Limits: work.SerialAgentLimits(), PollInterval: 2 * time.Second, ConfirmPollInterval: time.Second, Paused: quiesce.QueuePaused}, h.logger)
	m.mu.Lock()
	if m.ctx.Err() != nil || m.runtime != nil {
		m.mu.Unlock()
		return errCraftNativeAgentDenied
	}
	m.runtime, m.dispatcher, m.done = rt, d, make(chan struct{})
	done := m.done
	m.mu.Unlock()
	finish := beginBackgroundWork()
	go func() { defer finish(); defer close(done); _ = d.Run(m.ctx) }()
	return nil
}

func (m *craftNativeAgentManager) Close() {
	if m == nil {
		return
	}
	m.cancel()
	m.mu.Lock()
	for _, e := range m.entries {
		e.closed = true
	}
	done := m.done
	m.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
}

func (rt *craftNativeAgentRuntime) Classify(_ dispatch.Assignment, err error) dispatch.Outcome {
	if err == nil {
		return dispatch.OutcomeSucceeded
	}
	if errors.Is(err, errNativeAgentNoReceipt) {
		return dispatch.OutcomeFailed
	}
	return dispatch.OutcomeUnclear
}

// Work terminal state is projected only after this private leaf's own proof;
// generic webhook output must never receive private native data.
func (rt *craftNativeAgentRuntime) FlushRunOutcomes(ctx context.Context) error {
	if !rt.projectionMu.TryLock() {
		return nil
	}
	defer rt.projectionMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	store := work.NewStore(rt.h.db)
	// Restrict the existing durable outbox to this process's sealed runs.
	// Foreign pending entries must neither be modified nor consume our batch.
	rt.manager.mu.Lock()
	own := make(map[string]*craftNativeAgentEntry, len(rt.manager.runs))
	for id, e := range rt.manager.runs {
		if e != nil && e.seal == rt.manager.seal {
			own[id] = e
		}
	}
	rt.manager.mu.Unlock()
	type pending struct{ id, order string }
	var ready []pending
	for id := range own {
		var order string
		err := rt.h.db.QueryRowContext(ctx, `SELECT COALESCE(run_projection_attempted_at,started_at) FROM work_attempts WHERE run_id=? AND run_result_json IS NOT NULL AND run_status!='' AND run_projected_at IS NULL`, id).Scan(&order)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		ready = append(ready, pending{id, order})
	}
	sort.Slice(ready, func(i, j int) bool {
		if ready[i].order == ready[j].order {
			return ready[i].id < ready[j].id
		}
		return ready[i].order < ready[j].order
	})
	var failures []error
	for _, item := range ready {
		id, e := item.id, own[item.id]
		p, owned, err := store.RunProjection(ctx, id)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if !owned || !p.Ready || p.WorkspaceID != e.grant.Scope.WorkspaceID || p.AgentID != e.grant.AgentID || p.CrewID != e.grant.CrewID {
			continue
		}
		if err = store.MarkRunProjectionAttempt(ctx, id); err == nil {
			err = rt.h.resolver.UpdateRun(ctx, id, p.Status, p.Result.ExitCode, p.Result.ErrorMessage, p.Result.Metadata)
		}
		if err == nil {
			err = store.MarkRunProjected(ctx, id, p.Status)
		}
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (rt *craftNativeAgentRuntime) Run(ctx context.Context, a dispatch.Assignment, started func()) (err error) {
	defer func() {
		if recover() != nil {
			err = orchestrator.ErrPrivateRunUnqualified
		}
	}()
	if rt == nil || rt.manager == nil || rt.environment == nil || started == nil {
		return orchestrator.ErrPrivateRunUnqualified
	}
	m := rt.manager
	if rt.environment.BeforeStart(ctx) != nil {
		return orchestrator.ErrPrivateRunUnqualified
	}
	if _, err = m.Authorize(ctx, a); err != nil {
		return err
	}
	e := m.assignmentEntry(a)
	if e == nil {
		return errCraftNativeAgentDenied
	}
	g := e.grant
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	runCtx = runauthority.WithOriginID(runCtx, "")
	runCtx = chataudience.WithReceipt(runCtx, &g.Original)
	runCtx = runauthority.WithTriggerMessage(runCtx, g.MessageID)
	runCtx = journal.WithRunID(runCtx, a.RunID)
	launch := &webhookLaunch{cancel: cancel, workID: e.workID, runID: a.RunID, generation: a.Generation, store: work.NewStore(rt.h.db)}
	launch.requestCreation = func(call context.Context) error { return rt.capturePrincipal(call, e, a, true) }
	if _, loaded := rt.launches.LoadOrStore(a.RunID, launch); loaded {
		return errCraftNativeAgentDenied
	}
	defer func() { launch.mu.Lock(); launch.returned = true; launch.mu.Unlock() }()
	info, err := rt.h.resolver.ResolveChat(runCtx, g.Original.ChatID)
	if err != nil || info == nil || info.AgentID != g.AgentID || info.CrewID != g.CrewID || info.WorkspaceID != g.Scope.WorkspaceID || info.CLIAdapter != "CLAUDE_CODE" || info.AgentStatus == chatbridge.AgentStatusPendingReview {
		return errCraftNativeAgentDenied
	}
	if err = launch.Enter("native Craft private crew start"); err != nil {
		return err
	}
	cfg, err := info.CrewRuntimeConfig(4096, 2)
	if err != nil {
		return err
	}
	use, _, err := crewstart.New(rt.h.container, NewCrewConfigCompleter(rt.h.db), rt.h.logger).StartUse(runCtx, cfg, nil)
	if err != nil || use == nil {
		return orchestrator.ErrPrivateRunUnqualified
	}
	defer use.Release()
	if err = rt.environment.Qualify(runCtx, *info, use); err != nil {
		return err
	}
	if err = rt.capturePrincipal(runCtx, e, a, false); err != nil {
		return err
	}
	if err = rt.h.resolver.CreateRun(runCtx, a.RunID, g.AgentID, g.Original.ChatID, g.Scope.WorkspaceID, "CHAT", map[string]interface{}{"native_craft_task": true}); err != nil {
		return err
	}
	prompt := "Use only the provided native Craft tools for this explicit whole-document task. Acquire the lease first; if a human holds it, never steal or retry: read the current version and call craft_propose with the requested edit over that exact base_version, then report the proposal for the human to review. Inspect, apply only the requested edit, retain the original request_id for uncertain results, and release after a committed receipt. Never claim Saved without that receipt.\n\n" + g.Instruction
	req := info.ToAgentRunRequest(chatbridge.AgentRunOverrides{ChatID: g.Original.ChatID, ContainerID: use.ContainerID(), UserMessage: prompt, LLMModel: info.LLMModel, TimeoutSecs: info.TimeoutSecs, MemoryMB: info.MemoryMB, CPUs: info.CPUs, MaxTurns: orchestrator.RoutineMaxTurns}).WithStrictContentPrivacy()
	req.RunID, req.RuntimeUse = a.RunID, use
	req, err = req.WithNativeCraftTools()
	if err != nil {
		return err
	}
	location := orchestrator.RunLocation{ContainerID: req.ContainerID, AgentSlug: req.AgentSlug, RunID: a.RunID}
	if err = launch.Launch(location); err != nil {
		return err
	}
	req.ExecGate = func(call context.Context) error { return requestWebhookCreation(call, rt.h.orch, location, launch) }
	var once sync.Once
	var confirmationMu sync.Mutex
	var confirmation error
	confirmed := false
	req.OnExecStarted = func(execID string) {
		once.Do(func() {
			launch.mu.Lock()
			launch.nativeExecID = execID
			launch.confirmed = mooring.ValidWireIdentifier(execID)
			launch.mu.Unlock()
			confirmErr := rt.confirm(runCtx, e, a, started, location, execID)
			confirmationMu.Lock()
			confirmation = confirmErr
			confirmed = confirmErr == nil
			confirmationMu.Unlock()
			if confirmErr != nil {
				stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
				_, _ = rt.stopAt(stopCtx, a.RunID, location)
				stopCancel()
				cancel()
			}
		})
	}
	runErr := rt.h.orch.RunAgent(runCtx, req, nil)
	confirmationMu.Lock()
	ok := confirmed && confirmation == nil
	confirmationMu.Unlock()
	if !ok {
		return orchestrator.ErrPrivateRunUnqualified
	}
	inspectCtx, inspectCancel := context.WithTimeout(context.WithoutCancel(runCtx), 5*time.Second)
	defer inspectCancel()
	alive, inspectErr := rt.Alive(inspectCtx, rt.Locator(a))
	if inspectErr != nil || alive {
		return orchestrator.ErrPrivateRunUnqualified
	}
	// Only original-provider exit proves retirement. An uncertain Run return is
	// neither success nor permission to rerun the native mutation.
	outcome, finishErr := m.finishStoppedRun(inspectCtx, e)
	// This captures only observed output, never authority or terminal status.
	// A closed/revoked grant can prevent retirement but must not erase the
	// positively observed original-exec stop from a cancelled work outcome.
	result := work.RunResult{CompletedAt: time.Now().UTC().Format(time.RFC3339Nano), Metadata: map[string]any{"native_runtime_stopped": true}}
	switch {
	case finishErr == nil:
		if _, uncertain, _ := craftNativeAgentCompletion(e); uncertain {
			finishErr = orchestrator.ErrPrivateRunUnqualified
			break
		}
		result.Metadata["native_outcome"] = outcome.Kind
		switch outcome.Kind {
		case craftNativeAgentOutcomeSaved:
			result.Metadata["native_saved"] = true
			result.Metadata["native_version"] = outcome.Receipt.Version
		case craftNativeAgentOutcomeProposal:
			// The document is unchanged; the human reviews this proposal.
			result.Metadata["native_saved"] = false
			result.Metadata["native_proposal_id"] = outcome.ProposalID
		}
		if runErr == nil {
			zero := 0
			result.ExitCode = &zero
		}
	case errors.Is(finishErr, errNativeAgentNoReceipt):
		result.Metadata["native_saved"] = false
		message := errNativeAgentNoReceipt.Error()
		result.ErrorMessage = &message
	}
	stageCtx, stageCancel := context.WithTimeout(context.WithoutCancel(runCtx), 5*time.Second)
	defer stageCancel()
	if stageErr := work.NewStore(rt.h.db).StageRunResult(stageCtx, e.workID, a.RunID, a.Generation, result); stageErr != nil {
		return errors.Join(work.ErrRunResultUnstored, stageErr)
	}
	if runErr != nil {
		return orchestrator.ErrPrivateRunUnqualified
	}
	return finishErr
}

func (rt *craftNativeAgentRuntime) capturePrincipal(ctx context.Context, e *craftNativeAgentEntry, a dispatch.Assignment, requested bool) error {
	m := rt.manager
	tx, err := m.writer(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = m.checkAssignment(ctx, tx, e, a); err != nil {
		return err
	}
	g := e.grant
	if err = captureOriginalUserArtifactPrincipalContextQuery(ctx, tx, a.RunID, g.Scope.WorkspaceID, g.AgentID, g.CrewID, g.Original.ChatID, g.MessageID, &g.Original); err != nil {
		return err
	}
	if requested {
		if err = work.MarkRuntimeRequestedTx(ctx, tx, e.workID, a.RunID, a.Generation); err != nil {
			return err
		}
	}
	if err = m.checkAssignment(ctx, tx, e, a); err != nil {
		return err
	}
	return tx.Commit()
}

func (rt *craftNativeAgentRuntime) confirm(ctx context.Context, e *craftNativeAgentEntry, a dispatch.Assignment, started func(), location orchestrator.RunLocation, exec string) error {
	if !mooring.ValidWireIdentifier(exec) || location.RunID != a.RunID || location.ContainerID == "" || location.AgentSlug == "" {
		return errCraftNativeAgentDenied
	}
	started() // generic dispatcher confirms actual original exec before attach
	tx, err := rt.manager.writer(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	c, err := rt.manager.prepareConfirmed(ctx, tx, e, a.RunID)
	if err != nil || c.attempt.generation != a.Generation || c.attempt.attempt != int64(a.Attempt) || c.attempt.runtimeLocator != rt.Locator(a) {
		return errCraftNativeAgentDenied
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return rt.manager.publishConfirmed(c)
}

// Existing private-provider ExecID presence/stop rules inspect only the generic
// launch registry; these helpers do not read or install any D1 subject.
func (rt *craftNativeAgentRuntime) Alive(ctx context.Context, locator string) (bool, error) {
	return (&collaborationAgentTaskRuntime{WebhookRuntime: rt.WebhookRuntime}).Alive(ctx, locator)
}
func (rt *craftNativeAgentRuntime) Stop(ctx context.Context, locator string) (bool, error) {
	return (&collaborationAgentTaskRuntime{WebhookRuntime: rt.WebhookRuntime}).Stop(ctx, locator)
}

const (
	craftNativeAgentOutcomeSaved    = "saved"
	craftNativeAgentOutcomeProposal = "proposal"
)

// craftNativeAgentOutcome is a finished run's successful result: a committed
// save receipt, or, when a human held the lease, a proposal this run recorded.
type craftNativeAgentOutcome struct {
	Kind       string
	Receipt    *mooring.NativeSaveReceipt
	ProposalID string
}

func (m *craftNativeAgentManager) finishStoppedRun(ctx context.Context, e *craftNativeAgentEntry) (craftNativeAgentOutcome, error) {
	receipt, uncertain, _ := craftNativeAgentCompletion(e)
	proposal, proposalUncertain := craftNativeAgentProposalOutcome(e)
	proof, err := m.stoppedProof(ctx, e)
	if err != nil {
		return craftNativeAgentOutcome{}, err
	}
	p := craftNativeAgentPrincipal(e)
	if err = m.retireStoppedLease(ctx, proof); err != nil {
		return craftNativeAgentOutcome{}, err
	}
	if uncertain || proposalUncertain {
		return craftNativeAgentOutcome{}, orchestrator.ErrPrivateRunUnqualified
	}
	if receipt == nil {
		if proposal == nil {
			return craftNativeAgentOutcome{}, errNativeAgentNoReceipt
		}
		// Bound to this run's session and still recorded for this page by
		// this agent; a proposal from any other run never counts.
		var found int
		err = m.human.pages.db.QueryRowContext(ctx, `SELECT 1 FROM craft_native_proposals WHERE id=? AND page_id=? AND workspace_id=? AND author_actor_id=?`, proposal.ID, e.grant.Scope.ArtifactID, e.grant.Scope.WorkspaceID, p.ActorID).Scan(&found)
		if errors.Is(err, sql.ErrNoRows) || proposal.SessionID != p.SessionID {
			return craftNativeAgentOutcome{}, errNativeAgentNoReceipt
		}
		if err != nil {
			return craftNativeAgentOutcome{}, err
		}
		return craftNativeAgentOutcome{Kind: craftNativeAgentOutcomeProposal, ProposalID: proposal.ID}, nil
	}
	if receipt.Phase != "committed" || receipt.IssuedBy != "crewship" || receipt.Durability != "node" || receipt.SessionID != p.SessionID || receipt.WorkspaceID != e.grant.Scope.WorkspaceID || receipt.PageID != e.grant.Scope.ArtifactID {
		return craftNativeAgentOutcome{}, orchestrator.ErrPrivateRunUnqualified
	}
	return craftNativeAgentOutcome{Kind: craftNativeAgentOutcomeSaved, Receipt: receipt}, nil
}

var _ dispatch.Runtime = (*craftNativeAgentRuntime)(nil)
