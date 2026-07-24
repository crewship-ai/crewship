package pipeline

// Post-run outcome verdict (#1403) for routine/pipeline runs — the
// routine-run counterpart to internal/api/internal_runs.go's UpdateRun
// wiring for ad-hoc agent runs. Both call sites share the same
// feature_flags row (migrate_consts_v164_run_verdict_flag.go) and the
// same internal/runverdict.GenerateAndEmit, but fetch entries
// differently: ad-hoc agent runs correlate via journal trace_id ==
// run.id, while pipeline runs never set trace_id (every emit in
// journal.go stamps ActorID: runID instead) — hence the ActorID
// filter on journal.Query (see queries.go).

import (
	"context"
	"database/sql"
	"log/slog"

	"github.com/crewship-ai/crewship/internal/featureflags"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/llm"
	"github.com/crewship-ai/crewship/internal/runverdict"
)

// runVerdictFlagKey mirrors internal/api/internal_runs.go's constant of
// the same name — one feature_flags row gates both call sites.
const runVerdictFlagKey = "run_verdict_summaries"

// newRunVerdictHook builds the closure NewWiredExecutor installs via
// WithRunVerdict when both a DB and a pre-resolved LLM provider are
// available. Checks the workspace feature flag, fetches the run's
// journal entries by actor_id, and calls runverdict.GenerateAndEmit.
// Every failure is logged and swallowed — this narrates a run, it must
// never affect one.
func newRunVerdictHook(db *sql.DB, emitter Emitter, provider llm.Provider, model string, logger *slog.Logger) func(ctx context.Context, workspaceID, crewID, agentID, pipelineID, pipelineSlug, runID string) {
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context, workspaceID, crewID, agentID, pipelineID, pipelineSlug, runID string) {
		enabled, err := featureflags.IsEnabled(ctx, db, workspaceID, runVerdictFlagKey)
		if err != nil {
			logger.Debug("run verdict: feature flag check", "error", err, "run_id", runID)
			return
		}
		if !enabled {
			return
		}

		entries, _, err := journal.List(ctx, db, journal.Query{WorkspaceID: workspaceID, ActorID: runID, Limit: 500})
		if err != nil {
			logger.Debug("run verdict: fetch entries", "error", err, "run_id", runID)
			return
		}

		base := journal.Entry{
			WorkspaceID: workspaceID,
			CrewID:      crewID,
			AgentID:     agentID,
			ActorID:     runID,
			// pipeline_id/pipeline_slug/run_id mirror every other
			// pipeline.* emit's payload shape (see journal.go's
			// mergePayload calls) — internal/api/pipelines_exec.go's
			// ListRuns filters on json_extract(payload,'$.pipeline_id'),
			// so without these the verdict would never surface in the
			// routine runs tab's per-run entry list.
			Payload: map[string]any{"pipeline_id": pipelineID, "pipeline_slug": pipelineSlug, "run_id": runID},
		}
		if err := runverdict.GenerateAndEmit(ctx, emitter, provider, model, base, entries); err != nil {
			logger.Debug("run verdict: generate", "error", err, "run_id", runID)
		}
	}
}
