package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// waitCmd blocks until a run reaches a terminal status.
//
// The CLI surface mirrors `kubectl wait`: takes a resource id, polls
// until the resource transitions to a final state, exits with a
// status-aware exit code so scripts can act on the outcome without
// reparsing JSON.
//
// The id may be an agent run OR a routine (pipeline) run: the agent-run
// endpoint is probed first and a 404 falls through to the routine-run
// endpoint, so callers don't have to know which subsystem produced the
// id. --routine skips the agent probe when the caller already knows.
//
// Exit codes:
//
//	0  COMPLETED (or a routine dry_run)
//	1  FAILED (or a routine interrupted by a server restart)
//	2  CANCELLED
//	3  TIMEOUT (server-side or --timeout reached)
//	4  network/auth error, or the id matched neither run kind
var waitCmd = &cobra.Command{
	Use:   "wait <run-id>",
	Short: "Wait for an agent or routine run to reach a terminal status",
	Long: `Poll a run's status until it COMPLETED, FAILED, CANCELLED, or TIMEOUT.

Works for both agent runs and routine (pipeline) runs — the agent-run
endpoint is tried first, and on a 404 the id is retried as a routine
run. Pass --routine to skip the agent probe when the id is known to be
a routine run (e.g. the run_id printed by 'crewship routine run').

Useful in scripts and CI:

  RUN=$(crewship ask --no-stream -q "do X" | jq -r .id)
  crewship wait "$RUN" && echo "done" || echo "broke"

A routine run parked on a human approval (status "waiting") is NOT
terminal — wait keeps polling until the waitpoint is approved/rejected
and the run finishes, or --timeout fires.

Exit code reflects the terminal status (0 done, 1 failed, 2 cancelled,
3 timeout, 4 connection error / unknown run id).`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		runID := args[0]

		timeout, _ := cmd.Flags().GetDuration("timeout")
		interval, _ := cmd.Flags().GetDuration("interval")
		if interval <= 0 {
			interval = 2 * time.Second
		}
		routineOnly, _ := cmd.Flags().GetBool("routine")

		ctx := cmd.Context()
		if timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}

		f := newFormatter()
		quiet, _ := cmd.Flags().GetBool("quiet")

		start := time.Now()
		var lastStatus string
		tick := func(status string) {
			if quiet || f.Format != "table" && f.Format != "" {
				return
			}
			if status != lastStatus {
				lastStatus = status
				fmt.Fprintf(os.Stderr, "%s[wait]%s %s status=%s elapsed=%s\n",
					cli.Dim, cli.Reset, runID, status, time.Since(start).Truncate(time.Second))
			}
		}

		// exitForPollError maps a poll failure to wait's exit-code
		// contract: 3 when OUR --timeout fired, 4 otherwise (including a
		// Ctrl-C cancellation — that's an aborted wait, not a timeout).
		exitForPollError := func(err error) {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				if !quiet {
					fmt.Fprintf(os.Stderr, "%s[wait]%s timeout after %s: %v\n",
						cli.Yellow, cli.Reset, time.Since(start).Truncate(time.Second), ctx.Err())
				}
				os.Exit(3)
			}
			if errors.Is(ctx.Err(), context.Canceled) {
				if !quiet {
					fmt.Fprintf(os.Stderr, "%s[wait]%s cancelled after %s\n",
						cli.Yellow, cli.Reset, time.Since(start).Truncate(time.Second))
				}
				os.Exit(4)
			}
			fmt.Fprintf(os.Stderr, "%s[wait]%s error: %v\n", cli.Red, cli.Reset, err)
			os.Exit(4)
		}

		var status string
		var payload interface{}

		if !routineOnly {
			detail, err := client.PollRun(ctx, runID, interval, func(d *cli.RunDetail) { tick(d.Status) })
			switch {
			case err == nil:
				status = strings.ToUpper(detail.Status)
				payload = detail
			case isNotFound(err) && ctx.Err() == nil:
				// Unknown to the agent-run endpoint — fall through to
				// the routine-run probe below.
			default:
				exitForPollError(err)
			}
		}
		if status == "" {
			detail, err := client.PollPipelineRun(ctx, runID, interval, func(d *cli.PipelineRunDetail) { tick(d.Status) })
			if err != nil {
				if isNotFound(err) && ctx.Err() == nil && !routineOnly {
					fmt.Fprintf(os.Stderr, "%s[wait]%s %s matched neither an agent run nor a routine run in this workspace\n",
						cli.Red, cli.Reset, runID)
					os.Exit(4)
				}
				exitForPollError(err)
			}
			status = strings.ToUpper(detail.Status)
			payload = detail
		}

		// Emit terminal state for the user / pipeline.
		switch f.Format {
		case "json", "yaml", "ndjson":
			_ = f.Auto(payload, nil, nil)
		default:
			if !quiet {
				fmt.Printf("%s[done]%s %s status=%s elapsed=%s\n",
					cli.Green, cli.Reset, runID, status, time.Since(start).Truncate(time.Second))
			} else {
				fmt.Println(status)
			}
		}

		// Status-aware exit code so scripts can act without reparsing JSON.
		// DRY_RUN is a routine-run success; INTERRUPTED is a routine run
		// the previous server lifetime never finished — a failure.
		switch status {
		case "COMPLETED", "DRY_RUN":
			return nil
		case "FAILED", "INTERRUPTED":
			os.Exit(1)
		case "CANCELLED":
			os.Exit(2)
		case "TIMEOUT":
			os.Exit(3)
		}
		return nil
	},
}

// isNotFound reports whether err carries an HTTP 404 from the API.
func isNotFound(err error) bool {
	var apiErr *cli.APIError
	return errors.As(err, &apiErr) && apiErr.Status == 404
}

func init() {
	waitCmd.Flags().Duration("timeout", 30*time.Minute, "Maximum time to wait (0 = forever)")
	waitCmd.Flags().Duration("interval", 2*time.Second, "Poll interval")
	waitCmd.Flags().BoolP("quiet", "q", false, "Only print the terminal status")
	waitCmd.Flags().Bool("routine", false, "Treat <run-id> as a routine (pipeline) run id; skip the agent-run probe")
	rootCmd.AddCommand(waitCmd)
}
