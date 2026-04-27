package quartermaster

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// Replay rehydrates a mission's trajectory from the journal, computes
// metrics, and emits eval.* journal entries so the run is itself a
// durable artifact in the log.
//
// This is observational replay: we do NOT re-execute the agents. The
// seed argument parameterizes the signature calculation (so the caller
// can later correlate re-runs with identical seeds) but otherwise has
// no effect on the computation.
//
// No new DB table is introduced; the journal is the persistent record.
func Replay(ctx context.Context, db *sql.DB, j journal.Emitter, workspaceID, missionID string, seed int64) (EvalRun, error) {
	if j == nil {
		return EvalRun{}, fmt.Errorf("quartermaster: emitter required")
	}

	run := EvalRun{
		ID:        newRunID(),
		MissionID: missionID,
		StartedAt: time.Now().UTC(),
		Status:    "running",
	}

	steps, err := Extract(ctx, db, workspaceID, missionID)
	if err != nil {
		run.Status = "failed"
		run.Result = "extract_failed: " + err.Error()
		run.CompletedAt = time.Now().UTC()
		return run, err
	}

	metrics := Compute(steps)
	run.Metrics = metrics
	run.SeedSignature = signatureFor(steps, seed)

	// Emit the run-started entry first. The completed state is implicit
	// from the per-metric entries that follow (and a downstream consumer
	// can page eval.metric entries grouped by the refs.eval_run_id).
	if _, err := j.Emit(ctx, journal.Entry{
		WorkspaceID: workspaceID,
		MissionID:   missionID,
		Type:        journal.EntryEvalRunStarted,
		ActorType:   journal.ActorSystem,
		ActorID:     "quartermaster",
		Summary:     fmt.Sprintf("eval replay started (seed=%d, steps=%d)", seed, len(steps)),
		Payload: map[string]any{
			"seed":      seed,
			"steps":     len(steps),
			"signature": run.SeedSignature,
		},
		Refs: map[string]any{
			"eval_run_id": run.ID,
			"mission_id":  missionID,
			"seed":        seed,
			"signature":   run.SeedSignature,
		},
	}); err != nil {
		run.Status = "failed"
		run.Result = "emit_started_failed: " + err.Error()
		run.CompletedAt = time.Now().UTC()
		return run, err
	}

	// Emit one EntryEvalMetric per numeric metric. Keeping them as
	// separate entries lets the journal query layer filter by metric name
	// via the payload without custom indexes.
	metricEmissions := []struct {
		name  string
		value any
	}{
		{"tool_call_count", metrics.ToolCallCount},
		{"tool_success_rate", metrics.ToolSuccessRate},
		{"steps_to_goal", metrics.StepsToGoal},
		{"convergence_ratio", metrics.ConvergenceRatio},
		{"total_cost_usd", metrics.TotalCostUSD},
		{"total_tokens", metrics.TotalTokens},
		{"hallucinations", metrics.Hallucinations},
		{"failure_modes", metrics.FailureModes},
	}
	for _, m := range metricEmissions {
		if _, err := j.Emit(ctx, journal.Entry{
			WorkspaceID: workspaceID,
			MissionID:   missionID,
			Type:        journal.EntryEvalMetric,
			ActorType:   journal.ActorSystem,
			ActorID:     "quartermaster",
			Summary:     fmt.Sprintf("metric %s", m.name),
			Payload: map[string]any{
				"metric": m.name,
				"value":  m.value,
			},
			Refs: map[string]any{
				"eval_run_id": run.ID,
				"mission_id":  missionID,
			},
		}); err != nil {
			// Best-effort: a single failed metric emission doesn't fail
			// the run — we surface it in Result and move on.
			run.Result = "emit_metric_failed: " + m.name + ": " + err.Error()
		}
	}

	run.Status = "completed"
	if run.Result == "" {
		run.Result = "ok"
	}
	run.CompletedAt = time.Now().UTC()
	return run, nil
}

// signatureFor produces a reproducible hash of the trajectory's shape.
// Two runs over the same sequence of (entry_type, tool_name) pairs get
// the same signature, regardless of wall-clock or IDs. Seed is folded in
// so callers can bucket replays by seed.
func signatureFor(steps []TrajectoryStep, seed int64) string {
	h := sha256.New()
	h.Write([]byte(strconv.FormatInt(seed, 10)))
	h.Write([]byte{0})
	for _, s := range steps {
		h.Write([]byte(s.EntryType))
		h.Write([]byte{'|'})
		h.Write([]byte(s.ToolName))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// newRunID generates a short random identifier for eval runs. Not a UUID:
// same rationale as journal.newID — collision-free within a workspace and
// small enough to not bloat payloads.
func newRunID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "er_" + hex.EncodeToString(b[:])
}
