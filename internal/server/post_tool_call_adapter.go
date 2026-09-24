// PR-C F4.2 wire-up: bridge orchestrator.PostToolCallObserver to the
// behaviorhook singleton installed by registerBehaviorHook. The adapter
// translates orchestrator.ToolCallObservation into the hooks.EventContext
// the behavior hook expects, then forwards to
// behaviorhook.Get().MaybeEvaluate.
//
// #2575: the sampled verdict is ROUTED, not just watched. The hook returns
// the evaluator's full result, and this adapter persists it through the same
// half the synchronous POST /api/v1/keeper/behavior endpoint uses
// (KeeperPhase2Handler.RecordSampledBehavior): a keeper_requests row for
// every fired sample, an inbox item when the verdict's PolicyDecision says
// so (WARN → non-blocking, ESCALATE → operator review, DENY/ESCALATE in
// block mode × strict/guided → blocking), and a journal entry for the
// timeline. Before this, only a block-mode DENY left any trace — WARN and
// ESCALATE verdicts were computed and silently dropped.
//
// Block decisions (DENY in block mode + strict/guided) also come back as a
// *hooks.BlockedError. We log + journal them — the tool call has already
// executed (this is EventPostToolCall, not pre), so hard-aborting the
// in-flight CLI process would require a wider orchestrator-stdin refactor.
// The block interrupts the NEXT tool call.
package server

import (
	"context"
	"database/sql"

	"log/slog"
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/hooks"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/keeper/behaviorhook"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/keeper/governance"
	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// behaviorRecorder persists a sampled behavior verdict the way the
// synchronous /api/v1/keeper/behavior endpoint would have — implemented by
// *api.KeeperPhase2Handler (RecordSampledBehavior). An interface so the
// observer stays testable and internal/server does not need the whole
// KeeperPhase2Handler surface.
type behaviorRecorder interface {
	RecordSampledBehavior(ctx context.Context, in api.SampledBehaviorInput) (string, error)
}

// postToolCallObserver implements orchestrator.PostToolCallObserver by
// forwarding to behaviorhook.Get(). nil-safe: when no hook is installed
// (e.g. dev build without ANTHROPIC_API_KEY), Observe is a no-op.
type postToolCallObserver struct {
	logger   *slog.Logger
	journ    journal.Emitter
	db       *sql.DB
	recorder behaviorRecorder
}

func newPostToolCallObserver(logger *slog.Logger, j journal.Emitter, db *sql.DB) *postToolCallObserver {
	return &postToolCallObserver{logger: logger, journ: j, db: db}
}

// withRecorder wires the sampled-verdict persistence surface (#2575).
// Without it the observer degrades to the pre-#2575 behaviour (block-only
// journaling) rather than dropping samples entirely — the recorder is what
// turns a sampled verdict into an inbox item, and a nil one is only
// expected in tests and partial bootstraps.
func (o *postToolCallObserver) withRecorder(r behaviorRecorder) *postToolCallObserver {
	o.recorder = r
	return o
}

// Observe is called from the orchestrator's tool_call event tap. The
// hot path is bounded by:
//   - behaviorhook's per-crew sampling counter, at the workspace's own
//     cadence (behavior_sample_every; default every 5th call),
//   - the configured Behavior aux-slot timeout (8s default on PR-B F3).
//
// We use a fresh background ctx with the slot timeout because the
// orchestrator already calls Observe from a goroutine and we don't want
// a long-running tool call's caller-ctx cancel to kill our LLM in-flight.
// The downside (hook keeps running after agent run aborts) is bounded
// by the slot timeout and is strictly preferable to losing audit data on
// every cancelled run.
func (o *postToolCallObserver) Observe(obs orchestrator.ToolCallObservation) {
	ec := hooks.EventContext{
		Event:       hooks.EventPostToolCall,
		WorkspaceID: obs.WorkspaceID,
		CrewID:      obs.CrewID,
		AgentID:     obs.AgentID,
		MissionID:   obs.MissionID,
		ToolName:    obs.ToolName,
		Payload:     obs.Payload,
	}

	// User-registered post_tool_call hooks (crewship hooks create --event
	// post_tool_call) fire on EVERY observed tool call, independent of the
	// built-in behavior-monitor path below. That path is gated by
	// workspace-level watchdog settings and a sampling cadence — both are
	// policy for the F4.2 monitor specifically, not a precondition for a
	// user's own hook to run. Wiring it here (rather than a new tap) is
	// what closes the gap: this Observe is already the one place every
	// tool_call event reaches for post_tool_call, per
	// dispatchToolCallObservers in orchestrator.go. Best-effort: a
	// dispatch error is logged, never lets a hook failure affect the
	// (already-completed) tool call.
	if obs.WorkspaceID != "" {
		if hookErr := hooks.Dispatch(context.Background(), o.db, o.journ, hooks.EventPostToolCall, ec); hookErr != nil {
			// A Block outcome on post_tool_call cannot un-run the tool call
			// that already happened (see the package doc comment above) —
			// BlockedError is still surfaced via the journal entry the
			// dispatcher's blocking pass writes, same as any other blocking
			// hook, so an operator sees it. Nothing further to abort here;
			// just log so a broken handler (not a Block) is diagnosable.
			o.logger.Warn("post_tool_call hook dispatch reported an error",
				"workspace_id", obs.WorkspaceID, "tool", obs.ToolName, "error", hookErr)
		}
	}

	hook := behaviorhook.Get()
	if hook == nil {
		return
	}
	// Workspace watchdog settings (#1001 M0/M3). The behavioral watchdog is
	// opt-in per workspace (default OFF): an unconfigured workspace is not
	// monitored until an OWNER/ADMIN enables it. One PK lookup per sampled
	// observation; the observer already runs off the hot path.
	gctx, gcancel := context.WithTimeout(context.Background(), 2*time.Second)
	gov := governance.Resolve(gctx, o.db, o.logger, obs.WorkspaceID)
	gcancel()
	if !gov.Enabled {
		return
	}
	// Bound the call ourselves. behaviorhook.MaybeEvaluate uses the ctx
	// for the underlying LLM call; the default behavior aux slot has an
	// 8s timeout but the caller's ctx must beat the LLM provider's
	// transport timeout to actually cancel cleanly.
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	// The sampling cadence rides out of the SAME row read that gated Enabled
	// above (#1001 M3). Handing it to the hook per call — rather than pushing it
	// onto the hook once at boot — is what makes it a setting: the hook is a
	// process-wide singleton, so a stored cadence would be one workspace's policy
	// applied to every workspace, and it would need a restart to change (#1556,
	// the same trap one subsystem over). A workspace that never set one resolves
	// to 0 here and the hook keeps its built-in default.
	sample, err := hook.MaybeEvaluateEvery(ctx, ec, int64(gov.BehaviorSampleEvery))
	if err != nil {
		// ErrNotConfigured — the hook's own dependency state, not this sample.
		o.logger.Debug("post_tool_call observer: behavior hook not configured", "error", err)
		return
	}
	if sample == nil {
		// Not sampled this call; common case.
		return
	}

	// Persistence gets its OWN deadline, not the leftover of the one the LLM
	// call spent: a slow evaluation (the aux slot allows 8s of the 12s above)
	// would otherwise hand the DB writes a nearly-spent context, and the
	// verdict would be lost to `context deadline exceeded` at the exact moment
	// it exists to be recorded. The slowest evaluations are the ones most
	// likely to return non-ALLOW verdicts — the ones that must not vanish.
	// Fresh from Background like the parent: the observer already runs
	// detached from the tool call, and the records must outlive a caller ctx
	// that was never this sample's to begin with.
	pctx, pcancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer pcancel()

	// #2575: route the verdict. Every fired sample becomes a keeper_requests
	// row and (when the PolicyDecision says so) an operator inbox item —
	// the same persistence the /api/v1/keeper/behavior endpoint applies, so
	// the sampled and synchronous paths surface one shape of finding.
	if sample.Verdict != nil {
		o.routeSampledVerdict(pctx, obs, *sample.Verdict)
	}

	// The interrupt, kept exactly as before: a block fires a hook.blocked
	// journal entry so the operator sees the tool sequence was cut short.
	// Direct nil check, NOT errors.As: Sample.Blocked is a concrete
	// *hooks.BlockedError, and a typed-nil error interface would make As
	// return true while handing back a nil pointer.
	if be := sample.Blocked; be != nil {
		o.logger.Warn("behaviorhook: PostToolCall sample returned BLOCK",
			"workspace_id", obs.WorkspaceID,
			"crew_id", obs.CrewID,
			"agent_id", obs.AgentID,
			"tool", obs.ToolName,
			"message", be.Result.Message)
		if o.journ != nil {
			_, _ = o.journ.Emit(pctx, journal.Entry{
				WorkspaceID: obs.WorkspaceID,
				CrewID:      obs.CrewID,
				AgentID:     obs.AgentID,
				MissionID:   obs.MissionID,
				Type:        journal.EntryHookBlocked,
				Severity:    journal.SeverityWarn,
				ActorType:   journal.ActorSystem,
				ActorID:     be.HookID,
				Summary:     "behavior monitor blocked next tool call (sampled)",
				Payload: map[string]any{
					"tool":     obs.ToolName,
					"message":  be.Result.Message,
					"source":   "behaviorhook_sampled",
					"hook_id":  be.HookID,
					"agent_id": obs.AgentID,
					"crew_id":  obs.CrewID,
				},
			})
		}
	}
}

// routeSampledVerdict persists one sampled verdict (#2575): the audit row +
// inbox fan-out through the shared recorder, plus a journal entry so the
// timeline carries every fired sample the way the credential path carries
// every decision.
//
// Severity follows the credential path's convention: DENY and ESCALATE are
// what an operator wants to see without scrolling; WARN and ALLOW are
// telemetry.
func (o *postToolCallObserver) routeSampledVerdict(ctx context.Context, obs orchestrator.ToolCallObservation, res gatekeeper.BehaviorReviewResult) {
	if o.recorder != nil {
		reqID, rerr := o.recorder.RecordSampledBehavior(ctx, api.SampledBehaviorInput{
			WorkspaceID: obs.WorkspaceID,
			CrewID:      obs.CrewID,
			AgentID:     obs.AgentID,
			ToolName:    obs.ToolName,
			Verdict:     res,
		})
		if rerr != nil {
			// Error, not Warn: this is exactly the silent-governance-failure
			// #1048 made the endpoint refuse — the difference is there is no
			// HTTP caller here to refuse. The next sampled tool call retries.
			o.logger.Error("post_tool_call observer: persisting sampled behavior verdict failed",
				"workspace_id", obs.WorkspaceID, "crew_id", obs.CrewID,
				"agent_id", obs.AgentID, "decision", string(res.Decision), "error", rerr)
		}
		if o.journ != nil {
			severity := journal.SeverityNotice
			switch res.Decision {
			case gatekeeper.BehaviorDeny, gatekeeper.BehaviorEscalate:
				severity = journal.SeverityWarn
			}
			payload := map[string]any{
				"tool":            obs.ToolName,
				"decision":        string(res.Decision),
				"reason":          res.Reason,
				"risk_score":      res.RiskScore,
				"policy_decision": string(res.PolicyDecision),
				"source":          "behaviorhook_sampled",
				"agent_id":        obs.AgentID,
				"crew_id":         obs.CrewID,
			}
			if reqID != "" {
				payload["request_id"] = reqID
			}
			_, _ = o.journ.Emit(ctx, journal.Entry{
				WorkspaceID: obs.WorkspaceID,
				CrewID:      obs.CrewID,
				AgentID:     obs.AgentID,
				MissionID:   obs.MissionID,
				Type:        journal.EntryKeeperDecision,
				Severity:    severity,
				ActorType:   journal.ActorKeeper,
				ActorID:     "keeper_behavior",
				Summary:     "behavior monitor sampled " + string(res.Decision) + " on " + obs.ToolName,
				Payload:     payload,
			})
		}
	}
}
