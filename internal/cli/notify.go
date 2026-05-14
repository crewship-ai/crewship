package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// notifyTimeout caps how long any single OSNotify shell-out may block.
// External tools (osascript, notify-send, powershell) should respond
// near-instantly; if one hangs we'd rather drop the notification than
// stall the CLI's main flow.
const notifyTimeout = 5 * time.Second

// NotifyLevel categorises a notification so platform-specific renderers
// can route urgent ones differently (e.g., linux's `-u critical`).
type NotifyLevel int

const (
	NotifyInfo NotifyLevel = iota
	NotifyWarn
	NotifyCritical
)

// notifierWarnOnce carries one-time warning state so a missing CLI
// tool (notify-send, osascript) doesn't spam stderr on every
// invocation. The "available" boolean previously paired with this
// was only ever written, never read — callers always check the
// concrete error returned by OSNotify instead.
var notifierWarnOnce sync.Once

// OSNotify shows a desktop notification with title and body.
//
// Platform dispatch:
//   - darwin: osascript -e 'display notification "body" with title "title"'
//   - linux:  notify-send [--urgency=critical] "title" "body"
//   - other:  no-op (returns nil; one-time stderr warning the first call)
//
// The ctx is forwarded to the platform exec so CLI shutdown or a
// 5-second internal timeout can abort a hung notification process
// without stalling the main CLI flow. Pass context.Background() at the
// top of a CLI command and the helper will derive a bounded child.
//
// Best-effort: a missing CLI tool, an unsupported GOOS, or any exec
// failure returns an error so callers can decide whether to surface it,
// but the typical CLI flow logs+continues — notifications are nice-to-
// have, never load-bearing.
func OSNotify(ctx context.Context, title, body string, level NotifyLevel) error {
	if title == "" {
		title = "Crewship"
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, notifyTimeout)
	defer cancel()
	switch runtime.GOOS {
	case "darwin":
		return osNotifyDarwin(ctx, title, body)
	case "linux":
		return osNotifyLinux(ctx, title, body, level)
	case "windows":
		return osNotifyWindows(ctx, title, body)
	default:
		notifierWarnOnce.Do(func() {
			fmt.Fprintf(os.Stderr, "%s[notify]%s desktop notifications not supported on %s\n",
				Yellow, Reset, runtime.GOOS)
		})
		return nil
	}
}

// osNotifyDarwin shells out to `osascript`. osascript DOES recognise
// `\"` inside double-quoted AppleScript strings, so we escape embedded
// double quotes with a backslash. Newlines collapse to spaces (osascript
// renders them inline and the OS notification widget ignores them
// anyway), and backslashes themselves are escaped first to keep the
// substitution order safe.
func osNotifyDarwin(ctx context.Context, title, body string) error {
	clean := func(s string) string {
		s = strings.ReplaceAll(s, `\`, `\\`)
		s = strings.ReplaceAll(s, "\n", " ")
		s = strings.ReplaceAll(s, "\r", " ")
		return strings.ReplaceAll(s, `"`, `\"`)
	}
	script := fmt.Sprintf(`display notification "%s" with title "%s"`, clean(body), clean(title))
	cmd := exec.CommandContext(ctx, "osascript", "-e", script)
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
func osNotifyLinux(ctx context.Context, title, body string, level NotifyLevel) error {
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
	// notify-send receives title/body as positional argv slots, NOT
	// interpolated into a shell string, so it's already injection-safe.
	cmd := exec.CommandContext(ctx, "notify-send", "--urgency="+urgency, "--app-name=Crewship", title, body)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("notify-send: %w", err)
	}
	return nil
}

// osNotifyWindows uses PowerShell's BurntToast module if available,
// falling back to a no-op. The fallback is intentional — installing
// BurntToast is not something the CLI should require of Windows users
// merely so an approval ping can pop up.
func osNotifyWindows(ctx context.Context, title, body string) error {
	if _, err := exec.LookPath("powershell.exe"); err != nil {
		notifierWarnOnce.Do(func() {
			fmt.Fprintf(os.Stderr, "%s[notify]%s powershell.exe not found; notifications disabled\n",
				Yellow, Reset)
		})
		return errors.New("powershell not found")
	}
	// Escape user-controlled strings before they enter the inline
	// PowerShell program text. PowerShell double-quoted strings honour
	// `$` for variable expansion, backticks for escapes, and embedded
	// quotes — leaving any of those un-escaped is a command-injection
	// hazard. We strip newlines (PowerShell's `-Text` doesn't render
	// them anyway) and escape the four metacharacter classes
	// individually.
	psEscape := func(s string) string {
		s = strings.ReplaceAll(s, "\r", "")
		s = strings.ReplaceAll(s, "\n", " ")
		s = strings.ReplaceAll(s, "`", "``")
		s = strings.ReplaceAll(s, `"`, "`\"")
		s = strings.ReplaceAll(s, "$", "`$")
		return s
	}
	ps := fmt.Sprintf(`if (Get-Module -ListAvailable -Name BurntToast) { Import-Module BurntToast; New-BurntToastNotification -Text "%s","%s" }`,
		psEscape(title), psEscape(body))
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-Command", ps)
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
