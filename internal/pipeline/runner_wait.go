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
func (e *Executor) runWaitStep(ctx context.Context, step Step, parentRender RenderContext, in RunInput, runID string) (string, float64, int64, error) {
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
		// Phase 2: subscribe to journal filter. For MVP we surface
		// a clear "not yet implemented" so a pipeline that uses
		// event waits fails loudly instead of silently hanging.
		return "", 0, time.Since(stepStart).Milliseconds(),
			fmt.Errorf("wait step %q (event) not yet implemented", step.ID)
	}

	return "", 0, time.Since(stepStart).Milliseconds(),
		fmt.Errorf("wait step %q unknown kind %q", step.ID, step.Wait.Kind)
}
