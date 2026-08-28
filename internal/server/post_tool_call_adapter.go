// PR-C F4.2 wire-up: bridge orchestrator.PostToolCallObserver to the
// behaviorhook singleton installed by registerBehaviorHook. The adapter
// translates orchestrator.ToolCallObservation into the hooks.EventContext
// the behavior hook expects, then forwards to
// behaviorhook.Get().MaybeEvaluate.
//
// Block decisions (DENY in block mode + strict/guided) come back as a
// *hooks.BlockedError. For MVP we log + journal them — the tool call
// has already executed (this is EventPostToolCall, not pre), so
// hard-aborting the in-flight CLI process would require a wider
// orchestrator-stdin refactor. The block surfaces operator-visibly via
// the inbox row the keeper_phase2 endpoint handler creates when the
// same evaluator runs through the synchronous /api/v1/keeper/behavior
// route — i.e. the inbox carries the same audit row regardless of
// trigger path.
package server

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/crewship-ai/crewship/internal/hooks"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/keeper/behaviorhook"
	"github.com/crewship-ai/crewship/internal/keeper/governance"
	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// postToolCallObserver implements orchestrator.PostToolCallObserver by
// forwarding to behaviorhook.Get(). nil-safe: when no hook is installed
// (e.g. dev build without ANTHROPIC_API_KEY), Observe is a no-op.
type postToolCallObserver struct {
	logger *slog.Logger
	journ  journal.Emitter
	db     *sql.DB
}

func newPostToolCallObserver(logger *slog.Logger, j journal.Emitter, db *sql.DB) *postToolCallObserver {
	return &postToolCallObserver{logger: logger, journ: j, db: db}
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
	blocked, fired := hook.MaybeEvaluateEvery(ctx, ec, int64(gov.BehaviorSampleEvery))
	if !fired {
		// Not sampled this call; common case.
		return
	}
	if blocked == nil {
		return
	}

	// A block fired. Log + journal so the operator's inbox doesn't
	// silently drop it (the synchronous endpoint path writes the inbox
	// row; this path is invoked asynchronously from the orchestrator
	// hot path, so the journal entry is the audit trail).
	var be *hooks.BlockedError
	if errors.As(blocked, &be) {
		o.logger.Warn("behaviorhook: PostToolCall sample returned BLOCK",
			"workspace_id", obs.WorkspaceID,
			"crew_id", obs.CrewID,
			"agent_id", obs.AgentID,
			"tool", obs.ToolName,
			"message", be.Result.Message)
		if o.journ != nil {
			_, _ = o.journ.Emit(ctx, journal.Entry{
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
