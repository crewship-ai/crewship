package main

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// journalCmd is the CLI surface over the Crew Journal read API. Kept
// deliberately narrow — list + live tail. Anything richer (summaries,
// per-agent history, checkpoint fork) lives on the web UI where
// interaction is richer than a terminal can express.
var journalCmd = &cobra.Command{
	Use:   "journal",
	Short: "View the Crew Journal event stream",
	Long: `Read the Crew Journal — the canonical append-only event stream for every
observable action in the platform. Filter by crew, agent, mission, entry
type, severity, or time window.

Examples:
  crewship journal
  crewship journal --crew backend-team --since 24h
  crewship journal --severity warn,error
  crewship journal --type peer.escalation,keeper.decision --lines 100
  crewship journal --follow                  # live tail via SSE
  crewship journal --mission MIS-42 --format json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()

		lines, _ := cmd.Flags().GetInt("lines")
		crewFlag, _ := cmd.Flags().GetString("crew")
		agentID, _ := cmd.Flags().GetString("agent")
		missionID, _ := cmd.Flags().GetString("mission")
		typeFilter, _ := cmd.Flags().GetString("type")
		severityFilter, _ := cmd.Flags().GetString("severity")
		since, _ := cmd.Flags().GetString("since")
		follow, _ := cmd.Flags().GetBool("follow")

		q := url.Values{}
		q.Set("limit", fmt.Sprintf("%d", lines))
		if crewFlag != "" {
			crewID, err := resolveCrewID(client, crewFlag)
			if err != nil {
				return err
			}
			q.Set("crew_id", crewID)
		}
		if agentID != "" {
			q.Set("agent_id", agentID)
		}
		if missionID != "" {
			q.Set("mission_id", missionID)
		}
		if typeFilter != "" {
			q.Set("entry_type", typeFilter)
		}
		if severityFilter != "" {
			q.Set("severity", severityFilter)
		}
		if since != "" {
			sinceTime, err := parseSince(since)
			if err != nil {
				return fmt.Errorf("bad --since: %w", err)
			}
			q.Set("since", sinceTime.Format(time.RFC3339))
		}

		if follow {
			return fmt.Errorf("--follow requires SSE client support; use the web UI /journal for live view until the CLI SSE tail lands")
		}

		path := "/api/v1/journal?" + q.Encode()
		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var body struct {
			Entries []map[string]any `json:"entries"`
			Count   int              `json:"count"`
		}
		if err := cli.ReadJSON(resp, &body); err != nil {
			return err
		}

		f := newFormatter()
		if f.Format == "json" {
			return f.JSON(body.Entries)
		}
		if f.Format == "yaml" {
			return f.YAML(body.Entries)
		}

		// Text formatter: ts | severity-chip | scope | summary
		for _, e := range body.Entries {
			ts, _ := e["ts"].(string)
			if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
				ts = t.Format("2006-01-02 15:04:05")
			}
			entryType, _ := e["entry_type"].(string)
			severity, _ := e["severity"].(string)
			summary, _ := e["summary"].(string)
			actor, _ := e["actor_type"].(string)

			color := cli.Gray
			switch severity {
			case "warn":
				color = cli.Yellow
			case "error":
				color = cli.Red
			case "notice":
				color = cli.Cyan
			}

			fmt.Printf("%s%s%s  %s[%-8s]%s  %s%-22s%s  %s%-10s%s  %s\n",
				cli.Dim, ts, cli.Reset,
				color, severity, cli.Reset,
				cli.Bold, truncateString(entryType, 22), cli.Reset,
				cli.Dim, truncateString(actor, 10), cli.Reset,
				summary)
		}
		return nil
	},
}

// parseSince accepts a duration suffix (1h, 24h, 7d) or an RFC3339
// timestamp. Returns the absolute timestamp the API expects.
func parseSince(s string) (time.Time, error) {
	now := time.Now().UTC()
	// Try duration first.
	if strings.HasSuffix(s, "d") {
		days, err := time.ParseDuration(strings.TrimSuffix(s, "d") + "h")
		if err == nil {
			return now.Add(-days * 24), nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil {
		return now.Add(-d), nil
	}
	// Fall through to RFC3339.
	return time.Parse(time.RFC3339, s)
}

func truncateString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func init() {
	journalCmd.Flags().Int("lines", 50, "Max entries to fetch")
	journalCmd.Flags().String("crew", "", "Filter by crew slug or ID")
	journalCmd.Flags().String("agent", "", "Filter by agent ID")
	journalCmd.Flags().String("mission", "", "Filter by mission ID")
	journalCmd.Flags().String("type", "", "Comma-separated entry types (peer.conversation,keeper.decision,...)")
	journalCmd.Flags().String("severity", "", "Comma-separated severities (info,notice,warn,error)")
	journalCmd.Flags().String("since", "", "Time window (1h, 24h, 7d, or RFC3339)")
	journalCmd.Flags().Bool("follow", false, "Live tail via SSE (not yet implemented in CLI — use web UI)")
}
