package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// watchWrap turns any list/show command into an auto-refreshing dashboard.
// On each tick it clears the screen with the ANSI "clear+home" sequence
// and reruns the wrapped RunE. Ctrl-C ends cleanly without leaving the
// terminal in a broken state (we restore the cursor).
//
// Usage from a command's Cobra wiring:
//
//	cmd.Flags().String("watch", "", "Auto-refresh every <duration> (e.g. 5s, 1m)")
//	original := cmd.RunE
//	cmd.RunE = watchWrap(original)
//
// The wrapper is a no-op when --watch is empty, so commands keep their
// existing behaviour by default.
//
// Why generic ANSI rather than tcell/termbox: this is intentionally
// lightweight — every Cobra-printed line stays unchanged, the wrapper
// just nukes the screen between iterations. Heavier UIs go through
// bubbletea, which is a separate question.
func watchWrap(inner func(*cobra.Command, []string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		watchSpec, _ := cmd.Flags().GetString("watch")
		if watchSpec == "" {
			return inner(cmd, args)
		}
		dur, err := time.ParseDuration(watchSpec)
		if err != nil {
			return fmt.Errorf("--watch: %w", err)
		}
		// Clamp to a sensible minimum — sub-second polls hammer the server
		// for no human benefit. 1s feels live enough; servers stay friendly.
		if dur < time.Second {
			dur = time.Second
		}

		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		// Hide cursor for the duration, restore on exit. ANSI sequences:
		//   "\x1b[?25l"  hide cursor
		//   "\x1b[?25h"  show cursor
		//   "\x1b[2J\x1b[H"  clear screen + cursor home
		fmt.Fprint(os.Stderr, "\x1b[?25l")
		defer fmt.Fprint(os.Stderr, "\x1b[?25h")

		ticker := time.NewTicker(dur)
		defer ticker.Stop()

		render := func() {
			fmt.Fprint(os.Stderr, "\x1b[2J\x1b[H")
			fmt.Fprintf(os.Stderr, "%s[watching · refresh %s · Ctrl-C to exit · %s]%s\n\n",
				cli.Dim, dur, time.Now().Format("15:04:05"), cli.Reset)
			if err := inner(cmd, args); err != nil {
				fmt.Fprintf(os.Stderr, "%s[error]%s %v\n", cli.Red, cli.Reset, err)
			}
		}

		render()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				render()
			}
		}
	}
}

// addWatchFlag is the canonical way to add the --watch flag to a command.
// Pair it with watchWrap on the RunE so the flag actually does something.
//
// Used by: history, cost, paymaster, presence, journal (non-follow).
func addWatchFlag(cmd *cobra.Command) {
	cmd.Flags().String("watch", "", "Auto-refresh every <duration> (e.g. 5s, 1m). Min 1s.")
}
