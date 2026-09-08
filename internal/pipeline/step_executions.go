package pipeline

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/scrubber"
)

// ExecutionStore records actual invocations, including failed outputs and nested items.
// A finished row is immutable. A fresh attempt always gets a fresh identity.
type ExecutionStore struct {
	db        *sql.DB
	publisher ArtifactPublisher
}

func NewExecutionStore(db *sql.DB) *ExecutionStore                 { return &ExecutionStore{db: db} }
func (e *Executor) WithExecutionStore(s *ExecutionStore) *Executor { e.executionStore = s; return e }

type executionContextKey struct{}
type executionContext struct{ id, path, runID string }

func executionIn(ctx context.Context) executionContext {
	v, _ := ctx.Value(executionContextKey{}).(executionContext)
	return v
}
func foreachExecutionContext(ctx context.Context, index int) context.Context {
	v := executionIn(ctx)
	v.path += fmt.Sprintf("/items/%d", index)
	return context.WithValue(ctx, executionContextKey{}, v)
}
func (s *ExecutionStore) start(ctx context.Context, runID, stepID, kind, agent, model string) (context.Context, string, error) {
	parent := executionIn(ctx)
	if parent.runID != "" {
		runID = parent.runID
	}
	executionPath := parent.path + "/" + stepID
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return ctx, "", err
	}
	id := "exec_" + hex.EncodeToString(bytes[:])
	// One SQL statement allocates the attempt and inserts it under SQLite's write lock.
	_, err := s.db.ExecContext(ctx, `INSERT INTO pipeline_step_executions
      (id,run_id,parent_execution_id,step_id,execution_path,attempt,kind,status,agent_slug,model,started_at)
      SELECT ?,?,NULLIF(?,''),?,?,COALESCE(MAX(attempt),0)+1,?,'running',?,?,?
      FROM pipeline_step_executions WHERE run_id=? AND execution_path=?`,
		id, runID, parent.id, stepID, executionPath, kind, agent, model, time.Now().UTC().Format(time.RFC3339Nano), runID, executionPath)
	if err != nil {
		return ctx, "", fmt.Errorf("record step start: %w", err)
	}
	return context.WithValue(ctx, executionContextKey{}, executionContext{id: id, path: executionPath, runID: runID}), id, nil
}
func (s *ExecutionStore) finish(ctx context.Context, id, output string, runErr error) error {
	status, reason := "completed", ""
	if runErr != nil {
		status, reason = "failed", runErr.Error()
		if errors.Is(runErr, errExecutionSkipped) {
			status = "skipped"
		}
		var suspended *suspendError
		if errors.As(runErr, &suspended) {
			status = "waiting"
		}
		if errors.Is(runErr, context.Canceled) {
			status = "cancelled"
		}
	}
	// Persist cancellation even though the execution context has ended.
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	scrub := scrubber.New()
	_, err := s.db.ExecContext(persistCtx, `UPDATE pipeline_step_executions SET status=?,ended_at=?,output=?,error=? WHERE id=? AND status='running'`, status, time.Now().UTC().Format(time.RFC3339Nano), scrub.Scrub(output), scrub.Scrub(reason), id)
	return err
}

func (e *Executor) runStep(
	ctx context.Context, step Step, renderedPrompt string, primary AdapterModel, fallback []AdapterModel,
	in RunInput, runID, pipelineID string, emit *pipelineEmitContext, parentRender RenderContext, depth int, priorCostUSD float64,
) (out string, cost float64, dur int64, err error) {
	if e.executionStore == nil || in.Mode == ModeDryRun || in.pipeline == nil {
		return e.runStepBody(ctx, step, renderedPrompt, primary, fallback, in, runID, pipelineID, emit, parentRender, depth, priorCostUSD)
	}
	ctx, id, err := e.executionStore.start(ctx, runID, step.ID, string(step.Type), step.AgentSlug, primary.Model)
	if err != nil {
		return "", 0, 0, err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = e.executionStore.finish(ctx, id, out, fmt.Errorf("step panicked: %v", p))
			panic(p)
		}
		if persistErr := e.executionStore.finish(ctx, id, out, err); persistErr != nil {
			err = errors.Join(err, fmt.Errorf("record step result: %w", persistErr))
		}
	}()
	out, cost, dur, err = e.runStepBody(ctx, step, renderedPrompt, primary, fallback, in, runID, pipelineID, emit, parentRender, depth, priorCostUSD)
	if e.executionStore.publisher != nil && out != "" {
		state := "available"
		if err != nil {
			state = "draft"
		}
		err = errors.Join(err, e.executionStore.publisher.PublishRunArtifacts(ctx, in.WorkspaceID, in.AuthorCrewID, executionIn(ctx).runID, id, scrubStepOutput(out), state))
	}
	return
}
func (e *Executor) runRecordedAgent(ctx context.Context, req AgentStepRequest) (res AgentStepResult, err error) {
	if e.executionStore == nil || executionIn(ctx).id == "" {
		return e.runner.RunStep(ctx, req)
	}
	leaf, kind := "agent", "agent_attempt"
	if strings.HasSuffix(req.StepID, ":grader") {
		leaf, kind = "review", "review"
	}
	ctx, id, err := e.executionStore.start(ctx, req.PipelineRunID, leaf, kind, req.AgentSlug, req.Model)
	if err != nil {
		return res, err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = e.executionStore.finish(ctx, id, res.Output, fmt.Errorf("agent panicked: %v", p))
			panic(p)
		}
		err = errors.Join(err, e.executionStore.finish(ctx, id, res.Output, err))
	}()
	res, err = e.runner.RunStep(ctx, req)
	if e.executionStore.publisher != nil && res.Output != "" {
		err = errors.Join(err, e.executionStore.publisher.PublishRunArtifacts(ctx, req.WorkspaceID, req.AuthorCrewID, executionIn(ctx).runID, id, scrubStepOutput(res.Output), "draft"))
	}
	return
}

var errExecutionSkipped = errors.New("Condition was false")

func (e *Executor) recordSkippedExecution(ctx context.Context, in RunInput, runID string, step Step) {
	if e.executionStore == nil || in.Mode == ModeDryRun || in.pipeline == nil {
		return
	}
	ctx, id, err := e.executionStore.start(ctx, runID, step.ID, string(step.Type), step.AgentSlug, "")
	if err == nil {
		err = e.executionStore.finish(ctx, id, "", errExecutionSkipped)
	}
	if err != nil {
		e.persistWarn("record skipped step", runID, err)
	}
}
