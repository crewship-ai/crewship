package skills

import (
	"strings"
	"testing"
	"time"
)

func TestValidateLifecycleState(t *testing.T) {
	for _, s := range []LifecycleState{
		LifecycleActive, LifecycleStale, LifecycleArchived, LifecycleDeprecated,
	} {
		if err := ValidateLifecycleState(s); err != nil {
			t.Errorf("valid state %q rejected: %v", s, err)
		}
	}
	for _, s := range []LifecycleState{"", "yolo", "ACTIVE"} {
		if err := ValidateLifecycleState(s); err == nil {
			t.Errorf("invalid state %q accepted", s)
		}
	}
}

func TestEvaluateTransition(t *testing.T) {
	now := time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour

	tests := []struct {
		name           string
		snap           LifecycleSnapshot
		wantNext       LifecycleState
		wantReasonHint string
	}{
		// assignment-trumps-timer
		{
			name:     "active stays active with assignment + recent use",
			snap:     LifecycleSnapshot{Current: LifecycleActive, LastUsedAt: now.Add(-2 * day), ActiveAssignments: 1, Now: now},
			wantNext: LifecycleActive,
		},
		{
			name:     "active stays active with assignment even if unused 60d",
			snap:     LifecycleSnapshot{Current: LifecycleActive, LastUsedAt: now.Add(-60 * day), ActiveAssignments: 1, Now: now},
			wantNext: LifecycleActive,
		},
		{
			name:           "stale flips back to active when assignment appears",
			snap:           LifecycleSnapshot{Current: LifecycleStale, LastUsedAt: now.Add(-45 * day), ActiveAssignments: 2, Now: now},
			wantNext:       LifecycleActive,
			wantReasonHint: "assignment trumps",
		},

		// active → stale
		{
			name:           "active → stale at exactly 30d unused, no assignments",
			snap:           LifecycleSnapshot{Current: LifecycleActive, LastUsedAt: now.Add(-30 * day), Now: now},
			wantNext:       LifecycleStale,
			wantReasonHint: "stale",
		},
		{
			name:     "active stays active at 29d unused",
			snap:     LifecycleSnapshot{Current: LifecycleActive, LastUsedAt: now.Add(-29 * day), Now: now},
			wantNext: LifecycleActive,
		},
		{
			name:           "active → stale when never used",
			snap:           LifecycleSnapshot{Current: LifecycleActive, Now: now},
			wantNext:       LifecycleStale,
			wantReasonHint: "never used",
		},

		// stale → archived
		{
			name:           "stale → archived at 90d unused",
			snap:           LifecycleSnapshot{Current: LifecycleStale, LastUsedAt: now.Add(-90 * day), Now: now},
			wantNext:       LifecycleArchived,
			wantReasonHint: "archived",
		},
		{
			name:     "stale stays stale at 89d unused",
			snap:     LifecycleSnapshot{Current: LifecycleStale, LastUsedAt: now.Add(-89 * day), Now: now},
			wantNext: LifecycleStale,
		},

		// archived stays archived (never auto-recovers)
		{
			name:     "archived stays archived even with recent use",
			snap:     LifecycleSnapshot{Current: LifecycleArchived, LastUsedAt: now.Add(-1 * day), Now: now},
			wantNext: LifecycleArchived,
		},

		// deprecated is terminal
		{
			name:     "deprecated stays deprecated with assignments + use",
			snap:     LifecycleSnapshot{Current: LifecycleDeprecated, LastUsedAt: now, ActiveAssignments: 5, Now: now},
			wantNext: LifecycleDeprecated,
		},

		// unknown state defaults to active
		{
			name:     "unknown current state defaults to active",
			snap:     LifecycleSnapshot{Current: LifecycleState("mystery"), Now: now},
			wantNext: LifecycleActive,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := EvaluateTransition(tc.snap)
			if got.Next != tc.wantNext {
				t.Errorf("Next = %q, want %q (reason=%q)", got.Next, tc.wantNext, got.Reason)
			}
			if tc.wantReasonHint != "" && !strings.Contains(got.Reason, tc.wantReasonHint) {
				t.Errorf("Reason %q missing hint %q", got.Reason, tc.wantReasonHint)
			}
		})
	}
}
