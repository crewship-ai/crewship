package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
)

// NotifyLevel categorises a notification so platform-specific renderers
// can route urgent ones differently (e.g., linux's `-u critical`).
type NotifyLevel int

const (
	NotifyInfo NotifyLevel = iota
	NotifyWarn
	NotifyCritical
)

// notifierState carries one-time warning state so a missing CLI tool
// (notify-send, osascript) doesn't spam stderr on every invocation.
var (
	notifierWarnOnce  sync.Once
	notifierAvailable = true
)

// OSNotify shows a desktop notification with title and body.
//
// Platform dispatch:
//   - darwin: osascript -e 'display notification "body" with title "title"'
//   - linux:  notify-send [--urgency=critical] "title" "body"
//   - other:  no-op (returns nil; one-time stderr warning the first call)
//
// Best-effort: a missing CLI tool, an unsupported GOOS, or any exec
// failure returns an error so callers can decide whether to surface it,
// but the typical CLI flow logs+continues — notifications are nice-to-
// have, never load-bearing.
func OSNotify(title, body string, level NotifyLevel) error {
	if title == "" {
		title = "Crewship"
	}
	switch runtime.GOOS {
	case "darwin":
		return osNotifyDarwin(title, body)
	case "linux":
		return osNotifyLinux(title, body, level)
	case "windows":
		return osNotifyWindows(title, body)
	default:
		notifierWarnOnce.Do(func() {
			fmt.Fprintf(os.Stderr, "%s[notify]%s desktop notifications not supported on %s\n",
				Yellow, Reset, runtime.GOOS)
			notifierAvailable = false
		})
		return nil
	}
}

// osNotifyDarwin shells out to `osascript`. Both title and body are
// quoted by escaping embedded double-quotes — osascript's string syntax
// doesn't support backslash escapes, so we replace " with the typographic
// equivalent. Newlines collapse to spaces (osascript ignores them).
func osNotifyDarwin(title, body string) error {
	clean := func(s string) string {
		s = strings.ReplaceAll(s, "\n", " ")
		s = strings.ReplaceAll(s, "\r", " ")
		return strings.ReplaceAll(s, `"`, `\"`)
	}
	script := fmt.Sprintf(`display notification "%s" with title "%s"`, clean(body), clean(title))
	cmd := exec.Command("osascript", "-e", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("osascript: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// osNotifyLinux invokes `notify-send` if present. Maps NotifyLevel to
// the freedesktop urgency hint (low/normal/critical) which most notify
// daemons (mako, dunst, notification-daemon) honour to elevate display
// priority or skip auto-dismiss.
func osNotifyLinux(title, body string, level NotifyLevel) error {
	if _, err := exec.LookPath("notify-send"); err != nil {
		notifierWarnOnce.Do(func() {
			fmt.Fprintf(os.Stderr, "%s[notify]%s notify-send not installed; install libnotify-bin\n",
				Yellow, Reset)
		})
		return errors.New("notify-send not installed")
	}
	urgency := "normal"
	switch level {
	case NotifyCritical:
		urgency = "critical"
	case NotifyInfo:
		urgency = "low"
	}
	cmd := exec.Command("notify-send", "--urgency="+urgency, "--app-name=Crewship", title, body)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("notify-send: %w", err)
	}
	return nil
}

// osNotifyWindows uses PowerShell's BurntToast module if available,
// falling back to a no-op. The fallback is intentional — installing
// BurntToast is not something the CLI should require of Windows users
// merely so an approval ping can pop up.
func osNotifyWindows(title, body string) error {
	if _, err := exec.LookPath("powershell.exe"); err != nil {
		notifierWarnOnce.Do(func() {
			fmt.Fprintf(os.Stderr, "%s[notify]%s powershell.exe not found; notifications disabled\n",
				Yellow, Reset)
		})
		return errors.New("powershell not found")
	}
	// Single-line PowerShell that no-ops if BurntToast is missing so the
	// user doesn't see a stack trace.
	ps := fmt.Sprintf(`if (Get-Module -ListAvailable -Name BurntToast) { Import-Module BurntToast; New-BurntToastNotification -Text "%s","%s" }`,
		strings.ReplaceAll(title, `"`, ``), strings.ReplaceAll(body, `"`, ``))
	cmd := exec.Command("powershell.exe", "-NoProfile", "-Command", ps)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("powershell: %w", err)
	}
	return nil
}

// NotificationsEnabled returns whether the user has opted in via
// `notifications: true` in cli-config.yaml. Returns false on a nil cfg
// (paranoid guard for early-init paths).
func NotificationsEnabled(cfg *CLIConfig) bool {
	if cfg == nil {
		return false
	}
	return cfg.Notifications
}
