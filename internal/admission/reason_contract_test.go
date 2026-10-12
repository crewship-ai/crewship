package admission

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCapacityReasonsGiveSpecificOperatorGuidance(t *testing.T) {
	for _, tc := range []struct{ reason, phrase, remedy string }{
		{ReasonHostMemory, "free memory", "runtime.host_memory_reserve_mb"},
		{ReasonHostPressure, "stalling on memory", "runtime.host_memory_pressure_pct"},
		{ReasonConcurrency, "starting on this host", "runtime.max_concurrent_container_starts"},
		{ReasonPacing, "staggered", ""}, {"future-reason", "cannot afford", ""},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			if got := ReasonPhrase(tc.reason); !strings.Contains(got, tc.phrase) {
				t.Fatalf("unhelpful phrase %q", got)
			}
			got := ReasonRemedy(tc.reason)
			if tc.remedy == "" && got != "" || tc.remedy != "" && !strings.Contains(got, tc.remedy) {
				t.Fatalf("remedy=%q want %q", got, tc.remedy)
			}
		})
	}
}

func TestHoldExpiredErrorPreservesClassificationAndContext(t *testing.T) {
	err := &HoldExpiredError{Reason: ReasonHostMemory, Detail: "10 MiB available, 20 MiB needed", Waited: 1500 * time.Millisecond, Err: context.DeadlineExceeded}
	if !errors.Is(err, ErrHeldForCapacity) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("hold lost sentinel or deadline cause")
	}
	for _, want := range []string{"1.5s", "host_memory", "10 MiB available, 20 MiB needed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %s", want, err)
		}
	}
	err.Err = nil
	if !errors.Is(err, ErrHeldForCapacity) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("nil cause changed sentinel classification")
	}
}
