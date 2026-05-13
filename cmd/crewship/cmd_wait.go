package main

import (
	"context"
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
// Exit codes:
//
//	0  COMPLETED
//	1  FAILED
//	2  CANCELLED
//	3  TIMEOUT (server-side or --timeout reached)
//	4  network/auth error before any status could be read
var waitCmd = &cobra.Command{
	Use:   "wait <run-id>",
	Short: "Wait for a run to reach a terminal status",
	Long: `Poll a run's status until it COMPLETED, FAILED, CANCELLED, or TIMEOUT.

Useful in scripts and CI:

  RUN=$(crewship ask --no-stream -q "do X" | jq -r .id)
  crewship wait "$RUN" && echo "done" || echo "broke"

Exit code reflects the terminal status (0 done, 1 failed, 2 cancelled,
3 timeout, 4 connection error).`,
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
		onTick := func(d *cli.RunDetail) {
			if quiet || f.Format != "table" && f.Format != "" {
				return
			}
			if d.Status != lastStatus {
				lastStatus = d.Status
				fmt.Fprintf(os.Stderr, "%s[wait]%s %s status=%s elapsed=%s\n",
					cli.Dim, cli.Reset, runID, d.Status, time.Since(start).Truncate(time.Second))
			}
		}

		detail, err := client.PollRun(ctx, runID, interval, onTick)
		if err != nil {
			// Order matters: check ctx.Err() FIRST so we don't call
			// .Error() on a nil context error in the timeout branch.
			// A short-statement init-expression evaluates before the
			// condition, so the previous form `ctx.Err().Error()` would
			// panic when ctx had no error but PollRun returned a non-
			// context error of its own.
			if ctx.Err() != nil {
				if !quiet {
					fmt.Fprintf(os.Stderr, "%s[wait]%s timeout after %s: %v\n",
						cli.Yellow, cli.Reset, time.Since(start).Truncate(time.Second), ctx.Err())
				}
				os.Exit(3)
			}
			fmt.Fprintf(os.Stderr, "%s[wait]%s error: %v\n", cli.Red, cli.Reset, err)
			os.Exit(4)
		}

		// Emit terminal state for the user / pipeline.
		switch f.Format {
		case "json", "yaml", "ndjson":
			_ = f.Auto(detail, nil, nil)
		default:
			if !quiet {
				fmt.Printf("%s[done]%s %s status=%s elapsed=%s\n",
					cli.Green, cli.Reset, runID, detail.Status, time.Since(start).Truncate(time.Second))
			} else {
				fmt.Println(detail.Status)
			}
		}

		// Status-aware exit code so scripts can act without reparsing JSON.
		switch strings.ToUpper(detail.Status) {
		case "COMPLETED":
			return nil
		case "FAILED":
			os.Exit(1)
		case "CANCELLED":
			os.Exit(2)
		case "TIMEOUT":
			os.Exit(3)
		}
		return nil
	},
}

func init() {
	waitCmd.Flags().Duration("timeout", 30*time.Minute, "Maximum time to wait (0 = forever)")
	waitCmd.Flags().Duration("interval", 2*time.Second, "Poll interval")
	waitCmd.Flags().BoolP("quiet", "q", false, "Only print the terminal status")
	rootCmd.AddCommand(waitCmd)
}
