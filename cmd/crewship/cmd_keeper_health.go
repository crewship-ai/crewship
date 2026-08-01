package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// keeperHealthAlarm mirrors internal/api's response.
type keeperHealthAlarm struct {
	Kind    string `json:"kind"`
	Summary string `json:"summary"`
	At      string `json:"at,omitempty"`
}

type keeperHealthResult struct {
	WorkspaceID string `json:"workspace_id"`

	Samples       int `json:"samples"`
	Allow         int `json:"allow"`
	Deny          int `json:"deny"`
	Escalate      int `json:"escalate"`
	JudgeFailures int `json:"judge_failures"`

	AllowRate        float64 `json:"allow_rate"`
	DenyRate         float64 `json:"deny_rate"`
	EscalateRate     float64 `json:"escalate_rate"`
	ProgressedRate   float64 `json:"progressed_rate"`
	JudgeFailureRate float64 `json:"judge_failure_rate"`

	P95LatencyMS int64 `json:"p95_latency_ms"`

	MinSamples            int     `json:"min_samples"`
	AlarmProgressedRate   float64 `json:"alarm_progressed_rate"`
	AlarmJudgeFailureRate float64 `json:"alarm_judge_failure_rate"`

	Alarm *keeperHealthAlarm `json:"alarm,omitempty"`

	Oldest string `json:"oldest,omitempty"`
	Newest string `json:"newest,omitempty"`
}

var keeperHealthCmd = &cobra.Command{
	Use:   "health",
	Short: "How the judge has been deciding lately (requires OWNER or ADMIN)",
	Long: `Print the rolling window of Keeper decisions — the same numbers the collapse
alarm reads, on demand instead of when it fires.

Keeper is fail-closed, so a broken judge and a strict one produce the same
response: DENY, in the right format, with a plausible reason. That is how
crewship#1624 ran for milestones. The alarm exists to catch it, and this is how
you check without waiting to be paged.

Read the SHARE THAT PROGRESSED — granted or escalated — not the allow rate. A
workspace whose credentials are all L4 escalates every request by design and sits
at an allow rate of exactly zero while working perfectly.

Exits non-zero when an alarm is standing, so it works in a cron.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		var out keeperHealthResult
		if err := getJSON(client, "/api/v1/admin/keeper/health", &out); err != nil {
			return keeperPermissionHint(err)
		}

		if ferr := newFormatter().AutoHuman(out, func() { printKeeperHealth(out) }); ferr != nil {
			return ferr
		}
		if out.Alarm != nil {
			return cli.WithExitCode(
				fmt.Errorf("keeper health alarm standing: %s", out.Alarm.Kind), cli.ExitGeneric)
		}
		return nil
	},
}

func printKeeperHealth(h keeperHealthResult) {
	fmt.Printf("%sKeeper decisions%s  rolling window\n", cli.Bold, cli.Reset)

	if h.Samples == 0 {
		fmt.Printf("  %sNo decisions recorded yet.%s The window is in memory, so it also empties\n",
			cli.Dim, cli.Reset)
		fmt.Printf("  %son restart — this is not a claim that the judge is healthy.%s\n", cli.Dim, cli.Reset)
		return
	}

	pct := func(v float64) string { return fmt.Sprintf("%.0f%%", v*100) }
	fmt.Printf("  Decisions:    %d\n", h.Samples)
	fmt.Printf("  ALLOW:        %-4d %s\n", h.Allow, pct(h.AllowRate))
	fmt.Printf("  ESCALATE:     %-4d %s\n", h.Escalate, pct(h.EscalateRate))
	fmt.Printf("  DENY:         %-4d %s\n", h.Deny, pct(h.DenyRate))

	// The line the thresholds actually compare, called what it is.
	progressed := pct(h.ProgressedRate)
	colour := cli.Green
	if h.ProgressedRate < h.AlarmProgressedRate {
		colour = cli.Red
	}
	fmt.Printf("  Got somewhere:%s %s%s  (granted or escalated; alarms under %s)\n",
		colour, progressed, cli.Reset, pct(h.AlarmProgressedRate))

	if h.JudgeFailures > 0 {
		fmt.Printf("  %sJudge failed:  %d  %s%s  (unreachable, timed out or unparseable — each one is a DENY)\n",
			cli.Red, h.JudgeFailures, pct(h.JudgeFailureRate), cli.Reset)
	}
	if h.P95LatencyMS > 0 {
		fmt.Printf("  p95 latency:  %.1fs\n", float64(h.P95LatencyMS)/1000)
	}

	// Sample count before any verdict: a rate over four decisions is not a
	// finding, and the reader should see that before they act on a percentage.
	if h.Samples < h.MinSamples {
		fmt.Printf("\n  %sOnly %d of the %d decisions the alarm needs before it will fire.%s\n",
			cli.Dim, h.Samples, h.MinSamples, cli.Reset)
		fmt.Printf("  %sRates above are real but wide — read them as a hint, not a measurement.%s\n",
			cli.Dim, cli.Reset)
	}

	if h.Alarm != nil {
		fmt.Printf("\n%s! %s%s\n", cli.Red, h.Alarm.Kind, cli.Reset)
		fmt.Printf("  %s\n", h.Alarm.Summary)
		fmt.Printf("  %sCheck the judge itself: crewship keeper judge test%s\n", cli.Dim, cli.Reset)
	}
}
