package main

// Coverage for `notify send <title> <body>` (cmd_notify.go, notifySendCmd) —
// the one notify subcommand with no test anywhere in the suite.
//
// notifySendCmd calls cli.OSNotify directly with no requireAuth/network
// step, so it needs no stub server. What it DOES depend on is a platform
// notifier (notify-send / osascript / powershell.exe) that a CI runner or a
// headless dev box typically does not have installed — cli.OSNotify's own
// contract (internal/cli/notify.go) is "missing tool -> return an error,
// never panic", so the assertions below tolerate either outcome rather than
// assuming the tool is absent, and instead pin what IS deterministic
// regardless of environment: the args contract and that the command reaches
// OSNotify at all (as opposed to erroring out earlier on flag parsing).

import (
	"runtime"
	"strings"
	"testing"
)

func TestNotifySendCmd_ArgsContract(t *testing.T) {
	if notifySendCmd.Args == nil {
		t.Fatal("notifySendCmd.Args is nil — title/body are both required")
	}
	if err := notifySendCmd.Args(notifySendCmd, []string{"only-title"}); err == nil {
		t.Error("want an error with only one positional arg")
	}
	if err := notifySendCmd.Args(notifySendCmd, []string{"title", "body", "extra"}); err == nil {
		t.Error("want an error with three positional args")
	}
	if err := notifySendCmd.Args(notifySendCmd, []string{"title", "body"}); err != nil {
		t.Errorf("want exactly 2 args to be accepted, got %v", err)
	}
}

// osNotifyOutcomeIsExpected reports whether err matches what OSNotify is
// documented to return on this GOOS when the platform tool is missing (the
// overwhelmingly likely case on a CI runner / headless dev box) — or is nil,
// the case where the tool happens to be installed and the notification
// actually fired.
func osNotifyOutcomeIsExpected(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return // tool installed and the notification fired — also fine
	}
	switch runtime.GOOS {
	case "linux":
		if !strings.Contains(err.Error(), "notify-send") {
			t.Errorf("unexpected error shape on linux: %v", err)
		}
	case "darwin":
		if !strings.Contains(err.Error(), "osascript") {
			t.Errorf("unexpected error shape on darwin: %v", err)
		}
	case "windows":
		if !strings.Contains(err.Error(), "powershell") {
			t.Errorf("unexpected error shape on windows: %v", err)
		}
	default:
		// OSNotify no-ops (returns nil) on every other GOOS, so any non-nil
		// error here would itself be the surprise.
		t.Errorf("unexpected error on unsupported GOOS %s: %v", runtime.GOOS, err)
	}
}

func TestNotifySendRunE_DefaultLevel(t *testing.T) {
	guardCLIState(t)
	if err := notifySendCmd.Flags().Set("level", "info"); err != nil {
		t.Fatal(err)
	}
	err := notifySendCmd.RunE(notifySendCmd, []string{"Crewship", "default level"})
	osNotifyOutcomeIsExpected(t, err)
}

// Table over the level flag's mapping (cmd_notify.go: only "warn" and
// "critical" are recognised; anything else — including this test's "bogus"
// case — silently falls back to NotifyInfo rather than erroring). Whatever
// the level, the command must still reach OSNotify rather than reject the
// flag value, which is what "err is either nil or the platform-tool error"
// proves here.
func TestNotifySendRunE_LevelFlagVariants(t *testing.T) {
	for _, level := range []string{"info", "warn", "critical", "bogus"} {
		t.Run(level, func(t *testing.T) {
			guardCLIState(t)
			if err := notifySendCmd.Flags().Set("level", level); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = notifySendCmd.Flags().Set("level", "info") })
			err := notifySendCmd.RunE(notifySendCmd, []string{"Crewship", "body for " + level})
			osNotifyOutcomeIsExpected(t, err)
		})
	}
}
