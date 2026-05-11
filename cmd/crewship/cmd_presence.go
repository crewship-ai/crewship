package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// presenceCmd is the Watch Roster surface — a live view of who is
// online/busy/blocked/offline across a crew or workspace. Live against
// GET /api/v1/presence/roster[?crew_id=…]; status flips are emitted by
// the agent runtime on transition, not by this CLI.
var presenceCmd = &cobra.Command{
	Use:   "presence",
	Short: "Watch Roster — who is online/busy/blocked",
	Long: `Show the Watch Roster — the live presence board tracking agent status
(online, busy, blocked, offline) across a crew or the full workspace.

Examples:
  crewship presence roster
  crewship presence roster --crew backend-team
  crewship presence roster --crew backend-team --watch
  crewship presence roster --watch --interval 2s

--crew accepts either a slug or a CUID; slugs are resolved against
/api/v1/crews. --watch polls the roster on an interval and re-renders
the table in place; Ctrl-C exits cleanly.`,
}

// presenceRosterCmd fetches and renders the roster. When --watch is
// set, the command loops on --interval and re-renders the table —
// real SSE isn't wired on the server side for presence today, so a
// short-polling loop is the honest implementation. The interval is
// floored at 1s to avoid hammering the API when a user fat-fingers
// a sub-second value.
var presenceRosterCmd = &cobra.Command{
	Use:   "roster",
	Short: "Show the current presence roster (with optional --watch)",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()

		// Resolve --crew slug → ID before each render so the request
		// path stays stable for the watch loop. resolveCrewID
		// short-circuits when the value already looks like a CUID, so
		// the cost is bounded to one /crews lookup per invocation in
		// the slug case and zero in the CUID case.
		crewFlag, _ := cmd.Flags().GetString("crew")
		var crewID string
		if crewFlag != "" {
			id, err := resolveCrewID(client, crewFlag)
			if err != nil {
				return err
			}
			crewID = id
		}

		watch, _ := cmd.Flags().GetBool("watch")
		interval, _ := cmd.Flags().GetDuration("interval")
		// Floor + sanity defaults: 0 → 5s default; negative or sub-second
		// values are clamped to 1s so a typo can't fork-bomb the server.
		if interval <= 0 {
			interval = 5 * time.Second
		}
		if interval < time.Second {
			interval = time.Second
		}

		render := func() error {
			path := "/api/v1/presence/roster"
			if crewID != "" {
				path += "?" + url.Values{"crew_id": {crewID}}.Encode()
			}
			resp, err := client.Get(path)
			if err != nil {
				return err
			}
			if err := cli.CheckError(resp); err != nil {
				return err
			}
			var body struct {
				Rows []struct {
					AgentID string         `json:"agent_id"`
					CrewID  string         `json:"crew_id"`
					Status  string         `json:"status"`
					Since   string         `json:"since"`
					Details map[string]any `json:"details"`
				} `json:"rows"`
				Count int `json:"count"`
			}
			if err := cli.ReadJSON(resp, &body); err != nil {
				return err
			}

			f := newFormatter()
			if f.Format == "json" {
				return f.JSON(body.Rows)
			}
			if f.Format == "yaml" {
				return f.YAML(body.Rows)
			}
			if len(body.Rows) == 0 {
				fmt.Println("(roster empty — no presence entries for scope)")
				return nil
			}
			header := []string{"AGENT", "CREW", "STATUS", "SINCE"}
			rows := make([][]string, 0, len(body.Rows))
			for _, r := range body.Rows {
				rows = append(rows, []string{r.AgentID, r.CrewID, r.Status, r.Since})
			}
			f.Table(header, rows)
			return nil
		}

		if !watch {
			return render()
		}

		// --watch: poll loop with ANSI clear. Real SSE isn't wired for
		// presence yet; if/when it lands, replace this with StreamSSE.
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		// Initial render before the first sleep so the user sees output
		// immediately rather than waiting one full interval.
		clearScreen()
		fmt.Printf("%spresence roster — polling every %s (Ctrl-C to exit)%s\n\n",
			cli.Dim, interval, cli.Reset)
		if err := render(); err != nil {
			fmt.Fprintf(os.Stderr, "%s[render error: %v]%s\n", cli.Yellow, err, cli.Reset)
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				clearScreen()
				fmt.Printf("%spresence roster — polling every %s (Ctrl-C to exit)%s\n\n",
					cli.Dim, interval, cli.Reset)
				if err := render(); err != nil {
					// Print but keep looping; a transient error
					// shouldn't kill the watch session.
					fmt.Fprintf(os.Stderr, "%s[render error: %v]%s\n", cli.Yellow, err, cli.Reset)
				}
			}
		}
	},
}

// clearScreen does an ANSI clear + home so the watch loop re-renders
// in place rather than scrolling. Uses the simplest sequence that works
// across modern terminals; if stdout isn't a TTY this still prints,
// which is fine for piped output (it just adds the escape bytes).
func clearScreen() {
	fmt.Print("\033[2J\033[H")
}

func init() {
	presenceRosterCmd.Flags().String("crew", "", "Filter by crew slug or ID")
	presenceRosterCmd.Flags().Bool("watch", false, "Re-poll the roster on an interval (Ctrl-C to exit)")
	presenceRosterCmd.Flags().Duration("interval", 5*time.Second, "Polling interval for --watch (floored at 1s)")
	presenceCmd.AddCommand(presenceRosterCmd)
}
