package automation

import "testing"

// Validate must RANGE-CHECK, not rewrite.
//
// It coerced a zero to the default before checking the range, which threw away
// a distinction the API layer takes care to preserve: automationBody uses
// *int, so "the caller did not mention this field" and "the caller set it to
// zero" arrive as different values — and then Validate collapsed them.
//
// The consequence is that debounce_seconds: 0 is unreachable. Zero is INSIDE
// the documented range (0..maxDebounceSeconds) and means "fire on the first
// matching event, do not hold the run open" — a reasonable thing to want, and
// the API answers 200 while storing 10.
func TestValidate_DoesNotRewriteAnExplicitZeroDebounce(t *testing.T) {
	a := validRule()
	a.DebounceSeconds = 0
	if err := a.Validate(); err != nil {
		t.Fatalf("debounce_seconds 0 is inside the documented range and must validate: %v", err)
	}
	if a.DebounceSeconds != 0 {
		t.Errorf("DebounceSeconds = %d, want 0 — Validate rewrote a value the caller chose, and "+
			"the API then reports success for a rule it did not store", a.DebounceSeconds)
	}
}

// max_per_hour 0 is genuinely out of range (the brake must brake), so it must
// be REFUSED rather than quietly rewritten to 60. Silently substituting a
// number is how a caller ends up with a cap they never set.
func TestValidate_RefusesAnOutOfRangeMaxPerHourInsteadOfRewritingIt(t *testing.T) {
	a := validRule()
	a.MaxPerHour = 0
	err := a.Validate()
	if err == nil {
		t.Fatalf("max_per_hour 0 was accepted; stored value is %d", a.MaxPerHour)
	}
	if a.MaxPerHour != 0 {
		t.Errorf("MaxPerHour = %d, want the caller's 0 left intact — Validate must not mutate on "+
			"the refusal path either", a.MaxPerHour)
	}
}

// ApplyDefaults is where "unset" becomes a number, and it is the ONLY place.
// Splitting it out is the point: a caller that knows the field was absent asks
// for defaults, and a caller that knows it was chosen does not.
func TestApplyDefaults_FillsOnlyWhatWasNotChosen(t *testing.T) {
	a := validRule()
	a.DebounceSeconds = 0
	a.MaxPerHour = 0
	a.ApplyDefaults()
	if a.DebounceSeconds != DefaultDebounceSeconds {
		t.Errorf("DebounceSeconds = %d, want %d", a.DebounceSeconds, DefaultDebounceSeconds)
	}
	if a.MaxPerHour != DefaultMaxPerHour {
		t.Errorf("MaxPerHour = %d, want %d", a.MaxPerHour, DefaultMaxPerHour)
	}

	chosen := validRule()
	chosen.DebounceSeconds = 3
	chosen.MaxPerHour = 5
	chosen.ApplyDefaults()
	if chosen.DebounceSeconds != 3 || chosen.MaxPerHour != 5 {
		t.Errorf("ApplyDefaults overwrote chosen values: debounce=%d max=%d",
			chosen.DebounceSeconds, chosen.MaxPerHour)
	}
}

func validRule() Automation {
	return Automation{
		ID:              "aut_1",
		WorkspaceID:     "ws_1",
		Name:            "rule",
		EventType:       "mission.status_change",
		ActionKind:      ActionKindRoutine,
		Action:          Action{RoutineSlug: "triage"},
		DebounceSeconds: DefaultDebounceSeconds,
		MaxPerHour:      DefaultMaxPerHour,
	}
}
