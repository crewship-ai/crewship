package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPendingReasonClassificationPersists(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		reason string
	}{
		{"capacity", ErrConcurrencyLimitReached, "routine_capacity"},
		{"interrupted", context.Canceled, "retry_backoff"},
		{"missing", ErrPinnedVersionNotFound, "version_missing"},
		{"inactive", ErrRoutineNotActive, "routine_inactive"},
		{"failure", errors.New("private diagnostic"), "dispatch_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := enqueueDue(t, 1)
			d := NewPendingRunDispatcher(s, rejectedPendingExecutor{tc.err}, nil)
			d.fireOne(t.Context(), PendingRun{ID: "pa"})
			got, err := s.Get(t.Context(), "w", "pa")
			if err != nil {
				t.Fatal(err)
			}
			if got == nil {
				t.Fatal("missing receipt")
			}
			if got.ReasonCode != tc.reason {
				t.Fatalf("reason=%q want %q", got.ReasonCode, tc.reason)
			}
		})
	}
}

func TestPendingExpiryDoesNotInventCapacity(t *testing.T) {
	for _, code := range []string{"unknown", "routine_capacity"} {
		t.Run(code, func(t *testing.T) {
			s := NewPendingRunStore(newPendingDB(t))
			past := time.Now().Add(-time.Minute)
			if _, _, err := s.Enqueue(t.Context(), PendingRun{ID: "expired", WorkspaceID: "w", PipelineID: "p", PipelineSlug: "p", FireAt: past, ExpiresAt: &past, ReasonCode: code}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ExpireDue(t.Context(), time.Now()); err != nil {
				t.Fatal(err)
			}
			row, err := s.Get(t.Context(), "w", "expired")
			if err != nil {
				t.Fatal(err)
			}
			want := "expired"
			if code == "routine_capacity" {
				want = "capacity_expired"
			}
			if row.ReasonCode != want {
				t.Fatalf("got %+v", row)
			}
		})
	}
}
