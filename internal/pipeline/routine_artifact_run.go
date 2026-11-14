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

// Why a step of a schedule's fire gets no capability. These are what the
// journal says; a person's standing is one generic reason there (journal
// readers need not learn why a colleague lost standing), with the detail in
// the server log only.
const (
	withheldScheduleUnverified    = "schedule_fire_unverified"
	withheldScheduleCreatorAgent  = "schedule_saved_by_agent"
	withheldScheduleCreatorLegacy = "schedule_creator_unknown"
	withheldScheduleStanding      = "schedule_creator_not_in_good_standing"
	withheldScheduleTarget        = "schedule_target_mismatch"
)

// scheduleCreatorRoles may create schedules on their own role (MANAGER+);
// only they can vouch for a schedule's fire.
var scheduleCreatorRoles = map[string]bool{"OWNER": true, "ADMIN": true, "MANAGER": true}

// withheldJournaled journals a withheld schedule fire once per fire, not once
// per step.
var withheldJournaled artifactrun.Once

// routineArtifactAdmission decides whether one routine step may get an
// artifact run capability (#5339), and which trigger it records as. Never
// when an agent invoked the run or authored the routine. Otherwise:
//
//   - a person's own start (UI, CLI or API, admitted as routine.run or
//     routine.batch, trigger manual): yes, as a routine run;
//   - the host's own fire of a schedule (the cron scheduler or the
//     schedule's "run now"): yes, as a scheduled run, only when a person
//     created or last changed what the schedule runs and, checked now at
//     this step, that person still holds the same membership of the
//     workspace (not left and rejoined) with a MANAGER+ role, is a trusted
//     (not restricted) member, is not suspended, and has not had their
//     access revoked since. The person who pressed "run now" lends nothing;
//   - anything else (call_pipeline, a webhook, an automation, an issue, a
//     wake check, a delayed or debounced start, a queued occurrence, a
//     resumed run, a schedule an agent saved or whose creator is unknown):
//     no.
//
// withheld (the journal reason) is set only for a schedule fire refused
// because of the fire or its schedule; detail adds the server-log detail.
func routineArtifactAdmission(ctx context.Context, db *sql.DB, req AgentStepRequest) (trigger, withheld, detail string, ok bool, err error) {
	if req.InvokingAgentID != "" || req.AuthorAgentID != "" {
		return "", "", "", false, nil
	}
	if req.TriggeredVia == TriggeredViaSchedule {
		return scheduleFireAdmission(ctx, db, req)
	}
	if req.InvokingUserID == "" {
		return "", "", "", false, nil
	}
	if req.InvocationAuthority != RoutineRunAuthority && req.InvocationAuthority != RoutineBatchAuthority {
		return "", "", "", false, nil
	}
	if req.TriggeredVia != "" && req.TriggeredVia != TriggeredViaManual {
		return "", "", "", false, nil
	}
	return artifactrun.TriggerRoutine, "", "", true, nil
}

func scheduleFireAdmission(ctx context.Context, db *sql.DB, req AgentStepRequest) (trigger, withheld, detail string, ok bool, err error) {
	if req.ArtifactScheduleID == "" || req.ArtifactScheduleID != req.TriggeredByID {
		return "", withheldScheduleUnverified, "", false, nil
	}
	var kind, user, member, setAt, target string
	err = db.QueryRowContext(ctx, `SELECT COALESCE(creator_kind,''),COALESCE(creator_user_id,''),COALESCE(creator_member_id,''),COALESCE(creator_set_at,''),target_pipeline_id FROM pipeline_schedules WHERE id=? AND workspace_id=? AND deleted_at IS NULL`, req.ArtifactScheduleID, req.WorkspaceID).Scan(&kind, &user, &member, &setAt, &target)
	if errors.Is(err, sql.ErrNoRows) {
		return "", withheldScheduleUnverified, "", false, nil
	}
	if err != nil {
		return "", "", "", false, err
	}
	switch {
	case target != req.PipelineID:
		return "", withheldScheduleTarget, "", false, nil
	case kind == ScheduleCreatorAgent:
		return "", withheldScheduleCreatorAgent, "", false, nil
	case kind != ScheduleCreatorHuman || user == "" || member == "" || setAt == "":
		return "", withheldScheduleCreatorLegacy, "", false, nil
	}
	var role, mode string
	var suspended, revoked sql.NullString
	err = db.QueryRowContext(ctx, `SELECT m.role,m.access_mode,u.suspended_at,u.access_revoked_at FROM workspace_members m JOIN users u ON u.id=m.user_id WHERE m.id=? AND m.workspace_id=? AND m.user_id=?`, member, req.WorkspaceID, user).Scan(&role, &mode, &suspended, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return "", withheldScheduleStanding, "creator is no longer the member who saved the schedule (left, or left and rejoined)", false, nil
	}
	if err != nil {
		return "", "", "", false, err
	}
	switch {
	case suspended.Valid:
		return "", withheldScheduleStanding, "creator is suspended", false, nil
	case revoked.Valid && revokedSince(revoked.String, setAt):
		return "", withheldScheduleStanding, "creator's access was revoked after they saved the schedule", false, nil
	case !scheduleCreatorRoles[role]:
		return "", withheldScheduleStanding, "creator's role " + role + " can no longer create schedules", false, nil
	case mode != "trusted":
		return "", withheldScheduleStanding, "creator is a restricted member", false, nil
	}
	return artifactrun.TriggerScheduled, "", "", true, nil
}

// revokedSince reports whether a revocation at revokedAt is not older than
// the creator write at setAt. Anything unreadable counts as revoked.
func revokedSince(revokedAt, setAt string) bool {
	r, err1 := time.Parse(time.RFC3339Nano, revokedAt) // tsformat:allow: comparing two stored instants; nothing is written
	s, err2 := time.Parse(time.RFC3339Nano, setAt)     // tsformat:allow: comparing two stored instants; nothing is written
	return err1 != nil || err2 != nil || !r.Before(s)
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
	// Flag first: with it off nothing is read, recorded or journaled.
	if on, err := featureflags.IsEnabledWith(ctx, db, req.WorkspaceID, artifactrun.FlagKey); err != nil || !on {
		return "", noop
	}
	trigger, withheld, detail, ok, err := routineArtifactAdmission(ctx, db, req)
	if err != nil {
		if logger != nil {
			logger.Warn("unattended artifact run admission failed; the step reaches no artifact", "run_id", runID, "error", err)
		}
		return "", noop
	}
	if !ok {
		if withheld != "" {
			reportWithheldRoutineArtifactRun(ctx, logger, emitter, req, crewID, agentID, runID, withheld, detail)
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

// reportWithheldRoutineArtifactRun logs (with detail) and journals (without
// it) why a schedule's fire got no artifact access, once per fire. It never
// changes the decision. The schedule is named only when the host vouched for
// the fire; an unverified claim's schedule id came from the caller.
func reportWithheldRoutineArtifactRun(ctx context.Context, logger *slog.Logger, emitter journal.Emitter, req AgentStepRequest, crewID, agentID, runID, reason, detail string) {
	if logger != nil {
		logger.Info("scheduled routine step gets no artifact access", "run_id", runID, "schedule_id", req.ArtifactScheduleID, "claimed_schedule_id", req.TriggeredByID, "step_id", req.StepID, "reason", reason, "detail", detail)
	}
	fire := req.PipelineRunID
	if fire == "" {
		fire = runID
	}
	if emitter == nil || !withheldJournaled.First(fire) {
		return
	}
	payload := map[string]any{"pipeline_id": req.PipelineID, "pipeline_run_id": req.PipelineRunID, "step_id": req.StepID, "reason": reason}
	if reason != withheldScheduleUnverified {
		payload["schedule_id"] = req.ArtifactScheduleID
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
		Summary:     "Scheduled routine runs without artifact access: " + reason,
		Payload:     payload,
	}); err != nil && logger != nil {
		logger.Warn("journal entry for withheld artifact access not written", "run_id", runID, "error", err)
	}
}
