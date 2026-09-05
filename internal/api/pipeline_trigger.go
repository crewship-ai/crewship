package api

// Atomic routine authoring's optional trigger block (PRD-ISSUES-AND-
// ROUTINES-2026 §13.1, work package B8, #2359): a routine save may carry
// `"trigger": {...}` + `"activation": "draft"|""` alongside `definition`.
// pipeline.Store.SaveWithTrigger persists the routine, its version and this
// trigger in ONE transaction — all three exist afterward, or none do. This
// file is the HTTP-layer glue: wire shapes, and the draft-activation review
// item ("routine trigger activation" — distinct from the routine-risk
// governance proposal in pipeline_governance.go, which is about whether the
// DEFINITION may run at all, not whether its trigger may fire yet).
//
// Only trigger.kind "schedule" and "manual" are supported today. Webhook
// and automation-binding triggers are a follow-up (routine-author SKILL.md
// and docs/guides/routines.mdx say so) — see pipeline.TriggerKind.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

// triggerRequestBody is the wire shape of the optional `trigger` block.
// Field names match the PRD's own example verbatim (`cron`, not
// `cron_expr`) since this is a NEW surface with no legacy body to stay
// compatible with.
type triggerRequestBody struct {
	Kind                   string         `json:"kind"`
	CronExpr               string         `json:"cron"`
	Timezone               string         `json:"timezone"`
	CatchupPolicy          string         `json:"catchup_policy,omitempty"`
	MaxConsecutiveFailures int            `json:"max_consecutive_failures,omitempty"`
	Inputs                 map[string]any `json:"inputs,omitempty"`
}

// triggerInputFromBody converts the wire trigger block plus the top-level
// `activation` field into a pipeline.TriggerInput. Returns (nil, nil) when
// trigger is absent (the routine save behaves exactly as it did before B8).
// A malformed block is a user error (422), never a panic deeper in the
// store — validated here so the HTTP handler can reply before opening a
// transaction for a request that was never going to succeed.
func triggerInputFromBody(trigger *triggerRequestBody, activation string) (*pipeline.TriggerInput, error) {
	if trigger == nil {
		if activation != "" {
			return nil, errors.New("activation requires trigger")
		}
		return nil, nil
	}
	kind := pipeline.TriggerKind(strings.TrimSpace(trigger.Kind))
	switch kind {
	case pipeline.TriggerKindSchedule, pipeline.TriggerKindManual:
	case "":
		return nil, errors.New(`trigger.kind is required ("schedule" or "manual")`)
	default:
		return nil, fmt.Errorf(`unsupported trigger.kind %q (must be "schedule" or "manual")`, trigger.Kind)
	}
	if activation != "" && activation != pipeline.TriggerActivationDraft {
		return nil, fmt.Errorf(`unsupported activation %q (must be omitted or "draft")`, activation)
	}
	if kind == pipeline.TriggerKindManual && activation == pipeline.TriggerActivationDraft {
		return nil, errors.New(`activation "draft" has no meaning for trigger.kind "manual"`)
	}
	if kind == pipeline.TriggerKindSchedule && strings.TrimSpace(trigger.CronExpr) == "" {
		return nil, errors.New("trigger.cron is required for trigger.kind \"schedule\"")
	}
	return &pipeline.TriggerInput{
		Kind:                   kind,
		CronExpr:               trigger.CronExpr,
		Timezone:               trigger.Timezone,
		CatchupPolicy:          trigger.CatchupPolicy,
		MaxConsecutiveFailures: trigger.MaxConsecutiveFailures,
		Inputs:                 trigger.Inputs,
		Activation:             activation,
	}, nil
}

// triggerResponse reports what atomic authoring actually created — the
// piece the agent's skill and the CLI need to name the first fire time and
// say whether the trigger is live or awaiting approval.
type triggerResponse struct {
	Kind string `json:"kind"`
	// ScheduleID/CronExpr/Timezone/Enabled/FirstFireAt are populated only
	// when Kind == "schedule".
	ScheduleID  string  `json:"schedule_id,omitempty"`
	CronExpr    string  `json:"cron_expr,omitempty"`
	Timezone    string  `json:"timezone,omitempty"`
	Enabled     bool    `json:"enabled,omitempty"`
	FirstFireAt *string `json:"first_fire_at,omitempty"`
	// ApprovalRequired is true exactly when activation="draft" created the
	// trigger disabled and raised the review item below.
	ApprovalRequired bool   `json:"approval_required,omitempty"`
	InboxItemID      string `json:"inbox_item_id,omitempty"`
}

// toTriggerResponse builds the response fragment for what createTriggerTx
// actually created. trigger == nil (no trigger requested) yields a nil
// response; trigger.Kind == manual yields {"kind":"manual"} with no
// schedule fields; trigger.Kind == schedule requires sched != nil.
func toTriggerResponse(trigger *pipeline.TriggerInput, sched *pipeline.Schedule) *triggerResponse {
	if trigger == nil {
		return nil
	}
	if trigger.Kind == pipeline.TriggerKindManual || sched == nil {
		return &triggerResponse{Kind: string(pipeline.TriggerKindManual)}
	}
	out := &triggerResponse{
		Kind:             string(pipeline.TriggerKindSchedule),
		ScheduleID:       sched.ID,
		CronExpr:         sched.CronExpr,
		Timezone:         sched.Timezone,
		Enabled:          sched.Enabled,
		ApprovalRequired: sched.Activation == pipeline.TriggerActivationDraft,
	}
	if sched.NextRunAt != nil {
		t := sched.NextRunAt.Format("2006-01-02T15:04:05.999999999Z07:00")
		out.FirstFireAt = &t
	}
	return out
}

// pipelineSaveResponse wraps the ordinary pipeline save response with the
// trigger fragment, when one was requested. Embedding pipelineResponse
// keeps every existing field at the top level of the JSON object — callers
// that only cared about the routine see no shape change; `trigger` is new
// and additive.
type pipelineSaveResponse struct {
	pipelineResponse
	Trigger *triggerResponse `json:"trigger,omitempty"`
}

// isTriggerValidationError reports whether err came from createTriggerTx's
// validation (a bad cron expression, an unknown timezone, an invalid
// catchup_policy/activation, or an unsupported trigger.kind) rather than a
// genuine storage failure. Both are wrapped by the SAME "pipeline: save
// trigger" context in store.go, so this checks the inner sentinel
// (pipeline.ErrInvalidTrigger) with errors.Is instead of pattern-matching
// the combined message — a string check on the outer wrap alone would
// misclassify a real DB/infra failure (e.g. "insert schedule: database is
// locked") as a 422 validation error just because it shares that wrap.
func isTriggerValidationError(err error) bool {
	return errors.Is(err, pipeline.ErrInvalidTrigger)
}

// routineTriggerActivationInboxSource is the (kind, source_id) dedup key
// tying a draft trigger to its inbox review item — distinct from
// routineProposalInboxSource, which is about the routine DEFINITION's own
// risk review, not whether an already-approved definition's trigger may
// fire yet. Keyed on the schedule id: a routine can only ever author one
// trigger atomically today, but the key is schedule-scoped so it never
// collides with a routine-level key even if that changes.
func routineTriggerActivationInboxSource(workspaceID, scheduleID string) string {
	return "routinetrigger:" + workspaceID + ":" + scheduleID
}

// scheduleRoutineThreadKey resolves the same routineThreadKey
// (pipeline_governance.go) a trigger-activation review needs to merge with
// (or resolve via, when it merged) the routine's own governance card — it
// only has the pipeline id at hand, so it looks the slug up. Empty
// pipelineID or a lookup failure returns "" (WriteThreaded/
// ResolveByThreadOrSource both treat an empty thread key as "no thread",
// which degrades to the pre-B10 (kind, source_id)-only behaviour rather
// than failing the call).
func scheduleRoutineThreadKey(ctx context.Context, h *PipelineHandler, workspaceID, pipelineID string) string {
	if pipelineID == "" || h == nil || h.store == nil {
		return ""
	}
	p, err := h.store.GetByID(ctx, pipelineID)
	if err != nil || p == nil {
		return ""
	}
	return routineThreadKey(workspaceID, p.Slug)
}

// proposeTriggerActivationInbox raises the single MANAGER+ review item a
// draft trigger needs (B8's accept line: "draft activation raises one
// approval item with a receipt pinning the version"). The payload carries
// routine_version — the head version pipeline.Store just wrote in the SAME
// transaction as this schedule — so the review item and, once decided, its
// resolved_action/resolved_by_user_id/resolved_at (inbox_items' existing
// decision-record columns, §9.8) together are the receipt: which version
// was proposed, and what a MANAGER did about it. Best-effort, matching
// every other inbox raise in this file — a save that already committed
// must not be undone by a failure to also announce it.
func (h *PipelineHandler) proposeTriggerActivationInbox(ctx context.Context, workspaceID string, saved *pipeline.Pipeline, sched *pipeline.Schedule, senderName string) {
	if sched == nil {
		return
	}
	firstFire := ""
	if sched.NextRunAt != nil {
		firstFire = sched.NextRunAt.Format("2006-01-02T15:04:05.999999999Z07:00")
	}
	headVersion := 0
	if h.store != nil {
		if v, err := h.store.HeadVersion(ctx, saved.ID); err == nil {
			headVersion = v
		}
	}
	payload := map[string]interface{}{
		"kind":            "routine_trigger_activation",
		"slug":            saved.Slug,
		"pipeline_id":     saved.ID,
		"routine_version": headVersion,
		"schedule_id":     sched.ID,
		"cron_expr":       sched.CronExpr,
		"timezone":        sched.Timezone,
		"first_fire_at":   firstFire,
	}
	// WriteThreaded, not Upsert: routineThreadKey(workspaceID, saved.Slug) is
	// the SAME thread proposeRoutineInbox (pipeline_governance.go) uses, so
	// when one save is both risky AND carries a draft trigger, this call
	// merges into that card instead of raising a sibling — the fix for
	// #2364's live-observed duplicate.
	_ = inbox.WriteThreaded(ctx, h.db, h.logger, inbox.Item{
		WorkspaceID: workspaceID,
		Kind:        inbox.KindEscalation,
		SourceID:    routineTriggerActivationInboxSource(workspaceID, sched.ID),
		TargetRole:  "MANAGER",
		Title:       "Routine trigger ready: " + saved.Slug,
		BodyMD: fmt.Sprintf("Routine **%s** (version %d) is ready. First run would be %s. Activate the trigger?",
			saved.Slug, headVersion, firstFire),
		SenderType:     "pipeline",
		SenderName:     senderName,
		Priority:       "high",
		Blocking:       true,
		Payload:        payload,
		ThreadKey:      routineThreadKey(workspaceID, saved.Slug),
		AttentionClass: inbox.AttentionDecision,
		Actions: []inbox.Action{
			{ID: "activate_trigger", Label: "Activate", Effect: fmt.Sprintf("Enables the trigger; first run %s", firstFire), Irreversible: false},
			{ID: "dismiss_trigger", Label: "Dismiss", Effect: "Leaves the trigger disabled", Irreversible: false},
		},
	})
	h.broadcastInboxUpdated(workspaceID, "routine_trigger_proposed")
}
