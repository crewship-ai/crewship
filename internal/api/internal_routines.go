package api

// Internal sidecar route for pipeline-schedule (routine) creation
// (PRD-SLASH-CAPABILITIES-2026 §6.4).
//
// Pattern mirror of internal_hire.go: the sidecar's /routines/schedules/
// create endpoint forwards here over X-Internal-Token; we inject the
// workspace + a MANAGER-tier role into the request context so the
// public PipelineHandler.CreateSchedule path runs unchanged.
//
// The role injection is the same belt-and-braces hack the hire adapter
// uses — it lets the sidecar-vouched call satisfy the public handler's
// canRole("create") gate without the sidecar binary needing to know
// the caller's actual workspace role. The CAPABILITY gate is the real
// security boundary for slash-initiated routine creation; that gate
// fires in commit 6's dual-path slash-action handler. The role check
// in CreateSchedule degrades to a no-op safety net once the capability
// path is the authoritative one (graduation milestone, post-rollout).
//
// Why route through the public handler instead of duplicating logic:
// CreateSchedule does cron-expression validation, timezone parsing,
// pipeline-slug→id resolution, audit emit, and SaveScheduleInput
// shaping. Forking that for the internal entry would drift over time.
// One handler, two surfaces (public + internal) keeps decisions
// consistent.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/policy"
)

// RoutineInternalAdapter wraps PipelineHandler.CreateSchedule so the
// internal /api/v1/internal/routines/schedules route can dispatch
// into it with workspace + role context injected from query params
// and the internal-token vouch.
//
// Dual-path enforcement (PRD-SLASH-CAPABILITIES-2026 §6.5):
//
//   - User-initiated slash command (X-Caller-User-Id present): gate
//     on the caller's routine.create capability. Denies with 403 and
//     a user-attributed audit entry.
//   - Autonomous-agent tool call (X-Caller-User-Id absent): gated HERE
//     on the calling crew's autonomy_level (#1768). This adapter used
//     to claim the autonomy gate "is enforced upstream of this handler
//     in the /spawn-style entry; this surface receives only
//     post-autonomy calls from the sidecar". Neither half was true —
//     the sidecar's routine_schedule.go passes headers through without
//     inspecting role, capability or caller identity, and no other
//     layer resolved a policy either, so a cron entry that outlives the
//     session that asked for it was created ungated at every autonomy
//     level. CreateSchedule's canRole("create") check saw only the
//     MANAGER role this adapter injects, so it could never refuse.
type RoutineInternalAdapter struct {
	pipes   *PipelineHandler
	policy  *policy.Resolver
	journal journal.Emitter
}

// NewRoutineInternalAdapter returns a wrapper that satisfies the
// http.HandlerFunc shape expected by router_internal.go's
// internalAuth wrapping. Construction at router-wiring time keeps
// the adapter dependency-free (it reuses the PipelineHandler the
// public router already instantiated).
func NewRoutineInternalAdapter(pipes *PipelineHandler) *RoutineInternalAdapter {
	return &RoutineInternalAdapter{pipes: pipes}
}

// SetAutonomyGate wires the shared per-crew autonomy resolver and the
// journal emitter the #1768 hold records through. A nil resolver leaves the
// adapter on the conservative guided default (staged, not refused, not waved
// through) — see gateInternalAction.
func (h *RoutineInternalAdapter) SetAutonomyGate(r *policy.Resolver, j journal.Emitter) {
	h.policy = r
	h.journal = j
}

// CreateSchedule reads workspace_id from the query (the sidecar
// attaches it via proxyToAPI), injects MANAGER role into the
// context, then calls the public PipelineHandler.CreateSchedule
// path. The cron parse, timezone validate, audit emit, and store
// write all fire in the public handler unchanged.
//
// We use MANAGER (the lowest tier the public CreateSchedule handler
// accepts via canRole("create")) rather than OWNER for the same
// reason internal_hire.go uses MANAGER: a future per-action audit
// gate that splits OWNER from MANAGER shouldn't silently grant the
// sidecar admin-equivalent privileges. The CAPABILITY gate in the
// dual-path slash-action handler (commit 6) covers the user-attributed
// path; the autonomy gate below covers the autonomous-agent one.
//
// #1768 arms for policy.ActionRoutineScheduleCreate:
//
//	strict          → 403. A standing cron grant is not something an
//	                  operator on strict can approve once — approving it
//	                  approves every future firing.
//	guided          → created ENABLED, with a non-blocking inbox notice. It
//	                  used to be held at enabled=0; the rebalance dropped
//	                  that because a hold and a notice give the operator the
//	                  same visibility — same row, same place, same schedule
//	                  named — and only one of them stops ordinary work. The
//	                  operator's lever is unchanged either way: PATCH
//	                  .../pipeline-schedules/{id} disables it in one call.
//	trusted/full    → created enabled, journal-only.
//
// The held branch below is therefore no longer reachable from any autonomy
// LEVEL. It is kept, and tested, because gateInternalAction's fail-closed
// fallback still returns InboxApprove when the policy resolver is unwired —
// a schedule created while the gate is broken must not fire.
func (h *RoutineInternalAdapter) CreateSchedule(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.pipes == nil {
		replyError(w, http.StatusInternalServerError, "routine adapter not configured")
		return
	}
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		replyError(w, http.StatusBadRequest, "workspace_id required")
		return
	}

	// Dual-path: user-attributed slash command vs autonomous-agent.
	// CallerUserIDFromRequest returns non-empty when the chat-bridge /
	// CLI repl propagated X-Caller-User-Id; empty for the agent
	// tool-call surface.
	callerID := CallerUserIDFromRequest(r)
	if callerID != "" {
		if !requireCapabilityOrForbid(w, r, h.pipes.logger, h.pipes.db,
			wsID, callerID,
			CapabilityRoutineCreate, "routine.create", "routine:new") {
			return
		}
	}

	gate, ok := gateInternalAction(w, r, h.policy, h.pipes.logger,
		r.URL.Query().Get("crew_id"), policy.ActionRoutineScheduleCreate, "Routine schedule creation")
	if !ok {
		return
	}

	// Force the sentinel into the body rather than trusting the caller's
	// `enabled`. The public handler owns the decode, so the only place to
	// pin the value is the bytes it will read.
	if gate.held() {
		patched, perr := forceScheduleDisabled(r)
		if perr != nil {
			replyError(w, http.StatusBadRequest, "invalid JSON body: "+perr.Error())
			return
		}
		r = patched
	}

	// Inject workspace + role context. Caller-identity propagation
	// (X-Caller-User-Id) flows through the headers untouched — the
	// underlying CreateSchedule reads UserFromContext for attribution.
	ctx := context.WithValue(r.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxRole, "MANAGER")
	if callerID != "" {
		// Real user identity for audit. Email is a debug-friendly
		// placeholder; downstream code paths that need name/email
		// query the users table by id.
		ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: callerID, Email: "x-internal"})
	}
	r = r.WithContext(ctx)

	if !gate.held() && !gate.wantsInbox() {
		// trusted → AutoLogJournal has no post-processing to do; let the
		// public handler write the response straight through.
		h.pipes.CreateSchedule(w, r)
		return
	}

	// Capture so the created schedule's id is available for the hold /
	// notice. CreateSchedule is the sole writer of the row, so intercepting
	// its response is the only way to learn the id without duplicating its
	// cron-parse / slug-resolve / save logic — the exact duplication this
	// adapter's docstring exists to avoid.
	rec := newCapturedResponse()
	h.pipes.CreateSchedule(rec, r)
	scheduleID, name := scheduleIdentityFromResponse(rec)
	if rec.status != http.StatusCreated {
		rec.flush(w)
		return
	}
	if scheduleID == "" {
		// A 201 whose body carries no id we can read. On the held arm that
		// is fatal: the row was written disabled and we have nothing to
		// hang the release on, so approving it would be impossible — refuse
		// rather than leave a schedule nobody can enable. On the notice arm
		// there is no sentinel to release, so pass the response through.
		if gate.held() {
			h.pipes.logger.Error("internal create schedule: 201 without a readable id — cannot stage the autonomy hold")
			replyError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		rec.flush(w)
		return
	}

	if gate.held() {
		approvalID, herr := writeAutonomyHold(r.Context(), h.pipes.db, h.pipes.logger, h.journal,
			gate, autonomyHold{
				WorkspaceID: wsID,
				CrewID:      gate.CrewID,
				Target:      autonomyTargetSchedule,
				TargetID:    scheduleID,
				InboxKind:   inbox.KindWaitpoint,
				Title:       "Routine schedule created by agent: " + name,
				BodyMD: fmt.Sprintf(
					"An agent in a `%s` crew created the cron schedule **%s**.\n\n"+
						"It is created **disabled** and will not fire until approved.",
					gate.Level, name),
				Reason: "agent created routine schedule " + name,
			})
		if herr != nil {
			h.pipes.logger.Error("internal create schedule: autonomy hold failed — compensating delete",
				"schedule_id", scheduleID, "error", herr)
			if _, derr := h.pipes.db.ExecContext(r.Context(),
				`DELETE FROM pipeline_schedules WHERE id = ? AND workspace_id = ?`,
				scheduleID, wsID); derr != nil {
				h.pipes.logger.Error("internal create schedule: compensating delete failed",
					"schedule_id", scheduleID, "error", derr)
			}
			replyError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		writeJSON(w, http.StatusAccepted, scheduleGateResponse(rec, gate, approvalID, true))
		return
	}

	writeAutonomyNotice(r.Context(), h.pipes.db, h.pipes.logger, gate, wsID,
		inbox.KindMessage, scheduleID,
		"Routine schedule created by agent: "+name,
		fmt.Sprintf("An agent created the cron schedule **%s**.", name))
	writeJSON(w, http.StatusCreated, scheduleGateResponse(rec, gate, "", false))
}

// forceScheduleDisabled rewrites the request body with "enabled": false so
// the public CreateSchedule persists the sentinel. Returns a request carrying
// the patched body; the original is fully consumed.
//
// A caller-supplied `enabled` is overwritten, not merged — the value is the
// sentinel, so honouring a `true` from the agent that is being gated would
// defeat the gate.
func forceScheduleDisabled(r *http.Request) (*http.Request, error) {
	raw, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, maxExecBodyBytes))
	if err != nil {
		return nil, err
	}
	var body map[string]any
	if len(bytes.TrimSpace(raw)) == 0 {
		body = map[string]any{}
	} else if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	body["enabled"] = false
	patched, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	out := r.Clone(r.Context())
	out.Body = io.NopCloser(bytes.NewReader(patched))
	out.ContentLength = int64(len(patched))
	return out, nil
}

// scheduleIdentityFromResponse pulls (id, name) out of a captured 201 body.
// Both are best-effort: an id we cannot read means we cannot stage a hold,
// which the caller turns into a refusal (held arm) rather than a silent
// ungated create.
func scheduleIdentityFromResponse(rec *capturedResponse) (id, name string) {
	var parsed struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(rec.body.Bytes(), &parsed); err != nil {
		return "", ""
	}
	if parsed.Name == "" {
		parsed.Name = parsed.ID
	}
	return parsed.ID, parsed.Name
}

// scheduleGateResponse re-emits the captured schedule payload with the gate
// verdict attached, so the calling agent can tell "created and running" from
// "created and waiting on a human".
func scheduleGateResponse(rec *capturedResponse, gate autonomyDecision, approvalID string, held bool) map[string]any {
	out := map[string]any{}
	if err := json.Unmarshal(rec.body.Bytes(), &out); err != nil {
		out = map[string]any{}
	}
	out["decision"] = string(gate.Decision)
	out["autonomy_level"] = string(gate.Level)
	out["pending_review"] = held
	if approvalID != "" {
		out["approval_id"] = approvalID
	}
	return out
}
