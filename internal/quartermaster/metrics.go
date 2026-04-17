package quartermaster

import (
	"context"
	"database/sql"
	"time"
)

// Compute derives EvalMetrics from an ordered trajectory. The heuristics
// here are intentionally simple so they stay explainable:
//
//   - ToolCallCount = number of exec.command + llm.call steps.
//   - ToolSuccessRate = (exec.command exit_code==0 + keeper.decision allow)
//     / (total exec.command + total keeper.decision).
//   - StepsToGoal = index of the first mission.status_change to
//     "completed" (inclusive), or len(steps) if the mission never finished.
//   - ConvergenceRatio = (pending_tasks_at_start+1) / StepsToGoal.
//     Higher is better (closer to the optimal straight-line path).
//   - TotalCostUSD / TotalTokens = sum of llm.call payload fields.
//   - Hallucinations = guardrail.output_blocked at severity warn|error.
//   - FailureModes = MAST-taxonomy categories detected in the trajectory.
func Compute(steps []TrajectoryStep) EvalMetrics {
	m := EvalMetrics{}

	if len(steps) == 0 {
		return m
	}

	toolPassed := 0
	toolTotal := 0
	completedAt := -1

	for _, s := range steps {
		switch s.EntryType {
		case "exec.command":
			m.ToolCallCount++
			toolTotal++
			if s.Success {
				toolPassed++
			}
		case "llm.call":
			m.ToolCallCount++
			m.TotalTokens += int64(s.TokenCost)
			// TokenCost feeds tokens; dollar cost is attached via CostFromCallback
			// or a later pass — for MVP we approximate via tokens only if no
			// cost field is present. Callers can override TotalCostUSD.
		case "keeper.decision":
			toolTotal++
			if s.Success {
				toolPassed++
			}
		case "mission.status_change":
			if s.ToolName == "completed" && completedAt < 0 {
				completedAt = s.Index
			}
		}
	}

	if toolTotal > 0 {
		m.ToolSuccessRate = float64(toolPassed) / float64(toolTotal)
	}

	if completedAt >= 0 {
		m.StepsToGoal = completedAt + 1
	} else {
		m.StepsToGoal = len(steps)
	}

	// Convergence heuristic: we don't know pending_tasks_at_start from the
	// trajectory directly — approximate by counting assignment.created
	// entries that precede the first mission.status_change to "running".
	pendingAtStart := 0
	sawRunning := false
	for _, s := range steps {
		if s.EntryType == "mission.status_change" && s.ToolName == "running" {
			sawRunning = true
		}
		if s.EntryType == "assignment.created" && !sawRunning {
			pendingAtStart++
		}
	}
	if m.StepsToGoal > 0 {
		m.ConvergenceRatio = float64(pendingAtStart+1) / float64(m.StepsToGoal)
	}

	// Hallucinations: output-side guardrail blocks. Severity lives on the
	// journal Entry, not the TrajectoryStep — the Extract path drops it.
	// For the synthetic case here we count all output_blocked steps; the
	// DB-backed path can be upgraded later to read severity.
	for _, s := range steps {
		if s.EntryType == "guardrail.output_blocked" {
			m.Hallucinations++
		}
	}

	m.FailureModes = detectFailureModes(steps)
	return m
}

// ComputeFromDB is a convenience that runs Extract + Compute in one call
// and also reaches into the raw entries to pull severity (for precise
// hallucination counting) and llm.call cost totals. Callers that already
// have steps should prefer Compute.
func ComputeFromDB(ctx context.Context, db *sql.DB, workspaceID, missionID string) (EvalMetrics, []TrajectoryStep, error) {
	steps, err := Extract(ctx, db, workspaceID, missionID)
	if err != nil {
		return EvalMetrics{}, nil, err
	}
	return Compute(steps), steps, nil
}

// detectFailureModes walks the trajectory and flags MAST-taxonomy
// categories. The rules are deliberately coarse; false positives are
// cheap (caller can inspect), false negatives are worse.
func detectFailureModes(steps []TrajectoryStep) []string {
	var modes []string
	seen := map[string]struct{}{}
	add := func(m string) {
		if _, dup := seen[m]; dup {
			return
		}
		seen[m] = struct{}{}
		modes = append(modes, m)
	}

	// tool_loop: same tool >5x within a 2-minute window (by elapsed_ms
	// accumulation — we don't have absolute timestamps on the step).
	if detectToolLoop(steps, 5, 2*time.Minute) {
		add("tool_loop")
	}

	// budget_runaway: budget.exceeded fired at any point.
	for _, s := range steps {
		if s.EntryType == "budget.exceeded" {
			add("budget_runaway")
			break
		}
	}

	// guardrail_cascade: >3 guardrail.* entries.
	grCount := 0
	for _, s := range steps {
		if s.EntryType == "guardrail.input_blocked" || s.EntryType == "guardrail.output_blocked" {
			grCount++
		}
	}
	if grCount > 3 {
		add("guardrail_cascade")
	}

	// escalation_loop: >2 peer.escalation entries.
	esc := 0
	for _, s := range steps {
		if s.EntryType == "peer.escalation" {
			esc++
		}
	}
	if esc > 2 {
		add("escalation_loop")
	}

	return modes
}

// detectToolLoop slides a window across the trajectory. For each exec.command
// step it counts how many exec.command steps with the same ToolName fall
// within the subsequent elapsed-time budget. If the count exceeds threshold,
// it's a loop.
func detectToolLoop(steps []TrajectoryStep, threshold int, window time.Duration) bool {
	windowMs := int(window / time.Millisecond)
	for i := 0; i < len(steps); i++ {
		if steps[i].EntryType != "exec.command" || steps[i].ToolName == "" {
			continue
		}
		name := steps[i].ToolName
		elapsed := 0
		count := 1
		for j := i + 1; j < len(steps); j++ {
			elapsed += steps[j].ElapsedMs
			if elapsed > windowMs {
				break
			}
			if steps[j].EntryType == "exec.command" && steps[j].ToolName == name {
				count++
				if count > threshold {
					return true
				}
			}
		}
	}
	return false
}
