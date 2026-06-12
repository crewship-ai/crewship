package main

import (
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
)

// notify enable/disable persist through cli.SaveConfig — covSetupCli10 points
// CREWSHIP_CONFIG at a temp file so nothing touches the real config.

func TestNotifyEnableRunE_PersistsFlag(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")

	out, err := captureStdoutCovCli10(t, func() error {
		return notifyEnableCmd.RunE(notifyEnableCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "notifications: enabled") {
		t.Errorf("enable message missing: %q", out)
	}
	cfg, err := cli.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.Notifications {
		t.Error("Notifications flag not persisted as true")
	}
}

func TestNotifyDisableRunE_PersistsFlag(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	if err := cli.SaveConfig(&cli.CLIConfig{Notifications: true}); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	out, err := captureStdoutCovCli10(t, func() error {
		return notifyDisableCmd.RunE(notifyDisableCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "notifications: disabled") {
		t.Errorf("disable message missing: %q", out)
	}
	cfg, err := cli.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Notifications {
		t.Error("Notifications flag not persisted as false")
	}
}

func TestNotifyStatusRunE_BothStates(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")

	cliCfg = &cli.CLIConfig{Notifications: true}
	out, err := captureStdoutCovCli10(t, func() error {
		return notifyStatusCmd.RunE(notifyStatusCmd, nil)
	})
	if err != nil {
		t.Fatalf("status (on): %v", err)
	}
	if !strings.Contains(out, "notifications: enabled") {
		t.Errorf("enabled status missing: %q", out)
	}

	cliCfg = &cli.CLIConfig{Notifications: false}
	out, err = captureStdoutCovCli10(t, func() error {
		return notifyStatusCmd.RunE(notifyStatusCmd, nil)
	})
	if err != nil {
		t.Fatalf("status (off): %v", err)
	}
	if !strings.Contains(out, "notifications: disabled") {
		t.Errorf("disabled status missing: %q", out)
	}
}

// maybeNotifyRunComplete early-return paths. The actual OS notification
// (osascript / notify-send) is never reached: either opted out, or the
// run finished under the threshold.

func TestMaybeNotifyRunComplete_OptedOutIsNoop(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{Notifications: false}
	// Must return without touching the env-based threshold or the OS
	// notifier; any panic/hang here would fail the test run.
	maybeNotifyRunComplete(time.Now().Add(-10*time.Hour), "viktor", "done")
}

func TestMaybeNotifyRunComplete_UnderThresholdIsNoop(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{Notifications: true}
	// Custom threshold via env: anything under 9999s skips the notify.
	t.Setenv("CREWSHIP_NOTIFY_LONG_RUN", "9999")
	maybeNotifyRunComplete(time.Now(), "viktor", "done")
}

func TestMaybeNotifyRunComplete_BadEnvKeepsDefaultThreshold(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{Notifications: true}
	t.Setenv("CREWSHIP_NOTIFY_LONG_RUN", "not-a-number")
	// Default threshold = 30s; a just-started run stays silent.
	maybeNotifyRunComplete(time.Now(), "viktor", "error")
}
