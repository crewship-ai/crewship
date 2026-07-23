package config

import "testing"

// TestDefault_OrchestratorMaxConcurrentRuns locks the default agent-run
// concurrency cap at 8, matching orchestrator.defaultRunSemCap.
func TestDefault_OrchestratorMaxConcurrentRuns(t *testing.T) {
	cfg := Default()
	if cfg.Orchestrator.MaxConcurrentRuns != 8 {
		t.Errorf("expected orchestrator.max_concurrent_runs default of 8, got %d", cfg.Orchestrator.MaxConcurrentRuns)
	}
}

// TestEnvOverrides_MaxConcurrentRuns proves CREWSHIP_MAX_CONCURRENT_RUNS
// lands on cfg.Orchestrator.MaxConcurrentRuns, and that an invalid value is
// ignored (leaving the default/config value untouched).
func TestEnvOverrides_MaxConcurrentRuns(t *testing.T) {
	t.Run("valid value overrides default", func(t *testing.T) {
		t.Setenv("CREWSHIP_MAX_CONCURRENT_RUNS", "16")

		cfg, err := Load("")
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Orchestrator.MaxConcurrentRuns != 16 {
			t.Errorf("expected 16, got %d", cfg.Orchestrator.MaxConcurrentRuns)
		}
	})

	t.Run("invalid value ignored, default preserved", func(t *testing.T) {
		t.Setenv("CREWSHIP_MAX_CONCURRENT_RUNS", "not-a-number")

		cfg, err := Load("")
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Orchestrator.MaxConcurrentRuns != 8 {
			t.Errorf("expected default of 8 preserved, got %d", cfg.Orchestrator.MaxConcurrentRuns)
		}
	})

	t.Run("non-positive value fails validation", func(t *testing.T) {
		// "0" parses cleanly (so applyEnvOverrides accepts it, mirroring
		// CREWSHIP_PORT's own no-range-check-at-parse-time behavior) but
		// Validate rejects a non-positive concurrency cap — fail loud
		// rather than silently substituting a different value than what
		// the operator asked for.
		t.Setenv("CREWSHIP_MAX_CONCURRENT_RUNS", "0")

		if _, err := Load(""); err == nil {
			t.Error("expected Load to fail validation for CREWSHIP_MAX_CONCURRENT_RUNS=0, got nil error")
		}
	})
}

// TestValidate_MaxConcurrentRuns proves a non-positive configured value
// fails validation instead of silently reaching the orchestrator.
func TestValidate_MaxConcurrentRuns(t *testing.T) {
	cfg := Default()
	cfg.Orchestrator.MaxConcurrentRuns = 0
	if err := cfg.Validate(); err == nil {
		t.Error("expected validation error for orchestrator.max_concurrent_runs=0, got nil")
	}

	cfg.Orchestrator.MaxConcurrentRuns = -1
	if err := cfg.Validate(); err == nil {
		t.Error("expected validation error for orchestrator.max_concurrent_runs=-1, got nil")
	}

	cfg.Orchestrator.MaxConcurrentRuns = 8
	if err := cfg.Validate(); err != nil {
		t.Errorf("expected no validation error for a positive value, got %v", err)
	}
}
