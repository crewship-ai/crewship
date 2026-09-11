package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// maxDeferralSeconds bounds delay/ttl/debounce windows (30 days) — keeps
// fire_at arithmetic well inside int64 Duration range (no overflow → no
// accidental immediate fire) and rejects absurd far-future schedules.
const maxDeferralSeconds = 30 * 24 * 3600

// enqueueDeferredRun parks a delayed/debounced trigger in pending_runs.
// Always writes the HTTP response (scheduled receipt or error).
func (h *PipelineHandler) enqueueDeferredRun(w http.ResponseWriter, r *http.Request, workspaceID, invokingUser string, p *pipeline.Pipeline, body runRequestBody) {
	// Bound every duration field so a huge value can't overflow the
	// fire_at/expires_at arithmetic (which would wrap negative and fire
	// immediately). Reject rather than clamp so the caller sees the limit.
	for name, v := range map[string]int{
		"delay_seconds": body.DelaySeconds, "ttl_seconds": body.TTLSeconds,
		"debounce_window_seconds": body.DebounceWindowSecond, "debounce_max_seconds": body.DebounceMaxSeconds,
	} {
		if v < 0 || v > maxDeferralSeconds {
			replyError(w, http.StatusBadRequest, name+" out of range (0.."+strconv.Itoa(maxDeferralSeconds)+"s)")
			return
		}
	}
	now := time.Now().UTC()

	// fire_at: a debounce trigger fires window-seconds after the latest
	// trigger (default 30s); a plain delay fires delay-seconds out.
	fireAt := now.Add(time.Duration(body.DelaySeconds) * time.Second)
	if body.FireAt != "" {
		at, err := time.Parse(time.RFC3339, body.FireAt)
		if err != nil || !at.After(now) || at.After(now.AddDate(2, 0, 0)) {
			replyError(w, http.StatusBadRequest, "fire_at must be a future RFC3339 timestamp within two years, including timezone offset")
			return
		}
		if body.DelaySeconds != 0 || body.DebounceKey != "" || body.TTLSeconds != 0 {
			replyError(w, http.StatusBadRequest, "fire_at cannot be combined with delay, debounce or ttl")
			return
		}
		fireAt = at.UTC()
	}

	if body.DebounceKey != "" {
		window := body.DebounceWindowSecond
		if window <= 0 {
			window = 30
		}
		fireAt = now.Add(time.Duration(window) * time.Second)
	}

	var expiresAt *time.Time
	if body.TTLSeconds > 0 {
		e := now.Add(time.Duration(body.TTLSeconds) * time.Second)
		expiresAt = &e
	}
	var debounceMaxAt *time.Time
	if body.DebounceKey != "" && body.DebounceMaxSeconds > 0 {
		m := now.Add(time.Duration(body.DebounceMaxSeconds) * time.Second)
		debounceMaxAt = &m
	}

	inputsJSON := "{}"
	if len(body.Inputs) > 0 {
		if b, err := json.Marshal(body.Inputs); err == nil {
			inputsJSON = string(b)
		}
	}
	tagsJSON := "[]"
	if len(body.Tags) > 0 {
		if b, err := json.Marshal(body.Tags); err == nil {
			tagsJSON = string(b)
		}
	}

	// Pin the definition that passed preflight, not a concurrently changed
	// HEAD. Every deferral needs this, not only the one-time start (#2500):
	// a `delay_seconds` or debounced trigger is parked in the same table,
	// cleared the same governance/integration/resource/credential gates, and
	// hands the caller a receipt — so coming back minutes later running a
	// recipe that did not exist when they pressed Run is the same defect
	// whichever shape of deferral they used. A debounced burst keeps the
	// first trigger's pin, because coalescing makes it one logical trigger.
	if body.PinnedVersion == nil {
		var version int
		err := h.db.QueryRowContext(r.Context(), `SELECT version FROM pipeline_versions WHERE pipeline_id=? AND definition_hash=?`, p.ID, p.DefinitionHash).Scan(&version)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			h.logger.Error("deferred run: load archive", "error", err, "slug", p.Slug)
			replyError(w, http.StatusInternalServerError, "Could not load the published recipe archive.")
			return
		}
		switch {
		case version >= 1:
			body.PinnedVersion = &version
		case body.FireAt != "":
			// A one-time scheduled start IS the pin — refusing is right,
			// and this is the behaviour that shipped.
			replyError(w, http.StatusConflict, "Published recipe archive is unavailable. Publish a version before scheduling a one-time start.")
			return
		default:
			// A routine old enough to have no archived version can still be
			// deferred; it just runs unpinned, and the receipt says so
			// rather than the caller having to assume. Refusing here would
			// take away a run that works today to fix one that is rarer.
			h.logger.Warn("deferred run: no archived version to pin",
				"slug", p.Slug, "pipeline_id", p.ID)
		}
	}
	store := pipeline.NewPendingRunStore(h.db)
	id, coalesced, err := store.Enqueue(r.Context(), pipeline.PendingRun{
		PinnedVersion: body.PinnedVersion,
		ID:            "pnd_" + generateCUID(),
		WorkspaceID:   workspaceID,
		PipelineID:    p.ID,
		PipelineSlug:  p.Slug,
		InputsJSON:    inputsJSON,
		TagsJSON:      tagsJSON,
		MetadataJSON:  marshalMetadata(body.Metadata),
		TierOverride:  body.TierOverride,
		Priority:      body.Priority,
		DebounceKey:   body.DebounceKey,
		FireAt:        fireAt,
		ExpiresAt:     expiresAt,
		DebounceMaxAt: debounceMaxAt,
		// Carry the triggering user so a notify step's `to: trigger` in the
		// deferred run reaches the person who scheduled it, not a workspace
		// notice (issue #842 Phase 1). Empty for service/token triggers.
		InvokingUserID: invokingUser,
	})
	if err != nil {
		h.logger.Error("enqueue deferred run", "error", err, "slug", p.Slug)
		replyError(w, http.StatusInternalServerError, "enqueue deferred run")
		return
	}
	// The receipt must report the pin the ROW carries, not the one this
	// request computed. They differ exactly when the request coalesced into
	// an earlier trigger's window: the row keeps the first trigger's pin
	// (#2500), so a caller told "pinned_version: 2" here would be told the
	// wrong recipe for a run that will fire v1. Read it back.
	pinned := body.PinnedVersion
	if coalesced {
		if err := h.db.QueryRowContext(r.Context(),
			`SELECT pinned_version FROM pending_runs WHERE id = ?`, id).Scan(&pinned); err != nil {
			h.logger.Warn("deferred run: read coalesced pin", "error", err, "pending_id", id)
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":         "SCHEDULED",
		"pending_id":     id,
		"fire_at":        fireAt.Format(time.RFC3339Nano),
		"coalesced":      coalesced,
		"pinned_version": pinned,
		"priority":       body.Priority,
	})
}

// ListPendingRuns returns the workspace's not-yet-fired deferred runs.
// GET /api/v1/workspaces/{workspaceId}/pipelines/pending
func (h *PipelineHandler) ListPendingRuns(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if h.db == nil {
		replyError(w, http.StatusServiceUnavailable, "db not wired")
		return
	}
	store := pipeline.NewPendingRunStore(h.db)
	rows, err := store.ListPending(r.Context(), workspaceID, 100)
	if err != nil {
		replyError(w, http.StatusInternalServerError, "list pending runs")
		return
	}
	type dto struct {
		Inputs        map[string]any `json:"inputs"`
		PinnedVersion *int           `json:"pinned_version"`
		ID            string         `json:"id"`
		PipelineSlug  string         `json:"pipeline_slug"`
		DebounceKey   string         `json:"debounce_key,omitempty"`
		Priority      int            `json:"priority"`
		FireAt        string         `json:"fire_at"`
	}
	out := make([]dto, 0, len(rows))
	for _, pr := range rows {
		inputs, err := planPresetInputs(pr.InputsJSON)
		if err != nil {
			replyError(w, 500, "read pending inputs")
			return
		}
		out = append(out, dto{
			Inputs:        inputs,
			PinnedVersion: pr.PinnedVersion,
			ID:            pr.ID,
			PipelineSlug:  pr.PipelineSlug,
			DebounceKey:   pr.DebounceKey,
			Priority:      pr.Priority,
			FireAt:        pr.FireAt.Format(time.RFC3339Nano),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// CancelPendingRun cancels a not-yet-fired deferred run.
// POST /api/v1/workspaces/{workspaceId}/pipelines/pending/{pendingId}/cancel
func (h *PipelineHandler) CancelPendingRun(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "update") {
		return
	}
	workspaceID := WorkspaceIDFromContext(r.Context())
	pendingID := r.PathValue("pendingId")
	if pendingID == "" {
		replyError(w, http.StatusBadRequest, "pendingId required")
		return
	}
	if h.db == nil {
		replyError(w, http.StatusServiceUnavailable, "db not wired")
		return
	}
	store := pipeline.NewPendingRunStore(h.db)
	ok, err := store.Cancel(r.Context(), workspaceID, pendingID)
	if err != nil {
		replyError(w, http.StatusInternalServerError, "cancel pending run")
		return
	}
	if !ok {
		replyError(w, http.StatusNotFound, "pending run not found (already fired, expired, or cancelled)")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "cancelled": pendingID})
}
