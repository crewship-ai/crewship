package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// activityCmd surfaces the cross-crew activity feed at /api/v1/activity.
// The server merges three sources (assignments, peer conversations,
// escalations) into one DESC-by-created_at list narrowable by
// agent_id/crew_id and capped by limit/offset. Anything beyond that
// (free-text search, type filter, server-side time window, live SSE)
// is NOT supported server-side today — the CLI does what it can
// client-side after fetching the page.
//
// Specifically:
//
//	--type   client-side filter (case-insensitive substring) — the
//	         server endpoint has no `type=` parameter
//	--since  client-side filter on created_at — server has no since=
//	--export NDJSON / CSV dump of the current page, intended for
//	         incident review handoffs
//
// We deliberately do NOT implement --follow against /activity because
// no stream endpoint exists. The journal SSE stream (`crewship journal
// --follow`) covers the same events at a finer granularity; pointing
// users there in the help text is more honest than pretending to wrap
// a non-existent endpoint.
var activityCmd = &cobra.Command{
	Use:   "activity",
	Short: "View activity feed across all crews",
	Long: `View the cross-crew activity feed including assignments, peer
conversations, and escalations.

Flags:
  --crew <slug-or-id>   Narrow to a single crew (server-side filter)
  --lines <n>           Page size (server caps at 100)
  --type <substring>    Client-side filter by activity type
  --since <window>      Client-side filter (1h, 24h, 7d, or RFC3339)
  --export ndjson|csv   Dump the current page as NDJSON or CSV
  --out <path>          Write export to file (default: stdout)

Examples:
  crewship activity
  crewship activity --crew backend-team --lines 100
  crewship activity --type escalation --since 24h
  crewship activity --export ndjson --out activity.ndjson

For a live tail of granular events, use:
  crewship journal --follow

The /api/v1/activity endpoint has no SSE stream; --follow against
activity intentionally isn't wired.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()

		lines, _ := cmd.Flags().GetInt("lines")
		crewFilter, _ := cmd.Flags().GetString("crew")
		typeFilter, _ := cmd.Flags().GetString("type")
		sinceStr, _ := cmd.Flags().GetString("since")
		exportFmt, _ := cmd.Flags().GetString("export")
		outPath, _ := cmd.Flags().GetString("out")

		var sinceTime time.Time
		var sinceSet bool
		if sinceStr != "" {
			t, err := parseSince(sinceStr)
			if err != nil {
				return fmt.Errorf("bad --since: %w", err)
			}
			sinceTime = t
			sinceSet = true
		}

		q := url.Values{}
		q.Set("limit", fmt.Sprintf("%d", lines))
		if crewFilter != "" {
			crewID, err := resolveCrewID(client, crewFilter)
			if err != nil {
				return err
			}
			q.Set("crew_id", crewID)
		}
		path := "/api/v1/activity?" + q.Encode()

		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var activities []struct {
			Type      string  `json:"type"`
			CrewSlug  string  `json:"crew_slug"`
			Summary   string  `json:"summary"`
			CreatedAt string  `json:"created_at"`
			FromSlug  *string `json:"from_slug"`
			ToSlug    *string `json:"to_slug"`
		}
		if err := cli.ReadJSON(resp, &activities); err != nil {
			return err
		}

		// Client-side narrowing. Cheap on a single page (server caps at
		// 100) and avoids inventing server params for the audit gaps.
		if typeFilter != "" {
			needle := strings.ToLower(typeFilter)
			kept := activities[:0]
			for _, a := range activities {
				if strings.Contains(strings.ToLower(a.Type), needle) {
					kept = append(kept, a)
				}
			}
			activities = kept
		}
		if sinceSet {
			kept := activities[:0]
			for _, a := range activities {
				if t, err := time.Parse(time.RFC3339Nano, a.CreatedAt); err == nil && !t.Before(sinceTime) {
					kept = append(kept, a)
				}
			}
			activities = kept
		}

		// Export path runs before the normal renderers so --export wins
		// over --format. NDJSON / CSV are the two shapes operators ask
		// for: NDJSON for incident-review pipelines, CSV for spreadsheets.
		if exportFmt != "" {
			out := os.Stdout
			if outPath != "" {
				f, err := os.Create(outPath)
				if err != nil {
					return fmt.Errorf("open --out: %w", err)
				}
				defer f.Close()
				out = f
			}
			switch strings.ToLower(exportFmt) {
			case "ndjson":
				enc := json.NewEncoder(out)
				for _, a := range activities {
					if err := enc.Encode(a); err != nil {
						return fmt.Errorf("ndjson encode: %w", err)
					}
				}
			case "csv":
				w := csv.NewWriter(out)
				if err := w.Write([]string{"created_at", "type", "crew_slug", "from_slug", "to_slug", "summary"}); err != nil {
					return fmt.Errorf("csv header: %w", err)
				}
				for _, a := range activities {
					from := ""
					if a.FromSlug != nil {
						from = *a.FromSlug
					}
					to := ""
					if a.ToSlug != nil {
						to = *a.ToSlug
					}
					if err := w.Write([]string{a.CreatedAt, a.Type, a.CrewSlug, from, to, a.Summary}); err != nil {
						return fmt.Errorf("csv row: %w", err)
					}
				}
				w.Flush()
				if err := w.Error(); err != nil {
					return fmt.Errorf("csv flush: %w", err)
				}
			default:
				return fmt.Errorf("--export must be ndjson or csv (got %q)", exportFmt)
			}
			if outPath != "" {
				cli.PrintSuccess(fmt.Sprintf("Exported %d activities → %s", len(activities), outPath))
			}
			return nil
		}

		f := newFormatter()
		if f.Format == "json" || f.Format == "yaml" {
			if f.Format == "json" {
				return f.JSON(activities)
			}
			return f.YAML(activities)
		}

		for _, a := range activities {
			ts := a.CreatedAt
			if t, err := time.Parse(time.RFC3339Nano, a.CreatedAt); err == nil {
				ts = t.Format("2006-01-02 15:04:05")
			}

			typeColor := ""
			switch a.Type {
			case "ASSIGNMENT", "assignment":
				typeColor = cli.Blue
			case "COMPLETED", "completed":
				typeColor = cli.Green
			case "ESCALATION", "escalation":
				typeColor = cli.Red
			case "QUERY", "query", "RESPONSE", "response":
				typeColor = cli.Cyan
			default:
				typeColor = cli.Gray
			}

			fmt.Printf("%s%s%s  %s[%-12s]%s  %s%-10s%s  %s\n",
				cli.Dim, ts, cli.Reset,
				typeColor, a.Type, cli.Reset,
				cli.Bold, a.CrewSlug, cli.Reset,
				a.Summary)
		}

		return nil
	},
}

func init() {
	activityCmd.Flags().String("crew", "", "Filter by crew slug or ID")
	activityCmd.Flags().Int("lines", 50, "Number of activity entries")
	activityCmd.Flags().String("type", "", "Client-side filter by activity type substring")
	activityCmd.Flags().String("since", "", "Client-side filter (1h, 24h, 7d, or RFC3339)")
	activityCmd.Flags().String("export", "", "Export current page: ndjson|csv")
	activityCmd.Flags().String("out", "", "Output file for --export (default: stdout)")
}
