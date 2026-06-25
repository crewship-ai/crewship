package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// scheduleResponse is the wire shape for pipeline_schedules. We
// expose pipeline_slug alongside target_pipeline_id so the UI does
// not need a second roundtrip to render a recognizable label, and
// we strip inputs_json into a real object so the caller doesn't
// have to JSON-decode a string field nested inside JSON.
type scheduleResponse struct {
	ID                    string         `json:"id"`
	WorkspaceID           string         `json:"workspace_id"`
	Name                  string         `json:"name"`
	TargetPipelineID      string         `json:"target_pipeline_id"`
	TargetPipelineSlug    string         `json:"target_pipeline_slug,omitempty"`
	TargetPipelineVersion *int           `json:"target_pipeline_version,omitempty"`
	CronExpr              string         `json:"cron_expr"`
	Timezone              string         `json:"timezone"`
	Inputs                map[string]any `json:"inputs"`
	Enabled               bool           `json:"enabled"`
	LastRunAt             *time.Time     `json:"last_run_at,omitempty"`
	LastStatus            string         `json:"last_status,omitempty"`
	LastRunID             string         `json:"last_run_id,omitempty"`
	NextRunAt             *time.Time     `json:"next_run_at,omitempty"`
	// Wake gate — see pipeline.Schedule. WakeInputs is always a real
	// object (like Inputs) so callers don't branch on null; the
	// telemetry fields are omitted while zero to keep ungated
	// schedules' payloads unchanged.
	WakePipelineID   string         `json:"wake_pipeline_id,omitempty"`
	WakePipelineSlug string         `json:"wake_pipeline_slug,omitempty"`
	WakeInputs       map[string]any `json:"wake_inputs,omitempty"`
	WakeCheckCount   int            `json:"wake_check_count,omitempty"`
	WakeFireCount    int            `json:"wake_fire_count,omitempty"`
	LastWakeAt       *time.Time     `json:"last_wake_at,omitempty"`
	LastWakeStatus   string         `json:"last_wake_status,omitempty"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
}

func (h *PipelineHandler) toScheduleResponse(s *pipeline.Schedule, slug, wakeSlug string) scheduleResponse {
	var inputs map[string]any
	if s.InputsJSON != "" {
		_ = json.Unmarshal([]byte(s.InputsJSON), &inputs)
	}
	if inputs == nil {
		inputs = map[string]any{}
	}
	var wakeInputs map[string]any
	if s.WakePipelineID != "" && s.WakeInputsJSON != "" {
		if err := json.Unmarshal([]byte(s.WakeInputsJSON), &wakeInputs); err != nil {
			h.logger.Warn("unmarshal wake_inputs_json", "schedule_id", s.ID, "error", err)
		}
	}
	return scheduleResponse{
		ID:                    s.ID,
		WorkspaceID:           s.WorkspaceID,
		Name:                  s.Name,
		TargetPipelineID:      s.TargetPipelineID,
		TargetPipelineSlug:    slug,
		TargetPipelineVersion: s.TargetPipelineVersion,
		CronExpr:              s.CronExpr,
		Timezone:              s.Timezone,
		Inputs:                inputs,
		Enabled:               s.Enabled,
		LastRunAt:             s.LastRunAt,
		LastStatus:            s.LastStatus,
		LastRunID:             s.LastRunID,
		NextRunAt:             s.NextRunAt,
		WakePipelineID:        s.WakePipelineID,
		WakePipelineSlug:      wakeSlug,
		WakeInputs:            wakeInputs,
		WakeCheckCount:        s.WakeCheckCount,
		WakeFireCount:         s.WakeFireCount,
		LastWakeAt:            s.LastWakeAt,
		LastWakeStatus:        s.LastWakeStatus,
		CreatedAt:             s.CreatedAt,
		UpdatedAt:             s.UpdatedAt,
	}
}

type scheduleRequestBody struct {
	Name                  string         `json:"name"`
	TargetPipelineSlug    string         `json:"target_pipeline_slug"`
	TargetPipelineID      string         `json:"target_pipeline_id"`
	TargetPipelineVersion *int           `json:"target_pipeline_version,omitempty"`
	CronExpr              string         `json:"cron_expr"`
	Timezone              string         `json:"timezone"`
	Inputs                map[string]any `json:"inputs"`
	Enabled               *bool          `json:"enabled,omitempty"`
	// Wake gate. Pointers so PATCH can distinguish "absent — keep the
	// existing gate" from explicit `""` — clear it. Slug preferred
	// (UI-facing), ID accepted for CLI/API callers, same convention
	// as the target_pipeline pair above.
	WakePipelineSlug *string        `json:"wake_pipeline_slug,omitempty"`
	WakePipelineID   *string        `json:"wake_pipeline_id,omitempty"`
	WakeInputs       map[string]any `json:"wake_inputs,omitempty"`
}

// resolveWakePipeline validates a wake-gate reference at save time —
// this is the enforcement point for "wake checks are free":
//
//  1. the probe exists in this workspace,
//  2. it is NOT the schedule's own routine (self-gating runs the
//     target twice per tick), and
//  3. its definition declares `agentless: true`, so by the agentless
//     guarantee it can never invoke an LLM.
//
// Returns (id, slug, error); errors are user-facing 400 material.
func (h *PipelineHandler) resolveWakePipeline(r *http.Request, workspaceID, targetPipelineID, wakeID, wakeSlug string) (string, string, error) {
	var p *pipeline.Pipeline
	var err error
	switch {
	case wakeID != "":
		p, err = h.store.GetByID(r.Context(), wakeID)
		if err == nil && p.WorkspaceID != workspaceID {
			err = errors.New("not in this workspace")
		}
	case wakeSlug != "":
		p, err = h.store.GetBySlug(r.Context(), workspaceID, wakeSlug)
	default:
		return "", "", errors.New("wake_pipeline_slug or wake_pipeline_id required")
	}
	if err != nil {
		return "", "", errors.New("wake pipeline not found")
	}
	if p.ID == targetPipelineID {
		return "", "", errors.New("wake gate cannot reference the schedule's own routine")
	}
	dsl, err := pipeline.Parse([]byte(p.DefinitionJSON))
	if err != nil {
		return "", "", errors.New("wake pipeline definition does not parse")
	}
	if !dsl.Agentless {
		return "", "", errors.New("wake gate requires an agentless routine — declare \"agentless\": true in " + p.Slug + " (wake checks must be free of LLM spend)")
	}
	return p.ID, p.Slug, nil
}

// CreateSchedule POST /workspaces/{wsId}/pipeline-schedules
//
// Accepts either target_pipeline_slug or target_pipeline_id. Slug is
// the natural caller — UI fills target_pipeline_slug from the row
// the user clicks on. ID path is for the CLI (which already has the
// id from `crewship pipeline get`).
func (h *PipelineHandler) CreateSchedule(w http.ResponseWriter, r *http.Request) {
	if h.schedules == nil {
		replyError(w, http.StatusServiceUnavailable, "pipeline_schedules backend not wired")
		return
	}
	workspaceID := WorkspaceIDFromContext(r.Context())
	// Audit M2-promoted: creating a schedule fires the pipeline on cron
	// — same blast radius as Save. Layered gate: MANAGER+ role passes
	// straight through; MEMBER with explicit routine.create capability
	// also passes (slash command UX). PRD-SLASH-CAPABILITIES-2026 §6.
	role := RoleFromContext(r.Context())
	caller := UserFromContext(r.Context())
	callerID := ""
	if caller != nil {
		callerID = caller.ID
	}
	if !requireRoleOrCapabilityOrForbid(w, r, h.logger, h.db,
		workspaceID, callerID, role,
		CapabilityRoutineCreate, "routine.create", "workspace:"+workspaceID,
		"create") {
		return
	}

	var body scheduleRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if body.CronExpr == "" {
		replyError(w, http.StatusBadRequest, "cron_expr required")
		return
	}
	pipelineID, slug, err := h.resolveSchedulePipelineID(r, workspaceID, &body)
	if err != nil {
		replyError(w, http.StatusBadRequest, err.Error())
		return
	}

	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}

	wakeID, wakeSlug := "", ""
	if reqWakeID, reqWakeSlug, set := wakeRefFromBody(&body); set && (reqWakeID != "" || reqWakeSlug != "") {
		wakeID, wakeSlug, err = h.resolveWakePipeline(r, workspaceID, pipelineID, reqWakeID, reqWakeSlug)
		if err != nil {
			replyError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	in := pipeline.SaveScheduleInput{
		WorkspaceID:           workspaceID,
		Name:                  defaultIfBlank(body.Name, slug),
		TargetPipelineID:      pipelineID,
		TargetPipelineVersion: body.TargetPipelineVersion,
		CronExpr:              body.CronExpr,
		Timezone:              body.Timezone,
		Inputs:                body.Inputs,
		Enabled:               enabled,
		WakePipelineID:        wakeID,
		WakeInputs:            body.WakeInputs,
	}
	saved, err := h.schedules.Save(r.Context(), in)
	if err != nil {
		// Cron parse / timezone errors come back as plain errors —
		// surface them as 400 not 500 so the UI can show "fix the
		// cron expression" instead of "server error".
		if isUserScheduleError(err) {
			replyError(w, http.StatusBadRequest, err.Error())
			return
		}
		h.logger.Warn("create pipeline schedule", "error", err)
		replyError(w, http.StatusInternalServerError, "failed to create schedule")
		return
	}
	writeJSON(w, http.StatusCreated, h.toScheduleResponse(saved, slug, wakeSlug))
}

// wakeRefFromBody collapses the wake_pipeline_slug / wake_pipeline_id
// pointer pair into (id, slug, set). set=false means the caller didn't
// mention the gate at all (PATCH keeps the existing one); set=true
// with both values empty is an explicit clear.
func wakeRefFromBody(body *scheduleRequestBody) (id, slug string, set bool) {
	if body.WakePipelineID == nil && body.WakePipelineSlug == nil {
		return "", "", false
	}
	if body.WakePipelineID != nil {
		id = *body.WakePipelineID
	}
	if body.WakePipelineSlug != nil {
		slug = *body.WakePipelineSlug
	}
	return id, slug, true
}

// ListSchedules GET /workspaces/{wsId}/pipeline-schedules
func (h *PipelineHandler) ListSchedules(w http.ResponseWriter, r *http.Request) {
	if h.schedules == nil {
		replyError(w, http.StatusServiceUnavailable, "pipeline_schedules backend not wired")
		return
	}
	workspaceID := WorkspaceIDFromContext(r.Context())
	rows, err := h.schedules.List(r.Context(), workspaceID)
	if err != nil {
		h.logger.Warn("list pipeline schedules", "error", err)
		replyError(w, http.StatusInternalServerError, "failed to list schedules")
		return
	}
	out := make([]scheduleResponse, 0, len(rows))
	// Resolve pipeline slugs once per unique target so the UI can
	// render the schedule next to the pipeline name. Avoid N+1 with
	// a small in-memory cache.
	slugCache := map[string]string{}
	lookupSlug := func(pipelineID string) string {
		if pipelineID == "" {
			return ""
		}
		slug, ok := slugCache[pipelineID]
		if !ok {
			if p, perr := h.store.GetByID(r.Context(), pipelineID); perr == nil {
				slug = p.Slug
			}
			slugCache[pipelineID] = slug
		}
		return slug
	}
	for _, s := range rows {
		out = append(out, h.toScheduleResponse(s, lookupSlug(s.TargetPipelineID), lookupSlug(s.WakePipelineID)))
	}
	writeJSON(w, http.StatusOK, out)
}

// UpdateSchedule PATCH /workspaces/{wsId}/pipeline-schedules/{scheduleId}
//
// Whole-row replace semantics: caller sends the post-edit state. This
// matches how the UI thinks about schedules ("save form") and is
// simpler than partial-update merging logic.
func (h *PipelineHandler) UpdateSchedule(w http.ResponseWriter, r *http.Request) {
	if h.schedules == nil {
		replyError(w, http.StatusServiceUnavailable, "pipeline_schedules backend not wired")
		return
	}
	workspaceID := WorkspaceIDFromContext(r.Context())
	// Audit M2-promoted: edits to an active schedule shift firing
	// behaviour / inputs — manage tier matches the post-promotion
	// destructive-mutation contract.
	role := RoleFromContext(r.Context())
	if !canRole(role, "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	scheduleID := r.PathValue("scheduleId")
	if scheduleID == "" {
		replyError(w, http.StatusBadRequest, "scheduleId required")
		return
	}
	existing, err := h.schedules.GetByID(r.Context(), scheduleID)
	if err != nil {
		if errors.Is(err, pipeline.ErrNotFound) {
			replyError(w, http.StatusNotFound, "schedule not found")
			return
		}
		replyError(w, http.StatusInternalServerError, "failed to load schedule")
		return
	}
	if existing.WorkspaceID != workspaceID {
		replyError(w, http.StatusNotFound, "schedule not found")
		return
	}

	var body scheduleRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}

	pipelineID := existing.TargetPipelineID
	slug := ""
	if body.TargetPipelineSlug != "" || body.TargetPipelineID != "" {
		pid, sl, err := h.resolveSchedulePipelineID(r, workspaceID, &body)
		if err != nil {
			replyError(w, http.StatusBadRequest, err.Error())
			return
		}
		pipelineID = pid
		slug = sl
	} else if p, perr := h.store.GetByID(r.Context(), pipelineID); perr == nil {
		slug = p.Slug
	}

	cronExpr := body.CronExpr
	if cronExpr == "" {
		cronExpr = existing.CronExpr
	}
	tz := body.Timezone
	if tz == "" {
		tz = existing.Timezone
	}
	enabled := existing.Enabled
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	inputs := body.Inputs
	if inputs == nil {
		// Empty body Inputs → keep existing
		_ = json.Unmarshal([]byte(existing.InputsJSON), &inputs)
	}

	// Wake gate merge: absent fields keep the existing gate, an
	// explicit empty ref clears it, a non-empty ref re-validates
	// (exists, agentless, not self) like create.
	wakeID := existing.WakePipelineID
	wakeSlug := ""
	if reqWakeID, reqWakeSlug, set := wakeRefFromBody(&body); set {
		if reqWakeID == "" && reqWakeSlug == "" {
			wakeID = ""
		} else {
			wakeID, wakeSlug, err = h.resolveWakePipeline(r, workspaceID, pipelineID, reqWakeID, reqWakeSlug)
			if err != nil {
				replyError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
	} else if wakeID != "" {
		// Unchanged gate — re-check it didn't become the new target
		// (a PATCH that retargets the schedule onto its own probe).
		if wakeID == pipelineID {
			replyError(w, http.StatusBadRequest, "wake gate cannot reference the schedule's own routine")
			return
		}
		if p, perr := h.store.GetByID(r.Context(), wakeID); perr == nil {
			wakeSlug = p.Slug
		}
	}
	wakeInputs := body.WakeInputs
	if wakeInputs == nil && wakeID != "" {
		_ = json.Unmarshal([]byte(existing.WakeInputsJSON), &wakeInputs)
	}

	in := pipeline.SaveScheduleInput{
		ID:                    scheduleID,
		WorkspaceID:           workspaceID,
		Name:                  defaultIfBlank(body.Name, existing.Name),
		TargetPipelineID:      pipelineID,
		TargetPipelineVersion: body.TargetPipelineVersion,
		CronExpr:              cronExpr,
		Timezone:              tz,
		Inputs:                inputs,
		Enabled:               enabled,
		WakePipelineID:        wakeID,
		WakeInputs:            wakeInputs,
	}
	saved, err := h.schedules.Save(r.Context(), in)
	if err != nil {
		if isUserScheduleError(err) {
			replyError(w, http.StatusBadRequest, err.Error())
			return
		}
		h.logger.Warn("update pipeline schedule", "error", err)
		replyError(w, http.StatusInternalServerError, "failed to update schedule")
		return
	}
	writeJSON(w, http.StatusOK, h.toScheduleResponse(saved, slug, wakeSlug))
}

// DeleteSchedule DELETE /workspaces/{wsId}/pipeline-schedules/{scheduleId}
//
// Soft delete; the scheduler skips deleted_at IS NOT NULL rows on
// the next tick, so a scheduled run that's already in flight will
// finish but no new runs will fire.
func (h *PipelineHandler) DeleteSchedule(w http.ResponseWriter, r *http.Request) {
	if h.schedules == nil {
		replyError(w, http.StatusServiceUnavailable, "pipeline_schedules backend not wired")
		return
	}
	workspaceID := WorkspaceIDFromContext(r.Context())
	// Audit M2-promoted: stop a recurring fire trigger -- delete tier.
	role := RoleFromContext(r.Context())
	if !canRole(role, "delete") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	scheduleID := r.PathValue("scheduleId")
	if scheduleID == "" {
		replyError(w, http.StatusBadRequest, "scheduleId required")
		return
	}
	existing, err := h.schedules.GetByID(r.Context(), scheduleID)
	if err != nil {
		if errors.Is(err, pipeline.ErrNotFound) {
			replyError(w, http.StatusNotFound, "schedule not found")
			return
		}
		replyError(w, http.StatusInternalServerError, "failed to load schedule")
		return
	}
	if existing.WorkspaceID != workspaceID {
		replyError(w, http.StatusNotFound, "schedule not found")
		return
	}
	if err := h.schedules.SoftDelete(r.Context(), scheduleID); err != nil {
		h.logger.Warn("delete pipeline schedule", "error", err)
		replyError(w, http.StatusInternalServerError, "failed to delete schedule")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// resolveSchedulePipelineID figures out which pipeline the schedule
// is bound to. Slug is preferred (UI-facing); ID is accepted for
// CLI/API callers that already know it.
//
// Returns (id, slug, error). The slug round-trip is so the response
// includes target_pipeline_slug without a follow-up query.
func (h *PipelineHandler) resolveSchedulePipelineID(r *http.Request, workspaceID string, body *scheduleRequestBody) (string, string, error) {
	if body.TargetPipelineID != "" {
		p, err := h.store.GetByID(r.Context(), body.TargetPipelineID)
		if err != nil {
			return "", "", errors.New("target_pipeline_id not found")
		}
		if p.WorkspaceID != workspaceID {
			return "", "", errors.New("target_pipeline_id not in this workspace")
		}
		return p.ID, p.Slug, nil
	}
	if body.TargetPipelineSlug != "" {
		p, err := h.store.GetBySlug(r.Context(), workspaceID, body.TargetPipelineSlug)
		if err != nil {
			return "", "", errors.New("target_pipeline_slug not found")
		}
		return p.ID, p.Slug, nil
	}
	return "", "", errors.New("target_pipeline_slug or target_pipeline_id required")
}

// isUserScheduleError sniffs error strings from the schedule store
// that come from caller-supplied data (cron expr, timezone). The
// store wraps these with stable prefixes so we can map to 400 here
// without pattern-matching deep error chains.
func isUserScheduleError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.HasPrefix(msg, "invalid cron expression") ||
		strings.HasPrefix(msg, "invalid timezone") ||
		strings.HasPrefix(msg, "pipeline_schedules:")
}

func defaultIfBlank(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// RunSchedule force-fires a schedule out of cycle: it runs the schedule's
// target pipeline NOW with the schedule's stored inputs, without touching
// the cron cadence (no recordRun, so next_run_at is unchanged). This is the
// server side of `crewship routine schedules now <id>`, which previously
// 404'd because the endpoint didn't exist.
//
// POST /api/v1/workspaces/{workspaceId}/pipeline-schedules/{scheduleId}/run
func (h *PipelineHandler) RunSchedule(w http.ResponseWriter, r *http.Request) {
	if h.schedules == nil {
		replyError(w, http.StatusServiceUnavailable, "pipeline_schedules backend not wired")
		return
	}
	if h.runner == nil {
		replyError(w, http.StatusServiceUnavailable,
			"pipeline runner not wired (orchestrator not booted yet, or this build was assembled without the runner adapter)")
		return
	}
	workspaceID := WorkspaceIDFromContext(r.Context())
	// Firing a schedule runs a pipeline — same tier as a manual run.
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	scheduleID := r.PathValue("scheduleId")
	if scheduleID == "" {
		replyError(w, http.StatusBadRequest, "scheduleId required")
		return
	}

	sched, err := h.schedules.GetByID(r.Context(), scheduleID)
	if err != nil {
		if errors.Is(err, pipeline.ErrNotFound) {
			replyError(w, http.StatusNotFound, "schedule not found")
			return
		}
		replyError(w, http.StatusInternalServerError, "failed to load schedule")
		return
	}
	if sched.WorkspaceID != workspaceID {
		replyError(w, http.StatusNotFound, "schedule not found")
		return
	}

	p, err := h.store.GetByID(r.Context(), sched.TargetPipelineID)
	if err != nil {
		if errors.Is(err, pipeline.ErrNotFound) {
			replyError(w, http.StatusNotFound, "schedule target pipeline not found")
			return
		}
		h.logger.Error("schedule run: load target", "error", err, "schedule", scheduleID)
		replyError(w, http.StatusInternalServerError, "load target pipeline")
		return
	}
	// Defense-in-depth tenant isolation: the schedule is already scoped to
	// this workspace, but its target is loaded by pipeline ID alone — a row
	// pointing at a foreign pipeline must not run it here. Surface as 404,
	// same as a missing target.
	if p.WorkspaceID != workspaceID {
		replyError(w, http.StatusNotFound, "schedule target pipeline not found")
		return
	}

	var inputs map[string]any
	if sched.InputsJSON != "" {
		// Don't silently drop a corrupt stored payload — that would force-fire
		// the run with nil inputs (wrong run shape). Fail loudly instead.
		if err := json.Unmarshal([]byte(sched.InputsJSON), &inputs); err != nil {
			h.logger.Error("schedule run: malformed stored inputs", "error", err, "schedule", scheduleID)
			replyError(w, http.StatusInternalServerError, "schedule has malformed stored inputs")
			return
		}
	}

	exec := h.newExecutor()
	// Detach from the request context for the same reason as the manual
	// run path (see PipelineHandler.Run): a wait/approval step parks the
	// run past the reverse proxy's per-request budget, and tying it to
	// r.Context() would cancel + fail it at the gate. WithoutCancel keeps
	// auth/workspace/trace values while shedding cancellation so a manually
	// fired scheduled run with an approval gate still resumes.
	res, err := exec.Run(context.WithoutCancel(r.Context()), pipeline.RunInput{
		PipelineID:    p.ID,
		WorkspaceID:   workspaceID,
		Inputs:        inputs,
		Mode:          pipeline.ModeRun,
		TriggeredVia:  pipeline.TriggeredViaSchedule,
		TriggeredByID: sched.ID,
	})
	if err != nil {
		if errors.Is(err, pipeline.ErrConcurrencyLimitReached) {
			w.Header().Set("Retry-After", "5")
			replyError(w, http.StatusTooManyRequests,
				"concurrency limit reached for this pipeline: another run with the same concurrency_key is already in flight")
			return
		}
		h.logger.Error("schedule run: exec", "error", err, "schedule", scheduleID)
		replyError(w, http.StatusInternalServerError, "Failed to start scheduled run")
		return
	}
	writeJSON(w, http.StatusOK, res)
}
