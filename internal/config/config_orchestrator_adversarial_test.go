package config

import "testing"

// TestEnvOverrides_MaxConcurrentRuns_Whitespace pins the whitespace handling
// of CREWSHIP_MAX_CONCURRENT_RUNS in applyEnvOverrides so it agrees with the
// orchestrator's own resolveRunSemCap, which trims before parsing.
//
// The adversarial concern (double env read, angle 3): config.applyEnvOverrides
// and orchestrator.resolveRunSemCap BOTH read CREWSHIP_MAX_CONCURRENT_RUNS but
// with different parsers. resolveRunSemCap does strings.TrimSpace(...) then
// Atoi; applyEnvOverrides originally called Atoi on the raw value. A value with
// surrounding whitespace (" 16 ", trivially produced by a quoted env entry or
// a compose file) was therefore DROPPED by applyEnvOverrides (config stayed at
// the default 8, with a "ignoring invalid" warning) yet HONORED by
// resolveRunSemCap (actual runtime cap 16). The warning lied and the two
// readers disagreed. Both must trim so the validated config value and the live
// semaphore cap can never diverge on whitespace alone.
func TestEnvOverrides_MaxConcurrentRuns_Whitespace(t *testing.T) {
	t.Run("padded valid value is honored, matching resolveRunSemCap", func(t *testing.T) {
		t.Setenv("CREWSHIP_MAX_CONCURRENT_RUNS", " 16 ")

		cfg, err := Load("")
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Orchestrator.MaxConcurrentRuns != 16 {
			t.Errorf("expected padded \" 16 \" to resolve to 16 (as resolveRunSemCap does), got %d", cfg.Orchestrator.MaxConcurrentRuns)
		}
	})

	t.Run("padded zero fails validation loudly, not silently ignored", func(t *testing.T) {
		// A padded non-positive value must fail-loud exactly like the
		// unpadded "0" case, rather than silently falling back to the
		// default while pretending the operator's value was invalid syntax.
		t.Setenv("CREWSHIP_MAX_CONCURRENT_RUNS", " 0 ")

		if _, err := Load(""); err == nil {
			t.Error("expected Load to fail validation for CREWSHIP_MAX_CONCURRENT_RUNS=\" 0 \", got nil error")
		}
	})
}
