package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/crewship-ai/crewship/internal/artifactrun"
	"github.com/crewship-ai/crewship/internal/featureflags"
	"github.com/crewship-ai/crewship/internal/journal"
)

// EntryArtifactRunCapabilityWithheld records that a scheduled routine step
// got no artifact run capability because its schedule's creator does not
// (or no longer) vouch for it (#5339). Payload: schedule_id, step_id, reason.
const EntryArtifactRunCapabilityWithheld journal.EntryType = "artifact.run_capability_withheld"

// Why a step of a schedule's fire gets no capability. Each is journaled.
const (
	withheldScheduleUnverified    = "schedule_fire_unverified"
	withheldScheduleCreatorAgent  = "schedule_saved_by_agent"
	withheldScheduleCreatorLegacy = "schedule_creator_unknown"
	withheldScheduleCreatorGone   = "schedule_creator_not_a_member"
	withheldScheduleCreatorFrozen = "schedule_creator_suspended"
	withheldScheduleTarget        = "schedule_target_mismatch"
)

// routineArtifactAdmission decides whether one routine step may get an
// artifact run capability (#5339), and which trigger it records as. Never
// when an agent invoked the run or authored the routine. Otherwise:
//
//   - a person's own start (UI, CLI or API, admitted as routine.run or
//     routine.batch, trigger manual): yes, as a routine run;
//   - the host's own fire of a schedule (the cron scheduler or the
//     schedule's "run now"), when a person created or last saved that
//     schedule and is still a member of the workspace and not suspended,
//     checked now, at this step: yes, as a scheduled run. The person who
//     pressed "run now" lends nothing; the schedule's creator decides;
//   - anything else (call_pipeline, a webhook, an automation, an issue, a
//     wake check, a delayed or debounced start, a queued occurrence, a
//     schedule an agent saved or whose creator is unknown): no.
//
// withheld is non-empty only for a schedule fire refused because of its
// creator, so the caller can say why in the journal.
func routineArtifactAdmission(ctx context.Context, db *sql.DB, req AgentStepRequest) (trigger, withheld string, ok bool, err error) {
	if req.InvokingAgentID != "" || req.AuthorAgentID != "" {
		return "", "", false, nil
	}
	if req.TriggeredVia == TriggeredViaSchedule {
		if req.ArtifactScheduleID == "" || req.ArtifactScheduleID != req.TriggeredByID {
			return "", withheldScheduleUnverified, false, nil
		}
		var kind, user, target string
		err := db.QueryRowContext(ctx, `SELECT COALESCE(creator_kind,''),COALESCE(creator_user_id,''),target_pipeline_id FROM pipeline_schedules WHERE id=? AND workspace_id=? AND deleted_at IS NULL`, req.ArtifactScheduleID, req.WorkspaceID).Scan(&kind, &user, &target)
		if errors.Is(err, sql.ErrNoRows) {
			return "", withheldScheduleUnverified, false, nil
		}
		if err != nil {
			return "", "", false, err
		}
		switch {
		case target != req.PipelineID:
			return "", withheldScheduleTarget, false, nil
		case kind == ScheduleCreatorAgent:
			return "", withheldScheduleCreatorAgent, false, nil
		case kind != ScheduleCreatorHuman || user == "":
			return "", withheldScheduleCreatorLegacy, false, nil
		}
		var suspended sql.NullString
		err = db.QueryRowContext(ctx, `SELECT u.suspended_at FROM workspace_members m JOIN users u ON u.id=m.user_id WHERE m.workspace_id=? AND m.user_id=?`, req.WorkspaceID, user).Scan(&suspended)
		if errors.Is(err, sql.ErrNoRows) {
			return "", withheldScheduleCreatorGone, false, nil
		}
		if err != nil {
			return "", "", false, err
		}
		if suspended.Valid {
			return "", withheldScheduleCreatorFrozen, false, nil
		}
		return artifactrun.TriggerScheduled, "", true, nil
	}
	if req.InvokingUserID == "" {
		return "", "", false, nil
	}
	if req.InvocationAuthority != RoutineRunAuthority && req.InvocationAuthority != RoutineBatchAuthority {
		return "", "", false, nil
	}
	if req.TriggeredVia != "" && req.TriggeredVia != TriggeredViaManual {
		return "", "", false, nil
	}
	return artifactrun.TriggerRoutine, "", true, nil
}

// recordRoutineArtifactRun records a routine step's run as a run without a
// human (#5339) before its agent starts and returns its artifact capability
// plus a function that ends it. "" means the step reaches no artifact. With
// the flag off nothing is recorded or journaled.
func recordRoutineArtifactRun(ctx context.Context, db *sql.DB, logger *slog.Logger, emitter journal.Emitter, req AgentStepRequest, crewID, agentID, runID string, timeout time.Duration) (string, func()) {
	noop := func() {}
	if db == nil || crewID == "" || runID == "" {
		return "", noop
	}
	trigger, withheld, ok, err := routineArtifactAdmission(ctx, db, req)
	if err != nil {
		if logger != nil {
			logger.Warn("unattended artifact run admission failed; the step reaches no artifact", "run_id", runID, "error", err)
		}
		return "", noop
	}
	if !ok {
		if withheld != "" {
			reportWithheldRoutineArtifactRun(ctx, db, logger, emitter, req, crewID, agentID, runID, withheld)
		}
		return "", noop
	}
	source := "routine:" + req.PipelineID + "/" + req.StepID
	if trigger == artifactrun.TriggerScheduled {
		source = "schedule:" + req.ArtifactScheduleID + "/" + source
	}
	if len(source) > 500 {
		source = source[:500]
	}
	capability, err := artifactrun.Record(ctx, db, artifactrun.Run{WorkspaceID: req.WorkspaceID, CrewID: crewID, AgentID: agentID, RunID: runID, Trigger: trigger, Source: source}, artifactrun.TTLFor(timeout))
	if err != nil {
		if logger != nil {
			logger.Warn("unattended artifact run not recorded; the step reaches no artifact", "run_id", runID, "error", err)
		}
		return "", noop
	}
	if capability == "" {
		return "", noop
	}
	return capability, func() {
		endCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := artifactrun.End(endCtx, db, runID); err != nil && logger != nil {
			logger.Warn("unattended artifact run end not recorded", "run_id", runID, "error", err)
		}
	}
}

// reportWithheldRoutineArtifactRun logs and journals why a schedule's fire
// got no artifact access, flag on only. It never changes the decision.
func reportWithheldRoutineArtifactRun(ctx context.Context, db *sql.DB, logger *slog.Logger, emitter journal.Emitter, req AgentStepRequest, crewID, agentID, runID, reason string) {
	if on, err := featureflags.IsEnabledWith(ctx, db, req.WorkspaceID, artifactrun.FlagKey); err != nil || !on {
		return
	}
	if logger != nil {
		logger.Info("scheduled routine step gets no artifact access", "run_id", runID, "schedule_id", req.TriggeredByID, "step_id", req.StepID, "reason", reason)
	}
	if emitter == nil {
		return
	}
	emitCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	if _, err := emitter.Emit(emitCtx, journal.Entry{
		WorkspaceID: req.WorkspaceID,
		CrewID:      crewID,
		AgentID:     agentID,
		Type:        EntryArtifactRunCapabilityWithheld,
		Severity:    journal.SeverityWarn,
		ActorType:   journal.ActorOrchestrator,
		ActorID:     req.PipelineRunID,
		TraceID:     runID,
		Summary:     "Scheduled step " + req.StepID + " runs without artifact access: " + reason,
		Payload:     map[string]any{"schedule_id": req.TriggeredByID, "pipeline_id": req.PipelineID, "step_id": req.StepID, "reason": reason},
	}); err != nil && logger != nil {
		logger.Warn("journal entry for withheld artifact access not written", "run_id", runID, "error", err)
	}
}
