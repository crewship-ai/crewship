package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/crewstart"
	"github.com/crewship-ai/crewship/internal/dispatch"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/mooring"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/runauthority"
	"github.com/crewship-ai/crewship/internal/work"
)

// Neither callback is a request field or a client-controlled role assertion.
// No production constructor supplies them. The environment callback is a
// separate enablement gate: host output suppression cannot qualify native CLI
// histories, shared container files, mounts, tools or existing log readouts.
type collaborationAgentPrivateEnvironment interface {
	BeforeStart(context.Context) error
	Qualify(context.Context, chatbridge.ChatInfo, *provider.RuntimeUse) error
}

type collaborationAgentConfirmed struct {
	store                                   *collaborationAgentMandateStore
	key                                     mooring.SessionKey
	mandate, workID, runID, agentID, crewID string
	attempt                                 int
	generation                              int64
	location                                orchestrator.RunLocation
	execID                                  string
}

type collaborationAgentTaskRuntime struct {
	*WebhookRuntime // Existing launch registry, stop and uncertain-probe rules.
	tasks           *collaborationAgentTaskDispatcher
	environment     collaborationAgentPrivateEnvironment
	onConfirmed     func(context.Context, collaborationAgentConfirmed) error
}

func newCollaborationAgentTaskRuntime(tasks *collaborationAgentTaskDispatcher, h *WebhookHandler, environment collaborationAgentPrivateEnvironment, onConfirmed func(context.Context, collaborationAgentConfirmed) error) (*collaborationAgentTaskRuntime, error) {
	if tasks == nil || tasks.mandates == nil || h == nil || h.db != tasks.mandates.pages.db || h.orch == nil || h.resolver == nil || h.container == nil {
		return nil, errCollaborationAgentAccess
	}
	return &collaborationAgentTaskRuntime{WebhookRuntime: NewWebhookRuntime(h), tasks: tasks, environment: environment, onConfirmed: onConfirmed}, nil
}

// NewDispatcher is dormant and declares exactly its own work pair. It never
// broadens the webhook/schedule dispatcher's kinds or changes generic chat.
func (rt *collaborationAgentTaskRuntime) NewDispatcher(cfg dispatch.Config) *dispatch.Dispatcher {
	cfg.Kinds = []work.Kind{{Source: work.SourceChat, DomainKind: "mooring_wordcraft_task"}}
	return dispatch.New(work.NewStore(rt.h.db), rt, rt.tasks, cfg, rt.h.logger)
}

// Classify intentionally has no successful/terminal interpretation yet. A CLI
// exit or a callback return is not a sealed Mooring save/completion receipt.
func (rt *collaborationAgentTaskRuntime) Classify(dispatch.Assignment, error) dispatch.Outcome {
	return dispatch.OutcomeUnclear
}

// Do not inherit the generic runtime's global outcome projection. This leaf
// owns no qualified terminal Mooring result or completion outbox yet.
func (*collaborationAgentTaskRuntime) FlushRunOutcomes(context.Context) error { return nil }

func (rt *collaborationAgentTaskRuntime) Run(ctx context.Context, a dispatch.Assignment, started func()) error {
	if rt == nil || rt.tasks == nil || rt.environment == nil || rt.onConfirmed == nil || started == nil {
		return orchestrator.ErrPrivateRunUnqualified
	}
	if err := rt.environment.BeforeStart(ctx); err != nil {
		return orchestrator.ErrPrivateRunUnqualified
	}
	if _, err := rt.tasks.Authorize(ctx, a); err != nil {
		return errCollaborationAgentAccess
	}
	rt.tasks.mu.Lock()
	entry := rt.tasks.entries[a.Item.ID]
	if entry == nil {
		rt.tasks.mu.Unlock()
		return errCollaborationAgentAccess
	}
	captured := *entry
	rt.tasks.mu.Unlock()
	task := captured.task
	w := task.work
	releaseCreation, err := rt.tasks.mandates.fence.acquireOriginal(task.ingress)
	if err != nil {
		return errCollaborationAgentAccess
	}
	var creationReleased sync.Once
	releaseCreationOnce := func() { creationReleased.Do(releaseCreation) }
	// Full operation lease BEFORE crew/provider/SQL work through confirmed
	// attempt COMMIT and callback publication; failure also releases it. Live
	// content operations need their own fences after this creation boundary.
	defer releaseCreationOnce()
	// Drop any ambient serialized-origin reference. The ONLY human provenance
	// sent to authenticated IPC is the receipt copied from this host's pending.
	receipt := task.originalHuman
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	runCtx = runauthority.WithOriginID(runCtx, "")
	runCtx = chataudience.WithReceipt(runCtx, &receipt)
	runCtx = runauthority.WithTriggerMessage(runCtx, task.messageID)
	runCtx = journal.WithRunID(runCtx, a.RunID)
	launch := &webhookLaunch{cancel: cancel, workID: w.workID, runID: a.RunID, generation: a.Generation, store: work.NewStore(rt.h.db)}
	launch.requestCreation = func(ctx context.Context) error { return rt.requestOriginalCreation(ctx, a, captured) }
	if _, loaded := rt.launches.LoadOrStore(a.RunID, launch); loaded {
		return errCollaborationAgentAccess
	}
	defer func() { launch.mu.Lock(); launch.returned = true; launch.mu.Unlock() }()
	info, err := rt.h.resolver.ResolveChat(runCtx, w.chatID)
	if err != nil || info == nil || info.AgentID != w.agentID || info.CrewID != w.crewID || info.WorkspaceID != w.workspaceID || info.AgentStatus == chatbridge.AgentStatusPendingReview {
		return errCollaborationAgentAccess
	}
	if err = launch.Enter("private crew start"); err != nil {
		return orchestrator.ErrPrivateRunUnqualified
	}
	cfg, err := info.CrewRuntimeConfig(4096, 2)
	if err != nil {
		return orchestrator.ErrPrivateRunUnqualified
	}
	use, _, err := crewstart.New(rt.h.container, NewCrewConfigCompleter(rt.h.db), rt.h.logger).StartUse(runCtx, cfg, nil)
	defer use.Release()
	if err != nil || use == nil {
		return orchestrator.ErrPrivateRunUnqualified
	}
	if err = rt.environment.Qualify(runCtx, *info, use); err != nil {
		return orchestrator.ErrPrivateRunUnqualified
	}
	if err = rt.capturePrincipal(runCtx, a, captured); err != nil {
		return errCollaborationAgentAccess
	}
	if err = rt.h.resolver.CreateRun(runCtx, a.RunID, w.agentID, w.chatID, w.workspaceID, "CHAT", map[string]interface{}{"mooring_task": true}); err != nil {
		return orchestrator.ErrPrivateRunUnqualified
	}
	var input struct {
		Instruction string `json:"instruction"`
	}
	if json.Unmarshal([]byte(a.Item.InputJSON), &input) != nil || input.Instruction == "" {
		return errCollaborationAgentAccess
	}
	req := info.ToAgentRunRequest(chatbridge.AgentRunOverrides{ChatID: w.chatID, ContainerID: use.ContainerID(), UserMessage: input.Instruction, LLMModel: info.LLMModel, TimeoutSecs: info.TimeoutSecs, MemoryMB: info.MemoryMB, CPUs: info.CPUs, MaxTurns: orchestrator.RoutineMaxTurns}).WithStrictContentPrivacy()
	req.RunID = a.RunID
	req.RuntimeUse = use
	location := orchestrator.RunLocation{ContainerID: req.ContainerID, AgentSlug: req.AgentSlug, RunID: a.RunID}
	if err = launch.Launch(location); err != nil {
		return orchestrator.ErrPrivateRunUnqualified
	}
	req.ExecGate = func(gateCtx context.Context) error {
		return requestWebhookCreation(gateCtx, rt.h.orch, location, launch)
	}

	var once sync.Once
	var confirmMu sync.Mutex
	var confirmation error
	confirmed := false
	req.OnExecStarted = func(execID string) {
		once.Do(func() {
			defer releaseCreationOnce()
			launch.mu.Lock()
			launch.confirmed = true
			actualLocation := *launch.location
			launch.mu.Unlock()
			e := rt.confirm(runCtx, a, captured, started, actualLocation, execID)
			confirmMu.Lock()
			confirmation = e
			confirmed = e == nil
			confirmMu.Unlock()
			if e != nil {
				stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
				_, _ = rt.stopAt(stopCtx, a.RunID, location)
				stopCancel()
				cancel() // Neither cancellation nor this stop attempt proves exit.
			}
		})
	}
	// No conversation append, hub broadcast, output buffer or content handler.
	runErr := rt.h.orch.RunAgent(runCtx, req, nil)
	confirmMu.Lock()
	confirmedOK := confirmed && confirmation == nil
	confirmMu.Unlock()
	if !confirmedOK || runErr != nil {
		return orchestrator.ErrPrivateRunUnqualified
	}
	return orchestrator.ErrPrivateRunUnqualified // No sealed completion yet.
}

func (rt *collaborationAgentTaskRuntime) confirm(ctx context.Context, a dispatch.Assignment, entry collaborationAgentDispatchEntry, started func(), location orchestrator.RunLocation, execID string) error {
	if !mooring.ValidWireIdentifier(execID) || location.RunID != a.RunID || location.ContainerID == "" || location.AgentSlug == "" {
		return errCollaborationAgentAccess
	}
	release, err := rt.tasks.mandates.fence.acquireOriginal(entry.task.ingress)
	if err != nil {
		return err
	}
	defer release() // Covers StartRunning, own attach COMMIT and host publication.
	started()       // Synchronous dispatcher StartRunning commits before attach.
	binding, err := rt.attach(ctx, a, entry)
	if err != nil {
		return err
	}
	binding.location, binding.execID = location, execID
	return rt.onConfirmed(ctx, binding)
}

func (rt *collaborationAgentTaskRuntime) capturePrincipal(ctx context.Context, a dispatch.Assignment, entry collaborationAgentDispatchEntry) error {
	release, err := rt.tasks.mandates.fence.acquireOriginal(entry.task.ingress)
	if err != nil {
		return err
	}
	defer release()
	tx, err := rt.tasks.writer(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = rt.capturePrincipalTx(ctx, tx, a, entry); err != nil {
		return err
	}
	return tx.Commit()
}

// The caller owns the same canonical writer and original ingress lease.
func (rt *collaborationAgentTaskRuntime) capturePrincipalTx(ctx context.Context, tx *sql.Tx, a dispatch.Assignment, entry collaborationAgentDispatchEntry) error {
	if tx == nil {
		return errCollaborationAgentAccess
	}
	var err error
	if err = rt.tasks.checkAssignment(ctx, tx, &entry, a); err != nil {
		return err
	}
	w := entry.task.work
	receipt := entry.task.originalHuman
	if err = captureOriginalUserArtifactPrincipalContextQuery(ctx, tx, a.RunID, w.workspaceID, w.agentID, w.crewID, w.chatID, entry.task.messageID, &receipt); err != nil {
		return err
	}
	return nil
}

// Called only by the private launch gate while its stop mutex and the whole
// original creation-operation lease are held. No provider may start before the
// source/principal/requested phase have committed together on the FULL handle.
func (rt *collaborationAgentTaskRuntime) requestOriginalCreation(ctx context.Context, a dispatch.Assignment, entry collaborationAgentDispatchEntry) error {
	release, err := rt.tasks.mandates.fence.acquireOriginal(entry.task.ingress)
	if err != nil {
		return err
	}
	defer release()
	tx, err := rt.tasks.writer(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var synchronous int
	if err = tx.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil || synchronous < 2 {
		return errCollaborationAgentAccess
	}
	if err = rt.capturePrincipalTx(ctx, tx, a, entry); err != nil {
		return err
	}
	if err = work.MarkRuntimeRequestedTx(ctx, tx, entry.task.work.workID, a.RunID, a.Generation); err != nil {
		return err
	}
	// Recheck after the transition, including any canonical SQL trigger effects.
	// A changed source rolls the transition back instead of publishing creation.
	if err = rt.tasks.checkAssignment(ctx, tx, &entry, a); err != nil {
		return err
	}
	return tx.Commit()
}

func (rt *collaborationAgentTaskRuntime) attach(ctx context.Context, a dispatch.Assignment, entry collaborationAgentDispatchEntry) (collaborationAgentConfirmed, error) {
	var result collaborationAgentConfirmed
	s := rt.tasks.mandates
	release, err := s.fence.acquireOriginal(entry.task.ingress)
	if err != nil {
		return result, err
	}
	defer release()
	tx, err := rt.tasks.writer(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	actual, _, err := loadCollaborationAgentAttempt(ctx, tx, a.RunID)
	if err != nil || actual.collaborationAgentWorkBinding != entry.task.work || actual.generation != a.Generation || actual.attempt != int64(a.Attempt) || actual.runtimeLocator != rt.Locator(a) {
		return result, errCollaborationAgentAccess
	}
	handle, err := s.AttachConfirmedAttempt(ctx, tx, entry.pending, a.RunID)
	if err != nil {
		return result, err
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	return collaborationAgentConfirmed{store: s, key: entry.task.key, mandate: handle, workID: actual.workID, runID: actual.runID, agentID: actual.agentID, crewID: actual.crewID, attempt: a.Attempt, generation: a.Generation}, nil
}

var _ dispatch.Runtime = (*collaborationAgentTaskRuntime)(nil)
