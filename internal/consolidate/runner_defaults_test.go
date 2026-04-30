package consolidate

import (
	"testing"
	"time"
)

// TestApplyDefaults_AllZeroValues — empty RunnerOptions must produce a
// fully-functional default config rather than panic at runtime.
func TestApplyDefaults_AllZeroValues(t *testing.T) {
	got := applyDefaults(RunnerOptions{})
	if got.ConsolidationInterval != 6*time.Hour {
		t.Errorf("ConsolidationInterval = %v, want 6h", got.ConsolidationInterval)
	}
	if got.ConsolidationSince != got.ConsolidationInterval {
		t.Errorf("ConsolidationSince should default to interval, got %v", got.ConsolidationSince)
	}
	// CompactionHourUTC zero is in-range (0–23 valid), so applyDefaults
	// leaves it alone. Out-of-range values get clamped to 3 (covered by
	// TestApplyDefaults_ClampsCompactionHour).
	if got.CompactionHourUTC != 0 {
		t.Errorf("CompactionHourUTC = %d, want 0 (in-range zero preserved)", got.CompactionHourUTC)
	}
	if got.CompactionOlderThan != 30*24*time.Hour {
		t.Errorf("CompactionOlderThan = %v, want 30 days", got.CompactionOlderThan)
	}
	if got.MinEntries != 10 {
		t.Errorf("MinEntries = %d, want 10", got.MinEntries)
	}
	if got.CrewMemoryRoot != "/crew/shared/.memory" {
		t.Errorf("CrewMemoryRoot = %q, want default path", got.CrewMemoryRoot)
	}
	if got.Logger == nil {
		t.Error("Logger should default to slog.Default")
	}
}

// TestApplyDefaults_PreservesExplicitValues — when caller sets a value
// it must survive the defaults pass.
func TestApplyDefaults_PreservesExplicitValues(t *testing.T) {
	in := RunnerOptions{
		ConsolidationInterval: 1 * time.Hour,
		ConsolidationSince:    2 * time.Hour,
		CompactionHourUTC:     7,
		CompactionOlderThan:   60 * 24 * time.Hour,
		MinEntries:            100,
		CrewMemoryRoot:        "/custom/path",
	}
	got := applyDefaults(in)
	if got.ConsolidationInterval != in.ConsolidationInterval {
		t.Errorf("ConsolidationInterval overwritten: %v", got.ConsolidationInterval)
	}
	if got.ConsolidationSince != in.ConsolidationSince {
		t.Errorf("ConsolidationSince overwritten: %v", got.ConsolidationSince)
	}
	if got.CompactionOlderThan != in.CompactionOlderThan {
		t.Errorf("CompactionOlderThan overwritten: %v", got.CompactionOlderThan)
	}
	if got.MinEntries != 100 {
		t.Errorf("MinEntries overwritten: %d", got.MinEntries)
	}
	if got.CompactionHourUTC != 7 {
		t.Errorf("CompactionHourUTC overwritten: %d", got.CompactionHourUTC)
	}
	if got.CrewMemoryRoot != in.CrewMemoryRoot {
		t.Errorf("CrewMemoryRoot overwritten: %q", got.CrewMemoryRoot)
	}
}

// TestApplyDefaults_ClampsCompactionHour pins the exact behaviour for
// every hour value the runner might see. Implementation predicate is
// `if opts.CompactionHourUTC < 0 || opts.CompactionHourUTC > 23 { = 3 }`,
// so the zero value (0) is in-range and survives — caller asking for
// midnight UTC compaction gets midnight, NOT 3am.
func TestApplyDefaults_ClampsCompactionHour(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{"negative clamps to 3", -1, 3},
		{"24 clamps to 3", 24, 3},
		{"99 clamps to 3", 99, 3},
		{"zero (midnight) preserved", 0, 0},
		{"1am preserved", 1, 1},
		{"11pm preserved", 23, 23},
		{"7am preserved", 7, 7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := applyDefaults(RunnerOptions{CompactionHourUTC: tt.in}).CompactionHourUTC
			if got != tt.want {
				t.Errorf("CompactionHourUTC=%d → %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

// TestRandomHex generates a hex string of correct length. The signature
// is randomHex(n) returning n hex characters (NOT 2n).
//
// NOTE: the implementation uses time.Now().UnixNano() rather than
// crypto/rand, so back-to-back calls within the same nanosecond
// produce IDENTICAL output. The doc claims "12 hex chars is plenty for
// unique within a single workspace's snapshots" — true if calls are at
// least one nanosecond apart, but on modern hardware they often aren't.
// We pin the length contract here and document the determinism as a
// known weakness (filed as a follow-up to switch to crypto/rand).
func TestRandomHex(t *testing.T) {
	tests := []struct {
		name    string
		n       int
		wantLen int
	}{
		{"zero", 0, 0},
		{"one char", 1, 1},
		{"four chars", 4, 4},
		{"sixteen chars", 16, 16},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := randomHex(tt.n)
			if len(got) != tt.wantLen {
				t.Errorf("randomHex(%d) len = %d, want %d", tt.n, len(got), tt.wantLen)
			}
			for _, c := range got {
				if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
					t.Errorf("randomHex(%d) produced non-hex char: %q", tt.n, c)
					break
				}
			}
		})
	}
}

// TestNullableHealthCrew empty-string-to-nil coercion.
func TestNullableHealthCrew(t *testing.T) {
	if got := nullableHealthCrew(""); got != nil {
		t.Errorf("empty → %v want nil", got)
	}
	if got := nullableHealthCrew("crew_x"); got != "crew_x" {
		t.Errorf("non-empty → %v want crew_x", got)
	}
}
