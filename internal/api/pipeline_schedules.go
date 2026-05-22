package api

import (
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
	CreatedAt             time.Time      `json:"created_at"`
	UpdatedAt             time.Time      `json:"updated_at"`
}

func (h *PipelineHandler) toScheduleResponse(s *pipeline.Schedule, slug string) scheduleResponse {
	var inputs map[string]any
	if s.InputsJSON != "" {
		_ = json.Unmarshal([]byte(s.InputsJSON), &inputs)
	}
	if inputs == nil {
		inputs = map[string]any{}
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
	// — same blast radius as Save. Same gate.
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		replyError(w, http.StatusForbidden, "Forbidden")
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

	in := pipeline.SaveScheduleInput{
		WorkspaceID:           workspaceID,
		Name:                  defaultIfBlank(body.Name, slug),
		TargetPipelineID:      pipelineID,
		TargetPipelineVersion: body.TargetPipelineVersion,
		CronExpr:              body.CronExpr,
		Timezone:              body.Timezone,
		Inputs:                body.Inputs,
		Enabled:               enabled,
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
	writeJSON(w, http.StatusCreated, h.toScheduleResponse(saved, slug))
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
	for _, s := range rows {
		slug, ok := slugCache[s.TargetPipelineID]
		if !ok {
			if p, perr := h.store.GetByID(r.Context(), s.TargetPipelineID); perr == nil {
				slug = p.Slug
			}
			slugCache[s.TargetPipelineID] = slug
		}
		out = append(out, h.toScheduleResponse(s, slug))
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
	writeJSON(w, http.StatusOK, h.toScheduleResponse(saved, slug))
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
