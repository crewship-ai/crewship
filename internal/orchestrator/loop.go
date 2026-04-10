package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// LoopController manages task retry logic for the Ralph Loop pattern.
// When a task fails and has remaining iterations, the controller:
// 1. Increments the iteration counter
// 2. Resets the task to PENDING (or the loop_back_to task)
// 3. Injects failure context from the progress log so the agent learns from mistakes
type LoopController struct {
	db     *sql.DB
	pw     *ProgressWriter
	logger *slog.Logger
}

// NewLoopController creates a LoopController for managing task retry logic.
func NewLoopController(db *sql.DB, pw *ProgressWriter, logger *slog.Logger) *LoopController {
	return &LoopController{db: db, pw: pw, logger: logger}
}

// ShouldRetry checks if a failed task should be retried based on its iteration count.
// Returns true if a retry was initiated, false if the task is terminal.
func (lc *LoopController) ShouldRetry(ctx context.Context, taskID, missionID string) (bool, error) {
	var iteration int
	var maxIterations *int
	var depsJSON, title string
	var assignedAgentID *string

	err := lc.db.QueryRowContext(ctx,
		`SELECT iteration, max_iterations, depends_on, title, assigned_agent_id
		 FROM mission_tasks WHERE id = ? AND mission_id = ?`,
		taskID, missionID).Scan(&iteration, &maxIterations, &depsJSON, &title, &assignedAgentID)
	if err != nil {
		return false, fmt.Errorf("lookup task %s: %w", taskID, err)
	}

	// No max_iterations means no retry
	if maxIterations == nil || *maxIterations <= 1 {
		return false, nil
	}

	// Already exhausted retries
	if iteration >= *maxIterations {
		lc.logger.Info("task exhausted max iterations",
			"task_id", taskID, "iteration", iteration, "max", *maxIterations)
		return false, nil
	}

	// Retry: increment iteration and reset to PENDING
	newIteration := iteration + 1
	now := time.Now().UTC().Format(time.RFC3339)

	_, err = lc.db.ExecContext(ctx, `
		UPDATE mission_tasks SET
			status = 'PENDING',
			iteration = ?,
			assignment_id = NULL,
			result_summary = NULL,
			error_message = NULL,
			started_at = NULL,
			completed_at = NULL,
			duration_ms = NULL,
			updated_at = ?
		WHERE id = ? AND mission_id = ?`,
		newIteration, now, taskID, missionID)
	if err != nil {
		return false, fmt.Errorf("reset task %s for retry: %w", taskID, err)
	}

	lc.logger.Info("task retry initiated",
		"task_id", taskID, "title", title,
		"iteration", newIteration, "max", *maxIterations)

	return true, nil
}

// RetryLoopBack handles the loop-back pattern (dev-test-loop):
// When a downstream task fails, reset the upstream task to restart the cycle.
// E.g., when "test" fails, reset "develop" to PENDING for another iteration.
func (lc *LoopController) RetryLoopBack(ctx context.Context, failedTaskID, missionID string) (bool, error) {
	// Check if the failed task has a loop_back_to reference via the workflow template
	// For now, we look at the depends_on chain: if a failed task depends on another task,
	// and that upstream task has max_iterations, we reset the upstream task.
	var depsJSON string
	var maxIter *int

	err := lc.db.QueryRowContext(ctx,
		`SELECT depends_on, max_iterations FROM mission_tasks WHERE id = ? AND mission_id = ?`,
		failedTaskID, missionID).Scan(&depsJSON, &maxIter)
	if err != nil {
		return false, fmt.Errorf("lookup failed task: %w", err)
	}

	// The failed task itself can be retried
	if maxIter != nil && *maxIter > 1 {
		return lc.ShouldRetry(ctx, failedTaskID, missionID)
	}

	// Check upstream tasks (depends_on) for loop-back
	var deps []string
	if depsJSON != "" && depsJSON != "[]" {
		if err := json.Unmarshal([]byte(depsJSON), &deps); err != nil {
			return false, nil
		}
	}

	for _, depID := range deps {
		retried, err := lc.ShouldRetry(ctx, depID, missionID)
		if err != nil {
			lc.logger.Error("loop-back retry failed", "dep_id", depID, "error", err)
			continue
		}
		if retried {
			// Also reset the failed task to BLOCKED (it will be unblocked when dep completes)
			now := time.Now().UTC().Format(time.RFC3339)
			lc.db.ExecContext(ctx, `
				UPDATE mission_tasks SET
					status = 'BLOCKED',
					assignment_id = NULL,
					result_summary = NULL,
					error_message = NULL,
					started_at = NULL,
					completed_at = NULL,
					updated_at = ?
				WHERE id = ? AND mission_id = ?`,
				now, failedTaskID, missionID)
			return true, nil
		}
	}

	return false, nil
}
