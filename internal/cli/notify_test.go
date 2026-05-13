package cli

import (
	"testing"
)

func TestNotificationsEnabled(t *testing.T) {
	if NotificationsEnabled(nil) {
		t.Fatal("nil cfg should return false")
	}
	cfg := &CLIConfig{}
	if NotificationsEnabled(cfg) {
		t.Fatal("default cfg should return false")
	}
	cfg.Notifications = true
	if !NotificationsEnabled(cfg) {
		t.Fatal("enabled cfg should return true")
	}
}

func TestOSNotify_DoesNotPanicOnUnsupported(t *testing.T) {
	// Regardless of host OS, OSNotify must not panic on a missing
	// dispatch. We can't assert on output (osascript/notify-send may or
	// may not be present in CI) — we just guarantee no crash.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("OSNotify panicked: %v", r)
		}
	}()
	_ = OSNotify("test", "body", NotifyInfo)
}
