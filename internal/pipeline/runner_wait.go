package pipeline

import (
	"context"
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
				WorkspaceID:    in.WorkspaceID,
				PipelineRunID:  runID,
				StepID:         step.ID,
				Prompt:         prompt,
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
			// Park on a fresh run; on a resume, RE-PARK only while the waitpoint
			// is still pending (#1428, 2.9). A boot/approval-resumed run re-
			// acquires a concurrency slot, and blocking on WaitFor below would
			// hold that slot for up to the 24h approval timeout. Re-parking
			// returns the suspend sentinel so runDSL releases the slot again. A
			// DECIDED waitpoint (approved/denied/timed_out) falls through to
			// WaitFor to resolve immediately from the recorded decision.
			park := !in.resume
			if in.resume {
				if reader, ok := e.waitpoints.(WaitpointStatusReader); ok {
					if st, serr := reader.WaitpointStatus(ctx, token); serr == nil && st == "pending" {
						park = true
					}
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
		return "waited:approval:approved", 0, time.Since(stepStart).Milliseconds(), nil

	case "event":
		// Input-stream injection (Wave 4.3): wait for an external caller
		// to deliver a signal for this (run, event_type) via
		// POST /pipeline-runs/{id}/signal. The payload becomes the step
		// output (so downstream steps read {{ steps.<id>.output }}).
		//
		// Durability (#1409): a signal delivered while this run's
		// goroutine isn't live to receive it — process restart, or the
		// delivery racing ahead of this step even registering — used to
		// be lost forever (in-memory-only SignalRegistry). Now, when a
		// SignalWaitStore is wired, the wait durably ARMs before it does
		// anything else blocking, and checks for an already-delivered
		// payload FIRST (covers both a resume finding a signal that
		// arrived during downtime, and the ordinary race where the
		// signal endpoint's Deliver committed a hair before Register
		// below would have caught it).
		eventType := Render(step.Wait.EventType, parentRender)
		// (ModeDryRun is short-circuited for every wait kind at the top.)
		if e.signals == nil {
			return "", 0, time.Since(stepStart).Milliseconds(),
				fmt.Errorf("wait step %q (event) no signal registry wired", step.ID)
		}
		if e.signalWaits != nil {
			if !in.resume {
				if err := e.signalWaits.Arm(ctx, in.WorkspaceID, runID, step.ID, eventType); err != nil {
					return "", 0, time.Since(stepStart).Milliseconds(),
						fmt.Errorf("wait step %q (event) arm: %w", step.ID, err)
				}
			}
			if payload, ok, cerr := e.signalWaits.ConsumeDelivered(ctx, runID, step.ID); cerr != nil {
				return "", 0, time.Since(stepStart).Milliseconds(),
					fmt.Errorf("wait step %q (event) check delivered: %w", step.ID, cerr)
			} else if ok {
				return payload, 0, time.Since(stepStart).Milliseconds(), nil
			}
		}

		// Async suspend: mirrors wait(approval)'s park (runner_wait.go
		// case "approval" above) — a top-level foreground run with a
		// persisted row parks (MarkWaiting + suspendError) instead of
		// blocking the goroutine, so a process restart re-enters via the
		// normal resume path (ResumeInterruptedRuns / ResumeAfterSignal)
		// rather than needing THIS goroutine to still be alive when the
		// signal lands. Nested/test-only/no-store callers keep the
		// blocking behaviour below (they have no row to resume and
		// nobody to return WAITING to).
		if e.signalWaits != nil && depth == 0 && in.Mode == ModeRun && !in.resume && e.runStore != nil && in.pipeline != nil {
			if err := e.runStore.MarkWaiting(ctx, runID, step.ID); err != nil {
				return "", 0, time.Since(stepStart).Milliseconds(),
					fmt.Errorf("wait step %q (event) mark waiting: %w", step.ID, err)
			}
			return "", 0, time.Since(stepStart).Milliseconds(), &suspendError{stepID: step.ID}
		}

		ch, cancel := e.signals.Register(runID, eventType)
		defer cancel()
		// Honor the step timeout (default 1h) so an event that never
		// arrives doesn't hang the run forever.
		timeout := time.Duration(step.TimeoutSec) * time.Second
		if timeout <= 0 {
			timeout = time.Hour
		}
		select {
		case payload := <-ch:
			return payload, 0, time.Since(stepStart).Milliseconds(), nil
		case <-time.After(timeout):
			return "", 0, time.Since(stepStart).Milliseconds(),
				fmt.Errorf("wait step %q (event %q) timed out after %s", step.ID, eventType, timeout)
		case <-ctx.Done():
			return "", 0, time.Since(stepStart).Milliseconds(), ctx.Err()
		}
	}

	return "", 0, time.Since(stepStart).Milliseconds(),
		fmt.Errorf("wait step %q unknown kind %q", step.ID, step.Wait.Kind)
}
