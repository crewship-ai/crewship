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

		// Async suspend: for a top-level foreground run (depth==0, ModeRun,
		// not a resume) with a persisted run row, PARK instead of blocking.
		// Mark the run waiting (status=waiting + current_step) and return the
		// suspend sentinel; runDSL/runDAG turn it into a WAITING RunResult and
		// release the slot. Approving the waitpoint calls ResumeAfterApproval,
		// which re-enters with in.resume=true — that path falls through to
		// WaitFor below and resolves immediately from the recorded decision.
		// Non-top-level / test_run / no-store callers keep the blocking
		// behaviour (they have no row to resume and nobody to return WAITING to).
		if depth == 0 && in.Mode == ModeRun && !in.resume && e.runStore != nil && in.pipeline != nil {
			if err := e.runStore.MarkWaiting(ctx, runID, step.ID); err != nil {
				return "", 0, time.Since(stepStart).Milliseconds(), fmt.Errorf("wait step %q mark waiting: %w", step.ID, err)
			}
			return "", 0, time.Since(stepStart).Milliseconds(), &suspendError{token: token, stepID: step.ID}
		}

		approved, err := e.waitpoints.WaitFor(ctx, token)
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
		// Input-stream injection (Wave 4.3): block until an external
		// caller delivers a signal for this (run, event_type) via
		// POST /pipeline-runs/{id}/signal. The payload becomes the step
		// output (so downstream steps read {{ steps.<id>.output }}).
		// Blocking + in-memory (like wait:datetime) — steers a live run.
		eventType := Render(step.Wait.EventType, parentRender)
		// test_run / dry_run preview: don't block waiting for a signal
		// that the save gate can't deliver — short-circuit so a routine
		// with a wait:event step is saveable + previewable.
		if in.Mode == ModeTestRun || in.Mode == ModeDryRun {
			return "waited:event:preview", 0, time.Since(stepStart).Milliseconds(), nil
		}
		if e.signals == nil {
			return "", 0, time.Since(stepStart).Milliseconds(),
				fmt.Errorf("wait step %q (event) no signal registry wired", step.ID)
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
