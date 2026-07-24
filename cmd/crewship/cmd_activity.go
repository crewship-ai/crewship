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
// cross-crew "activity" view: peer queries, escalations, and the full
// assignment lifecycle (created → running → completed/failed). This is the
// journal-native replacement for the retired /api/v1/activity aggregator
// (which merged three tables server-side and silently dropped a whole
// source on any query error). The terminal rows matter: without
// assignment.completed / assignment.failed a user watching the feed never
// saw an assignment finish or fail. Terminal run-tracking still lives on
// the parallel run.completed/run.failed entries (keyed by trace_id), which
// this feed deliberately excludes so it stays scoped to assignments rather
// than every routine/pipeline run.
const activityEntryTypes = "peer.conversation,peer.escalation,assignment.created,assignment.running,assignment.completed,assignment.failed"

// activityMaxLines mirrors the journal List handler's server-side page cap
// (internal/api/journal_handler.go: limit must be 1..500). Kept in sync so
// the CLI rejects an out-of-range --lines locally with the same bound
// instead of round-tripping to a 400.
const activityMaxLines = 500

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

The full assignment lifecycle (created → running → completed/failed) shows
here. For the finer per-run trace (LLM calls, exec, egress) use ` + "`crewship journal`" + `.`,
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

		// Hard-reject an out-of-range page size rather than silently
		// clamping it. The journal List handler enforces the same 1..500
		// bound server-side (and 400s), so a clamp here would only paper
		// over a mistake — a user who asks for 1000 rows and silently gets
		// 500 has been lied to about what they received. Fail loud, local.
		if lines < 1 || lines > activityMaxLines {
			return fmt.Errorf("--lines must be between 1 and %d (got %d)", activityMaxLines, lines)
		}

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
		entries := dedupeActivity(body.Entries)

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

		// The "from"/"to" columns resolve agent ids to slugs against the
		// workspace lookup table; only the export renderers surface those
		// columns, so skip the fetch for the default table view.
		var agents map[string]string
		if exportFmt != "" {
			agents = fetchAgentSlugs(client)
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
					// Enrich each raw entry with the resolved participant
					// slugs so an incident-review pipeline gets the "from"
					// without re-implementing the actor_id→slug lookup.
					if err := enc.Encode(e.export(agents)); err != nil {
						return fmt.Errorf("ndjson encode: %w", err)
					}
				}
			case "csv":
				w := csv.NewWriter(out)
				if err := w.Write([]string{"ts", "entry_type", "from_slug", "to_slug", "summary"}); err != nil {
					return fmt.Errorf("csv header: %w", err)
				}
				for _, e := range entries {
					if err := w.Write([]string{e.TS, e.EntryType, e.fromSlug(agents), e.toSlug(agents), e.Summary}); err != nil {
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
// journal stores ids (actor_id / agent_id) and, for some entry types, slugs
// in the payload — never the joined display names the old /activity endpoint
// synthesised. The "from" column is therefore resolved client-side: an
// assignment entry carries the assigner only as actor_id, so the CLI maps it
// to a slug via the /api/v1/journal/lookup reference table (see fromSlug).
type activityRow struct {
	ID        string         `json:"id" yaml:"id"`
	TS        string         `json:"ts" yaml:"ts"`
	EntryType string         `json:"entry_type" yaml:"entry_type"`
	Severity  string         `json:"severity" yaml:"severity"`
	Summary   string         `json:"summary" yaml:"summary"`
	CrewID    string         `json:"crew_id,omitempty" yaml:"crew_id,omitempty"`
	AgentID   string         `json:"agent_id,omitempty" yaml:"agent_id,omitempty"`
	ActorID   string         `json:"actor_id,omitempty" yaml:"actor_id,omitempty"`
	Payload   map[string]any `json:"payload,omitempty" yaml:"payload,omitempty"`
}

// fromSlug resolves the "from" participant. Peer/escalation payloads carry
// an explicit from_slug; assignments carry the assigner only as actor_id, so
// we fall back to the agents lookup (actor_id, then agent_id). Mirrors the
// dashboard's journalEntriesToFeedRows resolution so both surfaces agree.
func (e activityRow) fromSlug(agents map[string]string) string {
	if s := payloadString(e.Payload, "from_slug"); s != "" {
		return s
	}
	if s := agents[e.ActorID]; e.ActorID != "" && s != "" {
		return s
	}
	return agents[e.AgentID]
}

// toSlug resolves the "to" participant: an explicit target_slug in the
// payload, else the agents lookup keyed by target_id.
func (e activityRow) toSlug(agents map[string]string) string {
	if s := payloadString(e.Payload, "target_slug"); s != "" {
		return s
	}
	return agents[payloadString(e.Payload, "target_id")]
}

func payloadString(p map[string]any, key string) string {
	if p == nil {
		return ""
	}
	s, _ := p[key].(string)
	return s
}

// activityExport is the shape written to NDJSON: the raw journal entry plus
// the resolved participant slugs, so a downstream consumer gets the "from"
// column without re-doing the actor_id→slug lookup the CLI already did.
type activityExport struct {
	activityRow
	FromSlug string `json:"from_slug"`
	ToSlug   string `json:"to_slug"`
}

func (e activityRow) export(agents map[string]string) activityExport {
	return activityExport{activityRow: e, FromSlug: e.fromSlug(agents), ToSlug: e.toSlug(agents)}
}

// dedupeActivity collapses duplicate-event noise in the feed. The journal is
// an append-only event log, so a single peer query lands as TWO
// peer.conversation rows (the question, then the answer) sharing one
// peer_conversation_id — rendering both reads as the same conversation
// appearing twice. We keep the first row seen for a given conversation
// (entries arrive newest-first, so that is the answer once it exists, else
// the still-open question) and drop the rest. Every other entry type has a
// unique identity and is keyed by its own id, so a stray repeated row (e.g.
// a stream replay after reconnect) is dropped too, while the distinct
// assignment lifecycle rows (created/running/completed) all survive.
func dedupeActivity(entries []activityRow) []activityRow {
	seen := make(map[string]struct{}, len(entries))
	kept := entries[:0]
	for _, e := range entries {
		key := activityDedupKey(e)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		kept = append(kept, e)
	}
	return kept
}

func activityDedupKey(e activityRow) string {
	if e.EntryType == "peer.conversation" {
		if cid := payloadString(e.Payload, "thread_id"); cid != "" {
			return "peer.conversation:" + cid
		}
	}
	return "id:" + e.ID
}

// fetchAgentSlugs loads the workspace's agent id→slug reference table from
// GET /api/v1/journal/lookup — the same denormalised table the dashboard's
// JournalLookupProvider consumes. Best-effort: a lookup failure returns an
// empty map so the feed still renders (participant columns just fall back to
// whatever the payload carried, or blank), rather than failing the command.
func fetchAgentSlugs(client *cli.Client) map[string]string {
	agents := map[string]string{}
	resp, err := client.Get("/api/v1/journal/lookup")
	if err != nil {
		return agents
	}
	if err := cli.CheckError(resp); err != nil {
		return agents
	}
	var body struct {
		Agents []struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
		} `json:"agents"`
	}
	if err := cli.ReadJSON(resp, &body); err != nil {
		return agents
	}
	for _, a := range body.Agents {
		if a.ID != "" && a.Slug != "" {
			agents[a.ID] = a.Slug
		}
	}
	return agents
}

// activityTypeColor picks a colour by entry-type family so the feed scans
// the same way the old type-coloured column did.
func activityTypeColor(entryType string) string {
	switch {
	case entryType == "assignment.completed":
		return cli.Green
	case entryType == "assignment.failed", entryType == "peer.escalation":
		return cli.Red
	case strings.HasPrefix(entryType, "assignment."):
		return cli.Blue
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
