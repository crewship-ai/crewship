package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/dispatch"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/work"
)

type assignmentExecutionKey struct{}
type assignmentExecution struct {
	assignment dispatch.Assignment
	launch     *webhookLaunch
	started    func()
	once       sync.Once
	runErr     error
	staged     bool
	stageErr   error
	exitCode   *int
	release    func()
}

func ownedAssignmentExecution(ctx context.Context) *assignmentExecution {
	v, _ := ctx.Value(assignmentExecutionKey{}).(*assignmentExecution)
	return v
}

type AssignmentRuntime struct {
	base         *WebhookRuntime
	h            *AssignmentHandler
	held         sync.Map
	projectionMu sync.Mutex
}

func (rt *AssignmentRuntime) Run(ctx context.Context, a dispatch.Assignment, started func()) error {
	var in assignmentWorkInput
	if a.Item == nil || json.Unmarshal([]byte(a.Item.InputJSON), &in) != nil || in.Version != 1 || in.AssignmentID != a.Item.DomainID || in.WorkspaceID != a.Item.WorkspaceID || in.AgentID != a.Item.AgentID || in.CrewID != a.Item.CrewID {
		return fmt.Errorf("%w: invalid assignment work input", errWebhookInputUnreadable)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	launch := &webhookLaunch{cancel: cancel, workID: a.Item.ID, runID: a.RunID, generation: a.Generation, store: work.NewStore(rt.h.db)}
	rt.base.launches.Store(a.RunID, launch)
	defer func() { launch.mu.Lock(); launch.returned = true; launch.mu.Unlock() }()
	execution := &assignmentExecution{assignment: a, launch: launch, started: started}
	runCtx = context.WithValue(runCtx, assignmentExecutionKey{}, execution)
	defer func() {
		if execution.release != nil {
			rt.held.Store(a.RunID, execution.release)
		}
	}()
	if err := rt.h.dispatchByID(runCtx, in.AssignmentID); err != nil {
		return err
	}
	if execution.stageErr != nil {
		return fmt.Errorf("%w: %v", work.ErrRunResultUnstored, execution.stageErr)
	}
	if !execution.staged {
		return errors.New("assignment returned without a durable execution result")
	}

	if execution.runErr != nil {
		launch.mu.Lock()
		requested := launch.requested
		launch.mu.Unlock()
		// A returned preparation never crossed the durable creation gate, or
		// the provider supplied a real terminal exit. Both are known failures;
		// requested execution without terminal evidence remains uncertain.
		if !requested || execution.exitCode != nil {
			return fmt.Errorf("%w: %w", errAssignmentKnownFailure, execution.runErr)
		}
	}
	return execution.runErr
}

var errAssignmentKnownFailure = errors.New("assignment failed with confirmed execution boundary")

func (rt *agentWorkRuntime) Classify(a dispatch.Assignment, err error) dispatch.Outcome {
	if a.Item != nil && a.Item.Source == work.SourceAssignment && a.Item.DomainKind == work.DomainAssignment && errors.Is(err, errAssignmentKnownFailure) {
		return dispatch.OutcomeFailed
	}
	return rt.WebhookRuntime.Classify(a, err)
}

// stageAssignmentResult records observations; the dispatcher alone owns the
// terminal transition. The domain row is projected after that transition.
func (h *AssignmentHandler) stageAssignmentResult(ctx context.Context, id, runID, result, errMsg string, acc *orchestrator.Accumulator) bool {
	execution := ownedAssignmentExecution(ctx)
	if execution == nil {
		return false
	}
	if id != execution.assignment.Item.DomainID || runID != "" && runID != execution.assignment.RunID {
		execution.stageErr = errors.New("assignment result identity mismatch")
		return true
	}
	metadata := map[string]any{"assignment_id": id, "assignment_result": result}
	if md := runSessionProvenance(acc); md != nil {
		for k, v := range md {
			metadata[k] = v
		}
	}
	var message *string
	if errMsg != "" {
		message = &errMsg
		if execution.runErr == nil {
			execution.runErr = errors.New(errMsg)
		}
	}
	if execution.exitCode == nil && execution.runErr == nil {
		execution.runErr = errors.New("assignment has no observed terminal execution")
	}
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	execution.stageErr = execution.launch.RecordResult(settleCtx, work.RunResult{ExitCode: execution.exitCode, ErrorMessage: message, Metadata: metadata})
	execution.staged = execution.stageErr == nil
	return true
}

func (rt *agentWorkRuntime) Forget(locator string) {
	if rt.assignment != nil {
		if _, held := rt.assignment.held.Load(runIDFromLocator(locator)); held {
			return
		}
	}
	rt.WebhookRuntime.Forget(locator)
}

// Owned execution needs a durable start record before projection can depend on
// its identity. Queued telemetry alone cannot acknowledge this boundary.
func (h *AssignmentHandler) emitAssignmentRunStarted(ctx context.Context, entry journal.Entry) (string, error) {
	if ownedAssignmentExecution(ctx) != nil {
		syncWriter, ok := h.journal.(journal.SyncEmitter)
		if !ok {
			return "", errors.New("assignment runtime requires synchronous start journal")
		}
		return syncWriter.EmitSync(ctx, entry)
	}
	return h.journal.Emit(ctx, entry)
}
