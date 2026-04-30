package hooks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/crewship-ai/crewship/internal/journal"
)

// Dispatch is the single entry point call sites use. Flow:
//
//  1. Load every enabled hook for (workspaceID, event) whose crew scope is
//     compatible with ec.CrewID.
//  2. Filter the list by Matcher.Matches(m, ec).
//  3. Execute blocking hooks synchronously in registration order. The
//     first OutcomeBlock short-circuits with a *BlockedError — the
//     caller uses errors.As to recover and abort the operation.
//  4. Execute non-blocking hooks in goroutines so the hot path is not
//     gated on webhook latency.
//
// Non-fatal errors (individual handler failures, journal emit failures)
// are aggregated into the returned error via errors.Join so the caller
// can log them without losing the Block short-circuit signal.
func Dispatch(ctx context.Context, db *sql.DB, emitter journal.Emitter, event Event, ec EventContext) error {
	if ec.WorkspaceID == "" {
		return errors.New("hooks: Dispatch requires workspace_id")
	}
	ec.Event = event

	hooks, err := ListByEvent(ctx, db, ec.WorkspaceID, ec.CrewID, event)
	if err != nil {
		return fmt.Errorf("hooks: dispatch: %w", err)
	}

	// Apply the Matcher filter once up-front so we can cheaply iterate the
	// filtered set twice (blocking pass + non-blocking spawn).
	filtered := make([]Hook, 0, len(hooks))
	for _, h := range hooks {
		if Matches(h.Matcher, ec) {
			filtered = append(filtered, h)
		}
	}
	if len(filtered) == 0 {
		return nil
	}

	var errs []error

	// Blocking pass: sequential, stop on first Block. A blocking hook that
	// returns OutcomeError is logged but does NOT short-circuit — we treat
	// "handler broken" as "fail open" so a buggy webhook can't wedge the
	// platform. Operators see the error in the journal.
	for _, h := range filtered {
		if !h.Blocking {
			continue
		}
		res, runErr := runHandler(ctx, h, ec)
		emitFired(ctx, emitter, h, ec, res, runErr)
		if runErr != nil {
			errs = append(errs, fmt.Errorf("hook %s: %w", h.ID, runErr))
			continue
		}
		if res.Outcome == OutcomeBlock {
			emitBlocked(ctx, emitter, h, ec, res)
			blocked := &BlockedError{HookID: h.ID, Event: event, Result: res}
			if len(errs) > 0 {
				errs = append(errs, blocked)
				return errors.Join(errs...)
			}
			return blocked
		}
	}

	// Non-blocking pass: fire-and-forget goroutines. We deliberately use
	// context.Background() so the caller's context cancellation (e.g.
	// request done) doesn't kill background hook execution mid-flight.
	// The per-handler timeouts still bound runtime so a hung webhook
	// can't leak goroutines forever. We don't wait on them — caller
	// returns immediately and the handlers land their records in the
	// journal on their own schedule.
	for _, h := range filtered {
		if h.Blocking {
			continue
		}
		h := h
		go func() {
			bgCtx := context.Background()
			// A panic in a buggy handler must not take down crewshipd.
			// Recover, surface the panic in the journal as a warn-level
			// hook.fired entry, and let the dispatcher live on.
			defer func() {
				r := recover()
				if r == nil {
					return
				}
				panicErr := fmt.Errorf("hook handler panic: %v", r)
				emitFired(bgCtx, emitter, h, ec, Result{
					Outcome: OutcomeError,
					Message: panicErr.Error(),
				}, panicErr)
			}()
			res, runErr := runHandler(bgCtx, h, ec)
			emitFired(bgCtx, emitter, h, ec, res, runErr)
			if runErr == nil && res.Outcome == OutcomeBlock {
				// Non-blocking Block still lands in the journal so
				// operators can see what would have blocked.
				emitBlocked(bgCtx, emitter, h, ec, res)
			}
		}()
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// runHandler dispatches to the correct backend based on HandlerKind.
// Centralized here so the dispatcher logic doesn't have to know anything
// about the individual handlers.
func runHandler(ctx context.Context, h Hook, ec EventContext) (Result, error) {
	switch h.HandlerKind {
	case HandlerKindShell:
		return shellHandler(ctx, h, ec)
	case HandlerKindHTTP:
		return httpHandler(ctx, h, ec)
	case HandlerKindSubagent:
		return subagentHandlerDispatch(ctx, h, ec)
	default:
		return Result{
			Outcome: OutcomeError,
			Message: "unknown handler kind: " + string(h.HandlerKind),
		}, ErrUnknownHandlerKind
	}
}

// emitFired writes a hook.fired entry into the journal. This is the
// "something happened" record — every dispatched hook lands one regardless
// of outcome. Severity escalates to warn on non-pass results.
func emitFired(ctx context.Context, emitter journal.Emitter, h Hook, ec EventContext, res Result, runErr error) {
	if emitter == nil {
		return
	}
	sev := journal.SeverityInfo
	if res.Outcome != OutcomePass || runErr != nil {
		sev = journal.SeverityWarn
	}
	payload := map[string]any{
		"hook_id":      h.ID,
		"event":        string(ec.Event),
		"handler_kind": string(h.HandlerKind),
		"outcome":      string(res.Outcome),
		"message":      res.Message,
		"latency_ms":   res.Latency.Milliseconds(),
		"blocking":     h.Blocking,
	}
	if res.Payload != nil {
		payload["handler_payload"] = res.Payload
	}
	if runErr != nil {
		payload["error"] = runErr.Error()
	}
	_, _ = emitter.Emit(ctx, journal.Entry{
		WorkspaceID: ec.WorkspaceID,
		CrewID:      ec.CrewID,
		AgentID:     ec.AgentID,
		MissionID:   ec.MissionID,
		Type:        journal.EntryHookFired,
		Severity:    sev,
		ActorType:   journal.ActorSystem,
		ActorID:     h.ID,
		Summary:     summaryFired(h, ec, res),
		Payload:     payload,
	})
}

// emitBlocked writes the hook.blocked follow-up. Separate from hook.fired
// so UI filters ("show me blocks") don't have to parse payloads — they can
// just query entry_type.
func emitBlocked(ctx context.Context, emitter journal.Emitter, h Hook, ec EventContext, res Result) {
	if emitter == nil {
		return
	}
	_, _ = emitter.Emit(ctx, journal.Entry{
		WorkspaceID: ec.WorkspaceID,
		CrewID:      ec.CrewID,
		AgentID:     ec.AgentID,
		MissionID:   ec.MissionID,
		Type:        journal.EntryHookBlocked,
		Severity:    journal.SeverityWarn,
		ActorType:   journal.ActorSystem,
		ActorID:     h.ID,
		Summary:     fmt.Sprintf("hook %s blocked %s", h.ID, ec.Event),
		Payload: map[string]any{
			"hook_id":      h.ID,
			"event":        string(ec.Event),
			"handler_kind": string(h.HandlerKind),
			"message":      res.Message,
			"blocking":     h.Blocking,
		},
	})
}

func summaryFired(h Hook, ec EventContext, res Result) string {
	return fmt.Sprintf("hook %s (%s) fired on %s → %s",
		h.ID, h.HandlerKind, ec.Event, res.Outcome)
}
