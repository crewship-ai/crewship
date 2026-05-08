package main

// Routine watch subcommand. Polls the runs endpoint and streams new
// step events to stdout as a live waterfall — same data the UI Runs
// sub-tab renders, but suitable for terminal/pipe/CI consumption.
//
// Why polling not WS: the CLI is a short-lived process; opening a WS
// connection + handling reconnects + dropping into the JWT-token
// auth dance is more code than this is worth for an MVP. We poll
// the runs endpoint with `?include_steps=1` every 2s and dedup by
// (run_id, step_id, kind). Bail when the run terminates.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

type watchEntry struct {
	ID        string                 `json:"id"`
	Timestamp string                 `json:"ts"`
	EntryType string                 `json:"entry_type"`
	Severity  string                 `json:"severity"`
	Summary   string                 `json:"summary"`
	RunID     string                 `json:"run_id,omitempty"`
	Payload   map[string]interface{} `json:"payload,omitempty"`
}

var routineWatchCmd = &cobra.Command{
	Use:   "watch <slug>",
	Short: "Stream a routine's run + step events live to stdout (Ctrl-C to stop)",
	Long: `Watches the named routine for new run + step events. Outputs each
event on its own line as either ANSI-coloured human text (default) or
JSON Lines (--json). Polls every 2s; loops until interrupted or
--once is set + a terminal event arrives.

Useful for CI / scripting: pipe to jq or grep to make decisions on
event-by-event data, or just tail in a second terminal while you
trigger runs from the UI.

Examples:
  crewship routine watch summarize-text
  crewship routine watch summarize-text --since 5m
  crewship routine watch summarize-text --json | jq '.entry_type'
  crewship routine watch summarize-text --once  # exit after first run completes
  crewship routine watch summarize-text --run-id <id>  # filter to one run
`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		slug := args[0]
		jsonMode, _ := cmd.Flags().GetBool("json")
		once, _ := cmd.Flags().GetBool("once")
		runIDFilter, _ := cmd.Flags().GetString("run-id")
		intervalFlag, _ := cmd.Flags().GetDuration("interval")
		if intervalFlag <= 0 {
			intervalFlag = 2 * time.Second
		}

		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()

		// Set up Ctrl-C / SIGTERM handler so an interactive watch
		// exits cleanly without "operation timed out" mess.
		ctx, cancel := context.WithCancel(cmd.Context())
		defer cancel()
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			<-sigCh
			cancel()
		}()

		// Dedupe across polls: an event has shape
		// (run_id|entry_type|step_id) — emit once. Keep this map
		// small by capping at 10k entries (older first); a 10k cap
		// matches the journal page LIMIT and is more than enough for
		// reasonably-sized runs.
		seen := make(map[string]bool, 1024)
		// firstPollSeen pre-loads `seen` from the first poll so
		// --once doesn't fire on a historical run that already
		// terminated before this command started. Without this, the
		// CI flow `crewship routine run X && crewship routine watch
		// X --once` exits immediately on the first poll because the
		// previous run's run.completed is in the response window.
		firstPollSeen := false

		ticker := time.NewTicker(intervalFlag)
		defer ticker.Stop()
		fmt.Fprintf(os.Stderr, "Watching routine %q every %s (Ctrl-C to stop)\n", slug, intervalFlag)

	loop:
		for {
			select {
			case <-ctx.Done():
				break loop
			case <-ticker.C:
			}

			path := fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/runs?include_steps=1&limit=200", ws, slug)
			resp, err := client.Get(path)
			if err != nil {
				fmt.Fprintf(os.Stderr, "watch: GET %s: %v\n", path, err)
				continue
			}
			if resp.StatusCode != http.StatusOK {
				fmt.Fprintf(os.Stderr, "watch: HTTP %d on %s\n", resp.StatusCode, path)
				_ = resp.Body.Close()
				continue
			}
			var rows []watchEntry
			if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
				_ = resp.Body.Close()
				fmt.Fprintf(os.Stderr, "watch: decode: %v\n", err)
				continue
			}
			_ = resp.Body.Close()

			// Reverse — server returns DESC; emit oldest-first so
			// the human reading the stream sees the lifecycle in
			// chronological order.
			for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
				rows[i], rows[j] = rows[j], rows[i]
			}

			var lastTerminal string
			for _, r := range rows {
				if runIDFilter != "" && r.RunID != runIDFilter {
					continue
				}
				stepID := ""
				if r.Payload != nil {
					if v, ok := r.Payload["step_id"].(string); ok {
						stepID = v
					}
				}
				key := r.RunID + "|" + r.EntryType + "|" + stepID + "|" + r.Timestamp
				if seen[key] {
					continue
				}
				seen[key] = true
				// On the first poll, pre-seed `seen` without
				// printing — events that were already in the API
				// window when the watch started are historical;
				// printing them would surprise users running
				// `routine watch X --once` after a previous run
				// already completed (the prior terminal would
				// trigger immediate exit). Skip emit + skip the
				// terminal trigger on this initial fill.
				if !firstPollSeen {
					continue
				}
				if jsonMode {
					b, _ := json.Marshal(r)
					fmt.Println(string(b))
				} else {
					fmt.Println(formatWatchEntry(r, stepID))
				}
				if r.EntryType == "pipeline.run.completed" || r.EntryType == "pipeline.run.failed" {
					lastTerminal = r.RunID
				}
			}
			firstPollSeen = true

			if once && lastTerminal != "" && (runIDFilter == "" || lastTerminal == runIDFilter) {
				fmt.Fprintln(os.Stderr, "Run terminated; exiting (--once).")
				break loop
			}

			// Cheap LRU-ish cap so a long-running watch doesn't
			// grow the dedupe map unbounded. We just dump the map
			// when it crosses the threshold; the next poll will
			// re-discover whatever's still in the API window, and
			// the bounded API window means at worst we double-print
			// a small batch.
			if len(seen) > 10000 {
				seen = make(map[string]bool, 1024)
			}
		}

		_ = cli.CheckError // keep import live even when build trims unused
		return nil
	},
}

func formatWatchEntry(e watchEntry, stepID string) string {
	t, err := time.Parse(time.RFC3339Nano, e.Timestamp)
	if err != nil {
		t, _ = time.Parse(time.RFC3339, e.Timestamp)
	}
	timestamp := t.Local().Format("15:04:05.000")
	kind := strings.TrimPrefix(e.EntryType, "pipeline.")
	prefix := colourize(kind)
	suffix := ""
	if stepID != "" {
		suffix = " step=" + stepID
	}
	if e.Severity == "error" {
		return fmt.Sprintf("\x1b[31m%s %s%s\x1b[0m  %s", timestamp, prefix, suffix, e.Summary)
	}
	return fmt.Sprintf("%s %s%s  %s", timestamp, prefix, suffix, e.Summary)
}

// colourize wraps the entry-type label in ANSI colour codes by
// severity-of-meaning. We use the same palette as the UI badges so
// terminal + browser stay visually consistent for users who flip
// between them.
func colourize(kind string) string {
	switch {
	case strings.HasSuffix(kind, ".completed"):
		return "\x1b[32m" + kind + "\x1b[0m"
	case strings.HasSuffix(kind, ".failed"), strings.HasSuffix(kind, ".validation_failed"):
		return "\x1b[31m" + kind + "\x1b[0m"
	case strings.HasSuffix(kind, ".started"):
		return "\x1b[36m" + kind + "\x1b[0m"
	default:
		return kind
	}
}

func init() {
	routineWatchCmd.Flags().Bool("json", false, "emit JSON Lines instead of human-readable text")
	routineWatchCmd.Flags().Bool("once", false, "exit after the first run completes (CI-friendly)")
	routineWatchCmd.Flags().String("run-id", "", "filter to events for one run_id only")
	routineWatchCmd.Flags().Duration("interval", 2*time.Second, "poll interval (default 2s; min 500ms recommended)")
	routineWatchCmd.Flags().Duration("since", 5*time.Minute, "lookback window on first poll (currently informational; full window per page)")
	pipelineCmd.AddCommand(routineWatchCmd)
}
