package pipeline

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// runWaitStep handles StepWait. Three flavours:
//
//   - approval: mint a token, park until /pipelines/waitpoints/{token}/approve
//     fires (Phase 2 — for now stub to a 60s no-op when WaitpointStore
//     is missing, so the executor can run end-to-end while the inbox
//     UI lands)
//   - datetime: parse the Until field as RFC3339 and sleep until then
//   - event: subscribe to a journal event filter (Phase 2)
//
// Wait steps don't produce a meaningful output by themselves; they
// return a stable "waited:<reason>" string so downstream templates
// can detect a waited-step transition. If approvals carry data
// (e.g. user comment), Phase 2 plumbs it through.
func (e *Executor) runWaitStep(ctx context.Context, step Step, parentRender RenderContext, in RunInput, runID string, depth int) (string, float64, int64, error) {
	stepStart := time.Now()
	if step.Wait == nil {
		return "", 0, 0, fmt.Errorf("wait step %q missing body", step.ID)
	}

	// dry_run preview: short-circuit EVERY wait kind before it can block or
	// cause side effects — datetime would sleep, approval would
	// CreateApproval/WaitFor (inbox card + DB row), event would block on a
	// signal the preview can't deliver. The save-gate's draft dry-run must
	// validate the routine statically, so return a deterministic preview marker.
	if in.Mode == ModeDryRun {
		switch step.Wait.Kind {
		case "datetime", "approval", "event":
			return "waited:" + step.Wait.Kind + ":preview", 0, time.Since(stepStart).Milliseconds(), nil
		}
	}

	switch step.Wait.Kind {
	case "datetime":
		// Render template (allows {{ inputs.deadline }} etc.) before
		// parsing — authors can pass a date dynamically.
		untilRaw := Render(step.Wait.Until, parentRender)
		untilT, err := time.Parse(time.RFC3339, untilRaw)
		if err != nil {
			// Try plain RFC3339 without nano fraction
			untilT, err = time.Parse(time.RFC3339Nano, untilRaw)
		}
		if err != nil {
			return "", 0, 0, fmt.Errorf("wait step %q parse until %q: %w", step.ID, untilRaw, err)
		}
		delay := time.Until(untilT)
		if delay <= 0 {
			// Already past — return immediately, this is fine
			return "waited:datetime:past", 0, time.Since(stepStart).Milliseconds(), nil
		}
		select {
		case <-time.After(delay):
			return "waited:datetime", 0, time.Since(stepStart).Milliseconds(), nil
		case <-ctx.Done():
			return "", 0, time.Since(stepStart).Milliseconds(), ctx.Err()
		}

	case "approval":
		prompt := Render(step.Wait.ApprovalPrompt, parentRender)
		// Rendered with the same context as the prompt, so
		// `Approve: {{ inputs.action }}` names THIS run's action rather
		// than the routine's boilerplate. Empty stays empty; the store
		// falls back to the prompt's first line.
		title := Render(step.Wait.ApprovalTitle, parentRender)
		if e.waitpoints == nil {
			// No store wired — production should always have one.
			// For dev/tests we time-out at 60s with a clear marker
			// so end-to-end tests don't hang forever.
			select {
			case <-time.After(60 * time.Second):
				return "", 0, time.Since(stepStart).Milliseconds(),
					fmt.Errorf("wait step %q (approval) no WaitpointStore wired and no approval received", step.ID)
			case <-ctx.Done():
				return "", 0, time.Since(stepStart).Milliseconds(), ctx.Err()
			}
		}
		// Boot-time resume: re-attach to the waitpoint the previous
		// lifetime created for this (run, step) instead of minting a
		// duplicate approval (second token + second inbox card).
		// WaitFor handles both live and already-decided tokens — if
		// the approval was resolved between the kill and the resume,
		// the DB re-check inside WaitFor returns immediately.
		var token string
		if in.resume {
			if finder, ok := e.waitpoints.(WaitpointResumer); ok {
				existing, ferr := finder.FindApprovalForStep(ctx, runID, step.ID)
				if ferr != nil {
					return "", 0, time.Since(stepStart).Milliseconds(), fmt.Errorf("wait step %q find resumable approval: %w", step.ID, ferr)
				}
				token = existing
			}
		}
		if token == "" {
			created, err := e.waitpoints.CreateApproval(ctx, WaitpointApprovalRequest{
				DecisionForm:   step.Wait.DecisionForm,
				WorkspaceID:    in.WorkspaceID,
				PipelineRunID:  runID,
				StepID:         step.ID,
				Prompt:         prompt,
				Title:          title,
				RiskLevel:      step.Wait.RiskLevel,
				InvokingCrewID: in.InvokingCrewID,
				TimeoutSec:     step.TimeoutSec,
			})
			if err != nil {
				return "", 0, time.Since(stepStart).Milliseconds(), fmt.Errorf("wait step %q create approval: %w", step.ID, err)
			}
			token = created
		}

		// Async suspend: for a top-level foreground run (depth==0, ModeRun)
		// with a persisted run row, PARK instead of blocking. Mark the run
		// waiting (status=waiting + current_step) and return the suspend
		// sentinel; runDSL/runDAG turn it into a WAITING RunResult and release
		// the slot. Non-top-level / test_run / no-store callers keep the
		// blocking behaviour (they have no row to resume and nobody to return
		// WAITING to).
		if depth == 0 && in.Mode == ModeRun && e.runStore != nil && in.pipeline != nil {
			// Park only while the waitpoint is actually PENDING (#1428, 2.9).
			// A boot/approval-resumed run re-acquires a concurrency slot, and
			// blocking on WaitFor below would hold that slot for up to the 24h
			// approval timeout. Re-parking returns the suspend sentinel so
			// runDSL releases the slot again. A DECIDED waitpoint
			// (approved/denied/timed_out) falls through to WaitFor to resolve
			// immediately from the recorded decision.
			//
			// The status check covers a FRESH run too, not just a resume. It
			// used to be resume-only, on the assumption that a run which just
			// minted its own waitpoint could only ever find it pending — true
			// until standing approval grants, which resolve the waitpoint
			// inside CreateApproval. Parking on one of those strands the run:
			// there is no pending row for anyone to approve, and the timeout
			// sweeper only visits pending rows, so nothing ever wakes it. A
			// trusted gate that parks is strictly worse than one that asks.
			park := true
			if reader, ok := e.waitpoints.(WaitpointStatusReader); ok {
				if st, serr := reader.WaitpointStatus(ctx, token); serr == nil && st != "pending" {
					park = false
				}
			}
			if park {
				// MarkWaiting flips the row back to 'waiting' (the step-entry
				// projection stamped it 'running'); it is idempotent for a row
				// already parked.
				if err := e.runStore.MarkWaiting(ctx, runID, step.ID); err != nil {
					return "", 0, time.Since(stepStart).Milliseconds(), fmt.Errorf("wait step %q mark waiting: %w", step.ID, err)
				}
				return "", 0, time.Since(stepStart).Milliseconds(), &suspendError{token: token, stepID: step.ID}
			}
		}

		approved, err := e.waitpoints.WaitFor(ctx, token)
		// Blocking run died mid-wait (#1426, 3.2): the run's ctx was cancelled
		// rather than the waitpoint resolving. Flip the waitpoint to cancelled
		// so its inbox approval card stops being actionable (approving a
		// waitpoint whose run is gone resolves nothing). Detached context —
		// ctx is already cancelled, so a store call keyed on it would fail.
		if ctx.Err() != nil {
			if wc, ok := e.waitpoints.(WaitpointCanceller); ok {
				cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_, _ = wc.CancelWaitpointsForRun(cctx, runID)
				cancel()
			}
			return "", 0, time.Since(stepStart).Milliseconds(), ctx.Err()
		}
		if err != nil {
			return "", 0, time.Since(stepStart).Milliseconds(), fmt.Errorf("wait step %q wait: %w", step.ID, err)
		}
		if !approved {
			// WaitFor collapses denial / timeout / cancellation into
			// approved=false. Re-read the terminal status (committed
			// to the DB before the channel signal in every path) so a
			// waitpoint that expired — e.g. during downtime, before a
			// boot-time resume re-attached — is reported as what it
			// is, not as a human "denied".
			if reader, ok := e.waitpoints.(WaitpointStatusReader); ok {
				switch st, serr := reader.WaitpointStatus(ctx, token); {
				case serr == nil && st == "timed_out":
					return "", 0, time.Since(stepStart).Milliseconds(), fmt.Errorf("wait step %q (approval) timed out", step.ID)
				case serr == nil && st == "cancelled":
					return "", 0, time.Since(stepStart).Milliseconds(), fmt.Errorf("wait step %q (approval) cancelled", step.ID)
				}
			}
			return "", 0, time.Since(stepStart).Milliseconds(), fmt.Errorf("wait step %q (approval) denied", step.ID)
		}
		// Binary approvals already carry their complete result in WaitFor.
		// A second database read must not turn a confirmed approval into failure.
		if step.Wait.DecisionForm == nil {
			return "waited:approval:approved", 0, time.Since(stepStart).Milliseconds(), nil
		}
		if reader, ok := e.waitpoints.(interface {
			ApprovalOutput(context.Context, string, string) (string, error)
		}); ok {
			output, err := readApprovedDecision(ctx, reader, in.WorkspaceID, token)
			return output, 0, time.Since(stepStart).Milliseconds(), err
		}
		return "", 0, time.Since(stepStart).Milliseconds(), fmt.Errorf("waitpoint store does not support decision forms")

	case "event":
		eventType := Render(step.Wait.EventType, parentRender)
		if e.signals == nil {
			return "", 0, time.Since(stepStart).Milliseconds(), fmt.Errorf("wait step %q (event) no signal registry wired", step.ID)
		}
		timeout := time.Duration(step.TimeoutSec) * time.Second
		if timeout <= 0 {
			timeout = time.Hour
		}
		deadline := stepStart.Add(timeout)
		timeoutError := func() (string, float64, int64, error) {
			return "", 0, time.Since(stepStart).Milliseconds(), fmt.Errorf("wait step %q (event %q) timed out after %s", step.ID, eventType, timeout)
		}
		// SQL is the arbiter: delivery must commit before the original deadline;
		// a delivered row takes precedence even when consumed after downtime.
		check := func() (string, bool, error) {
			if e.signalWaits == nil {
				return "", false, nil
			}
			for {
				if payload, ok, err := e.signalWaits.ConsumeDelivered(ctx, runID, step.ID); err != nil || ok {
					return payload, ok, err
				}
				status, err := e.signalWaits.Resolve(ctx, runID, step.ID)
				if err != nil {
					return "", false, err
				}
				switch status {
				case "delivered":
					continue // delivery raced our first consume
				case "timed_out":
					return "", false, fmt.Errorf("wait step %q (event %q) timed out after %s", step.ID, eventType, timeout)
				case "cancelled":
					return "", false, context.Canceled
				case "consumed":
					if in.resume && in.resumeCurrentStepID == step.ID {
						payload, err := e.signalWaits.ReplayConsumed(ctx, runID, step.ID)
						return payload, err == nil, err
					}
					return "", false, fmt.Errorf("wait step %q (event) already consumed", step.ID)
				default:
					return "", false, nil
				}
			}
		}
		if e.signalWaits != nil {
			var err error
			deadline, err = e.signalWaits.ArmWithTimeout(ctx, in.WorkspaceID, runID, step.ID, eventType, timeout)
			if err != nil {
				return "", 0, time.Since(stepStart).Milliseconds(), fmt.Errorf("wait step %q (event) arm: %w", step.ID, err)
			}
			if payload, ok, err := check(); err != nil || ok {
				return payload, 0, time.Since(stepStart).Milliseconds(), err
			}
		}
		// Pending resumed waits re-park as well. Neither retries nor restart
		// renew the deadline, and no idle wait owns a goroutine or registry slot.
		if e.signalWaits != nil && depth == 0 && in.Mode == ModeRun && e.runStore != nil && in.pipeline != nil {
			if err := e.runStore.MarkWaiting(ctx, runID, step.ID); err != nil {
				return "", 0, time.Since(stepStart).Milliseconds(), fmt.Errorf("wait step %q (event) mark waiting: %w", step.ID, err)
			}
			return "", 0, time.Since(stepStart).Milliseconds(), &suspendError{stepID: step.ID}
		}
		ch, cancel := e.signals.Register(runID, eventType)
		defer cancel()
		// Close delivery-before-registration for blocking/nested callers too.
		if payload, ok, err := check(); err != nil || ok {
			return payload, 0, time.Since(stepStart).Milliseconds(), err
		}
		timer := time.NewTimer(time.Until(deadline))
		defer timer.Stop()
		select {
		case payload := <-ch:
			if e.signalWaits != nil {
				if output, ok, err := check(); err != nil || ok {
					return output, 0, time.Since(stepStart).Milliseconds(), err
				}
				// The in-memory wake is advisory when a durable store is wired.
				return timeoutError()
			}
			return payload, 0, time.Since(stepStart).Milliseconds(), nil
		case <-timer.C:
			if payload, ok, err := check(); err != nil || ok {
				return payload, 0, time.Since(stepStart).Milliseconds(), err
			}
			return timeoutError()
		case <-ctx.Done():
			return "", 0, time.Since(stepStart).Milliseconds(), ctx.Err()
		}

	}

	return "", 0, time.Since(stepStart).Milliseconds(),
		fmt.Errorf("wait step %q unknown kind %q", step.ID, step.Wait.Kind)
}

// The answer was committed before WaitFor signalled approval. Retry an
// intermittent read failure without repeating the human action. A missing or
// foreign answer remains an integrity error; never fabricate form data.
func readApprovedDecision(ctx context.Context, reader interface {
	ApprovalOutput(context.Context, string, string) (string, error)
}, workspaceID, token string) (string, error) {
	for attempt := 0; ; attempt++ {
		output, err := reader.ApprovalOutput(ctx, workspaceID, token)
		if err == nil || errors.Is(err, ErrAlreadyDecided) || errors.Is(err, ErrDecisionInput) || attempt == 2 {
			return output, err
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
	}
}
