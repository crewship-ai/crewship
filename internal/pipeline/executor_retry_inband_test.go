package pipeline

import (
	"errors"
	"fmt"
	"testing"

	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// isTransientRunnerError classifies by substring over the whole error string
// (transientErrorMarkers), and an in-band failure error quotes the agent CLI's
// own message. Those two facts collide: a deterministic refusal whose text
// happens to contain "500" or "timeout" would be retried on the same tier,
// repeating a failure that cannot succeed and billing for every attempt.
//
// The classifier must therefore reject in-band failures on IDENTITY
// (errors.Is on the sentinel), before any prose is consulted.
func TestIsTransientRunnerError_InBandFailureNeverTransient(t *testing.T) {
	// Every message here contains at least one transientErrorMarker substring,
	// so each row fails if the identity check is missing or ordered after the
	// marker loop.
	cases := []struct {
		name string
		msg  string
	}{
		{"contains 500", `agent reported a failed run (error): I cannot process a list of 500 items`},
		{"contains timeout", `agent reported a failed run: that request would timeout, so I stopped`},
		{"contains rate limit", `agent reported a failed run: the user asked me to explain a rate limit`},
		{"contains eof", `agent reported a failed run: the file ends with an unexpected eof marker`},
		{"contains 503", `agent reported a failed run (turn.failed): refusing to fake a 503 response`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Self-proving guard: the SAME prose without the in-band identity
			// IS classified transient. If this ever stops holding, the row has
			// gone stale — it is no longer exercising the marker collision it
			// was written to catch, and the assertions below would pass for the
			// wrong reason.
			if !isTransientRunnerError(errors.New(tc.msg)) {
				t.Fatalf("stale case: %q no longer matches any transient marker, so this row proves nothing", tc.msg)
			}

			err := orchestrator.NewInBandFailureError(tc.msg)
			if isTransientRunnerError(err) {
				t.Errorf("in-band failure classified transient: %q — a deterministic agent failure would be retried and billed per attempt", tc.msg)
			}
			// The real call path wraps on the way up
			// (runner_orchestrator.go: "orchestrator: %w", then the executor's
			// own wraps), so the identity must survive wrapping.
			wrapped := fmt.Errorf("step foo: %w", fmt.Errorf("orchestrator: %w", err))
			if isTransientRunnerError(wrapped) {
				t.Errorf("wrapped in-band failure classified transient: %v", wrapped)
			}
			if !errors.Is(wrapped, orchestrator.ErrAgentInBandFailure) {
				t.Error("wrapped in-band failure lost its sentinel identity")
			}
		})
	}
}

// The control: excluding in-band failures must not blunt the classifier for the
// transport faults it exists to catch. A genuine upstream 5xx / rate limit /
// truncated stream is still worth a same-tier retry.
func TestIsTransientRunnerError_TransportFaultsStillTransient(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"upstream 500", errors.New("orchestrator: upstream returned 500 internal server error")},
		{"rate limit", errors.New("orchestrator: 429 rate limit exceeded")},
		{"unexpected eof", errors.New("orchestrator: unexpected eof reading stream")},
		{"gateway timeout", errors.New("orchestrator: 504 gateway timeout")},
		{"conn reset", fmt.Errorf("step foo: %w", errors.New("connection reset by peer"))},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !isTransientRunnerError(tc.err) {
				t.Errorf("transport fault %v classified NOT transient — same-tier retry lost", tc.err)
			}
		})
	}
}
