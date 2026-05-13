package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// notifyCmd manages desktop notifications. The actual emission helpers
// live in internal/cli/notify.go; this is the user-facing surface for
// enabling, testing, and explicitly firing a notification.
var notifyCmd = &cobra.Command{
	Use:   "notify",
	Short: "Manage desktop notifications",
	Long: `Crewship can ping you when a long run completes, an approval is
waiting, or an escalation arrives — useful when the CLI is in the
background.

Subcommands:
  enable       opt in
  disable      opt out
  status       show current setting
  test         fire one notification right now
  send         send an ad-hoc notification (scripting)`,
}

var notifyEnableCmd = &cobra.Command{
	Use:   "enable",
	Short: "Opt in to desktop notifications",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := cli.LoadConfig()
		if err != nil {
			cfg = &cli.CLIConfig{}
		}
		cfg.Notifications = true
		if err := cli.SaveConfig(cfg); err != nil {
			return err
		}
		fmt.Println("notifications: enabled")
		return nil
	},
}

var notifyDisableCmd = &cobra.Command{
	Use:   "disable",
	Short: "Opt out of desktop notifications",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := cli.LoadConfig()
		if err != nil {
			cfg = &cli.CLIConfig{}
		}
		cfg.Notifications = false
		if err := cli.SaveConfig(cfg); err != nil {
			return err
		}
		fmt.Println("notifications: disabled")
		return nil
	},
}

var notifyStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show whether notifications are enabled",
	RunE: func(cmd *cobra.Command, args []string) error {
		on := cli.NotificationsEnabled(cliCfg)
		if on {
			fmt.Println("notifications: enabled")
		} else {
			fmt.Println("notifications: disabled")
		}
		return nil
	},
}

var notifyTestCmd = &cobra.Command{
	Use:   "test",
	Short: "Fire a test notification right now",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := cli.OSNotify(cmd.Context(), "Crewship", "test notification — your setup works", cli.NotifyInfo); err != nil {
			return err
		}
		fmt.Println("notification sent")
		return nil
	},
}

var notifySendCmd = &cobra.Command{
	Use:   "send <title> <body>",
	Short: "Send an ad-hoc notification (scripting hook)",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		levelStr, _ := cmd.Flags().GetString("level")
		level := cli.NotifyInfo
		switch levelStr {
		case "warn":
			level = cli.NotifyWarn
		case "critical":
			level = cli.NotifyCritical
		}
		return cli.OSNotify(cmd.Context(), args[0], args[1], level)
	},
}

// maybeNotifyRunComplete is called from run/ask helpers on `done`/`error`
// events. It only fires when the user opted in AND the run took longer
// than the threshold — short runs would feel noisy.
//
// Threshold defaults to 30 seconds; override with CREWSHIP_NOTIFY_LONG_RUN
// (integer seconds) when a tighter or looser cadence fits the workflow.
func maybeNotifyRunComplete(startedAt time.Time, agentSlug, finalStatus string) {
	if !cli.NotificationsEnabled(cliCfg) {
		return
	}
	threshold := 30 * time.Second
	if v := os.Getenv("CREWSHIP_NOTIFY_LONG_RUN"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
			threshold = time.Duration(secs) * time.Second
		}
	}
	elapsed := time.Since(startedAt)
	if elapsed < threshold {
		return
	}
	title := "Crewship — run done"
	level := cli.NotifyInfo
	if finalStatus == "error" || finalStatus == "FAILED" {
		title = "Crewship — run FAILED"
		level = cli.NotifyCritical
	}
	body := fmt.Sprintf("agent=%s elapsed=%s status=%s", agentSlug, elapsed.Truncate(time.Second), finalStatus)
	// Bounded ctx so a hung notification process never holds the run
	// completion path; cli.OSNotify imposes its own 5s timeout but we
	// want the parent ctx to also be cancellable from here in case
	// callers shut down mid-stream.
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	_ = cli.OSNotify(ctx, title, body, level)
}

// maybeNotifyApproval is called by `approvals` poller / SSE listeners
// when a pending approval lands. Opt-in.
func maybeNotifyApproval(approvalID, title string) {
	if !cli.NotificationsEnabled(cliCfg) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	_ = cli.OSNotify(ctx, "Crewship — approval needed", fmt.Sprintf("%s — %s", approvalID, title), cli.NotifyCritical)
}

func init() {
	notifySendCmd.Flags().String("level", "info", "Notification urgency: info|warn|critical")
	notifyCmd.AddCommand(notifyEnableCmd, notifyDisableCmd, notifyStatusCmd, notifyTestCmd, notifySendCmd)
	rootCmd.AddCommand(notifyCmd)
}
