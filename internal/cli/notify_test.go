package cli

import (
	"context"
	"testing"
)

func TestNotificationsEnabled(t *testing.T) {
	cases := []struct {
		name string
		cfg  *CLIConfig
		want bool
	}{
		{"nil config", nil, false},
		{"default config (zero value)", &CLIConfig{}, false},
		{"explicitly enabled", &CLIConfig{Notifications: true}, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := NotificationsEnabled(tc.cfg); got != tc.want {
				t.Errorf("NotificationsEnabled = %v, want %v", got, tc.want)
			}
		})
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
	_ = OSNotify(context.Background(), "test", "body", NotifyInfo)
}
