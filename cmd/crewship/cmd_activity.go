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

// activityEntryTypes is the set of journal entry types that make up the
// cross-crew "activity" view: peer queries, escalations, and assignment
// starts. This is the journal-native replacement for the retired
// /api/v1/activity aggregator (which merged three tables server-side and
// silently dropped a whole source on any query error). Assignment TERMINAL
// state is logged as run.completed/run.failed (with an assignment_id ref),
// not assignment.*, so it is intentionally out of scope here — the
// created/running rows plus their summaries cover the "what's happening"
// glance without pulling in every pipeline run.
const activityEntryTypes = "peer.conversation,peer.escalation,assignment.created,assignment.running"

// activityCmd surfaces the cross-crew activity feed, now sourced from the
// journal (`GET /api/v1/journal`) rather than the deleted /api/v1/activity
// endpoint. The journal is the canonical event stream, so this is an event
// feed (one row per event) rather than the old entity snapshot (one row per
// assignment/conversation/escalation). Filters map straight onto journal
// query params:
//
//	--crew   → crew_id (server-side)
//	--lines  → limit
//	--since  → since= (now SERVER-SIDE; was client-side against /activity)
//	--type   → client-side substring filter on entry_type (e.g. "escalation"
//	           matches peer.escalation, "assignment" matches assignment.*)
//	--export NDJSON / CSV dump of the current page for incident handoffs
//
// For a live tail use `crewship journal --follow`, which streams the same
// journal at finer granularity.
var activityCmd = &cobra.Command{
	Use:   "activity",
	Short: "View activity feed across all crews",
	Long: `View the cross-crew activity feed — agent assignments, peer
conversations, and escalations — sourced from the journal.

Flags:
  --crew <slug-or-id>   Narrow to a single crew (server-side filter)
  --lines <n>           Page size (server caps at 500)
  --type <substring>    Client-side filter by entry type (e.g. escalation)
  --since <window>      Server-side time filter (1h, 24h, 7d, or RFC3339)
  --export ndjson|csv   Dump the current page as NDJSON or CSV
  --out <path>          Write export to file (default: stdout)

Examples:
  crewship activity
  crewship activity --crew backend-team --lines 100
  crewship activity --type escalation --since 24h
  crewship activity --export ndjson --out activity.ndjson

For a live tail of granular events, use:
  crewship journal --follow

Assignment completion/failure is recorded as run.completed / run.failed in
the journal (not assignment.*); use ` + "`crewship journal`" + ` for those.`,
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

		q := url.Values{}
		q.Set("limit", fmt.Sprintf("%d", lines))
		q.Set("entry_type", activityEntryTypes)
		if sinceStr != "" {
			t, err := parseSince(sinceStr)
			if err != nil {
				return fmt.Errorf("bad --since: %w", err)
			}
			// Server-side filter — the journal List handler honours since=.
			q.Set("since", t.UTC().Format(time.RFC3339))
		}
		if crewFilter != "" {
			crewID, err := resolveCrewID(client, crewFilter)
			if err != nil {
				return err
			}
			q.Set("crew_id", crewID)
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
			Entries []activityRow `json:"entries"`
		}
		if err := cli.ReadJSON(resp, &body); err != nil {
			return err
		}
		entries := body.Entries

		// Client-side type narrowing: a substring match on entry_type keeps
		// the familiar `--type escalation` / `--type assignment` UX even
		// though the server already scoped the feed to the activity types.
		if typeFilter != "" {
			needle := strings.ToLower(typeFilter)
			kept := entries[:0]
			for _, e := range entries {
				if strings.Contains(strings.ToLower(e.EntryType), needle) {
					kept = append(kept, e)
				}
			}
			entries = kept
		}

		// Export path runs before the normal renderers so --export wins over
		// --format. NDJSON for incident-review pipelines, CSV for spreadsheets.
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
				for _, e := range entries {
					if err := enc.Encode(e); err != nil {
						return fmt.Errorf("ndjson encode: %w", err)
					}
				}
			case "csv":
				w := csv.NewWriter(out)
				if err := w.Write([]string{"ts", "entry_type", "from_slug", "to_slug", "summary"}); err != nil {
					return fmt.Errorf("csv header: %w", err)
				}
				for _, e := range entries {
					if err := w.Write([]string{e.TS, e.EntryType, e.fromSlug(), e.toSlug(), e.Summary}); err != nil {
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
				cli.PrintSuccess(fmt.Sprintf("Exported %d activities → %s", len(entries), outPath))
			}
			return nil
		}

		f := newFormatter()
		return f.AutoHuman(entries, func() {
			for _, e := range entries {
				ts := e.TS
				if t, err := time.Parse(time.RFC3339Nano, e.TS); err == nil {
					// Explicit UTC marker — a zone-less timestamp is ambiguous
					// the moment the reader sits in a different timezone than
					// the server. UTC keeps output deterministic across machines.
					ts = t.UTC().Format("2006-01-02 15:04:05 UTC")
				}
				fmt.Printf("%s%s%s  %s[%-22s]%s  %s\n",
					cli.Dim, ts, cli.Reset,
					activityTypeColor(e.EntryType), e.EntryType, cli.Reset,
					e.Summary)
			}
		})
	},
}

// activityRow is one journal entry as the activity feed consumes it. The
// participant slugs live in the entry payload (from_slug / target_slug),
// not as top-level columns — the journal stores ids + slugs, never the
// joined display names the old /activity endpoint synthesised.
type activityRow struct {
	ID        string         `json:"id" yaml:"id"`
	TS        string         `json:"ts" yaml:"ts"`
	EntryType string         `json:"entry_type" yaml:"entry_type"`
	Severity  string         `json:"severity" yaml:"severity"`
	Summary   string         `json:"summary" yaml:"summary"`
	CrewID    string         `json:"crew_id,omitempty" yaml:"crew_id,omitempty"`
	AgentID   string         `json:"agent_id,omitempty" yaml:"agent_id,omitempty"`
	Payload   map[string]any `json:"payload,omitempty" yaml:"payload,omitempty"`
}

func (e activityRow) fromSlug() string { return payloadString(e.Payload, "from_slug") }

func (e activityRow) toSlug() string { return payloadString(e.Payload, "target_slug") }

func payloadString(p map[string]any, key string) string {
	if p == nil {
		return ""
	}
	s, _ := p[key].(string)
	return s
}

// activityTypeColor picks a colour by entry-type family so the feed scans
// the same way the old type-coloured column did.
func activityTypeColor(entryType string) string {
	switch {
	case strings.HasPrefix(entryType, "assignment."):
		return cli.Blue
	case entryType == "peer.escalation":
		return cli.Red
	case entryType == "peer.conversation":
		return cli.Cyan
	default:
		return cli.Gray
	}
}

func init() {
	activityCmd.Flags().String("crew", "", "Filter by crew slug or ID")
	activityCmd.Flags().Int("lines", 50, "Number of activity entries")
	activityCmd.Flags().String("type", "", "Client-side filter by entry type substring")
	activityCmd.Flags().String("since", "", "Server-side time filter (1h, 24h, 7d, or RFC3339)")
	activityCmd.Flags().String("export", "", "Export current page: ndjson|csv")
	activityCmd.Flags().String("out", "", "Output file for --export (default: stdout)")
}
