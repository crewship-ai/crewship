package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// validPriorities mirrors journal.ValidPriority — duplicated here so the
// CLI can validate user input before round-tripping to the server. The
// API would also reject a bad value, but a clear error message at the
// edge beats a generic 400 surfaced through HTTP.
var validPriorities = map[string]struct{}{
	"normal": {}, "high": {}, "pin": {}, "permanent": {},
}

// validActorTypes mirrors journal.ActorType. Same rationale — fail fast
// in the CLI rather than after a network round-trip.
var validActorTypes = map[string]struct{}{
	"agent": {}, "user": {}, "system": {}, "keeper": {}, "sidecar": {}, "orchestrator": {},
}

// validSeverities mirrors journal.Severity. Cosmetic guard so a typo
// doesn't silently filter to nothing.
var validSeverities = map[string]struct{}{
	"info": {}, "notice": {}, "warn": {}, "error": {},
}

// validateCSV parses a comma-separated list and rejects values that
// aren't in `allowed`. The error message lists the offender plus the
// allowed set so users can self-correct without consulting docs.
func validateCSV(label, raw string, allowed map[string]struct{}) error {
	if raw == "" {
		return nil
	}
	for _, s := range strings.Split(raw, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := allowed[s]; !ok {
			keys := make([]string, 0, len(allowed))
			for k := range allowed {
				keys = append(keys, k)
			}
			return fmt.Errorf("invalid --%s value %q (allowed: %s)", label, s, strings.Join(keys, "|"))
		}
	}
	return nil
}

// journalCmd is the CLI surface over the Crew Journal read API. The
// list view is the top-level command; subcommands cover single-entry
// fetch (`get`), priority annotation (`priority`), and result counts
// (`count`). The live tail flag (`--follow`) shares the polling loop
// with the SSE endpoint so a remote reader keeps pace with new entries
// without the client having to retry timestamps.
var journalCmd = &cobra.Command{
	Use:   "journal",
	Short: "View the Crew Journal event stream",
	Long: `Read the Crew Journal — the canonical append-only event stream for every
observable action in the platform. Filter by crew, agent, mission, entry
type, severity, actor, priority, trace, or time window.

Examples:
  crewship journal
  crewship journal --crew backend-team --since 24h
  crewship journal --severity warn,error
  crewship journal --type peer.escalation,keeper.decision --lines 100
  crewship journal --follow                  # live tail via SSE
  crewship journal --mission MIS-42 --format json
  crewship journal --query "OOMKilled" --since 24h
  crewship journal --priority permanent,high
  crewship journal --trace-id <run-id>       # one run's spans
  crewship journal get j_abc                  # single entry
  crewship journal count --severity error    # total matching count`,
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
		traceID, _ := cmd.Flags().GetString("trace-id")
		typeFilter, _ := cmd.Flags().GetString("type")
		excludeType, _ := cmd.Flags().GetString("exclude-type")
		severityFilter, _ := cmd.Flags().GetString("severity")
		actorFilter, _ := cmd.Flags().GetString("actor-type")
		priorityFilter, _ := cmd.Flags().GetString("priority")
		queryStr, _ := cmd.Flags().GetString("query")
		since, _ := cmd.Flags().GetString("since")
		follow, _ := cmd.Flags().GetBool("follow")

		// Client-side validation for the small enum sets. The server
		// will also reject these but the local message is more useful
		// than "400 invalid parameter".
		if err := validateCSV("severity", severityFilter, validSeverities); err != nil {
			return err
		}
		if err := validateCSV("actor-type", actorFilter, validActorTypes); err != nil {
			return err
		}
		if err := validateCSV("priority", priorityFilter, validPriorities); err != nil {
			return err
		}
		if lines < 1 || lines > 500 {
			return fmt.Errorf("--lines must be between 1 and 500 (got %d)", lines)
		}

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
		if traceID != "" {
			q.Set("trace_id", traceID)
		}
		if typeFilter != "" {
			q.Set("entry_type", typeFilter)
		}
		if excludeType != "" {
			q.Set("exclude_entry_type", excludeType)
		}
		if severityFilter != "" {
			q.Set("severity", severityFilter)
		}
		if actorFilter != "" {
			q.Set("actor_type", actorFilter)
		}
		if priorityFilter != "" {
			q.Set("priority", priorityFilter)
		}
		if queryStr != "" {
			q.Set("q", queryStr)
		}
		if since != "" {
			sinceTime, err := parseSince(since)
			if err != nil {
				return fmt.Errorf("bad --since: %w", err)
			}
			q.Set("since", sinceTime.Format(time.RFC3339))
		}

		if follow {
			return followJournal(client, q)
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
			printJournalEntry(e)
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

// journalPriorityCmd marks a journal entry with one of the four
// priority values. Inspired by OpenClaw Auto-Dream's ⚠️ PERMANENT /
// 🔥 HIGH / 📌 PIN markers — operators annotate entries they want
// surfaced prominently in recall or never compacted. Requires OWNER
// or ADMIN on the caller's workspace.
var journalPriorityCmd = &cobra.Command{
	Use:   "priority <entry-id>",
	Short: "Mark a journal entry with a priority (permanent/high/pin/normal)",
	Long: `Annotate a journal entry with an importance marker.

  permanent — never compacted, extracted to learned rules immediately,
              recall importance floored at 0.95. Use sparingly.
  high      — recall importance boosted to 0.85, normal compaction.
  pin       — snapshot to /crew/shared/.memory/pins.md at next
              consolidate run, recall importance 0.80+.
  normal    — clear any existing marker back to default.

Examples:
  crewship journal priority j_abc --mark permanent --reason "FX compliance rule"
  crewship journal priority j_abc --mark pin --reason "team playbook entry"
  crewship journal priority j_abc --mark normal`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		mark, _ := cmd.Flags().GetString("mark")
		reason, _ := cmd.Flags().GetString("reason")
		if mark == "" {
			return fmt.Errorf("--mark is required")
		}
		if _, ok := validPriorities[mark]; !ok {
			return fmt.Errorf("invalid --mark %q (allowed: normal|high|pin|permanent)", mark)
		}

		client := newAPIClient()
		path := fmt.Sprintf("/api/v1/journal/%s/priority", url.PathEscape(args[0]))
		resp, err := client.Post(path, map[string]string{
			"priority": mark,
			"reason":   reason,
		})
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var out struct {
			ID       string `json:"id"`
			Priority string `json:"priority"`
			Previous string `json:"previous"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		fmt.Printf("Entry %s: priority %s → %s\n", out.ID, out.Previous, out.Priority)
		return nil
	},
}

func init() {
	journalCmd.Flags().Int("lines", 50, "Max entries to fetch (1-500)")
	journalCmd.Flags().String("crew", "", "Filter by crew slug or ID")
	journalCmd.Flags().String("agent", "", "Filter by agent ID")
	journalCmd.Flags().String("mission", "", "Filter by mission ID")
	journalCmd.Flags().String("trace-id", "", "Filter by run/trace ID — narrows to one run's spans")
	journalCmd.Flags().String("type", "", "Comma-separated entry types (peer.conversation,keeper.decision,...)")
	journalCmd.Flags().String("exclude-type", "", "Comma-separated entry types to exclude (NOT IN); useful for hiding container.metrics noise")
	journalCmd.Flags().String("severity", "", "Comma-separated severities (info,notice,warn,error)")
	journalCmd.Flags().String("actor-type", "", "Comma-separated actors (agent,user,system,keeper,sidecar,orchestrator)")
	journalCmd.Flags().String("priority", "", "Comma-separated priorities (normal,high,pin,permanent)")
	journalCmd.Flags().StringP("query", "q", "", "FTS5 free-text search across summary + payload")
	journalCmd.Flags().String("since", "", "Time window (1h, 24h, 7d, or RFC3339)")
	journalCmd.Flags().Bool("follow", false, "Live tail via SSE — Ctrl-C to exit")

	// Count reuses every list filter except --lines/--follow/--cursor.
	// Defining them on the count subcommand directly (rather than
	// inheriting via PersistentFlags on `journal`) keeps the list
	// view's --lines/--follow from polluting `journal count --help`.
	journalCountCmd.Flags().String("crew", "", "Filter by crew slug or ID")
	journalCountCmd.Flags().String("agent", "", "Filter by agent ID")
	journalCountCmd.Flags().String("mission", "", "Filter by mission ID")
	journalCountCmd.Flags().String("trace-id", "", "Filter by run/trace ID")
	journalCountCmd.Flags().String("type", "", "Comma-separated entry types")
	journalCountCmd.Flags().String("exclude-type", "", "Comma-separated entry types to exclude")
	journalCountCmd.Flags().String("severity", "", "Comma-separated severities")
	journalCountCmd.Flags().String("actor-type", "", "Comma-separated actors")
	journalCountCmd.Flags().String("priority", "", "Comma-separated priorities")
	journalCountCmd.Flags().StringP("query", "q", "", "FTS5 free-text search")
	journalCountCmd.Flags().String("since", "", "Time window (1h, 24h, 7d, or RFC3339)")
	journalCountCmd.Flags().String("until", "", "Upper bound (1h, 24h, 7d, or RFC3339)")

	journalPriorityCmd.Flags().String("mark", "", "Priority marker: permanent | high | pin | normal (required)")
	journalPriorityCmd.Flags().String("reason", "", "Short reason recorded alongside the change (shows up in logs)")

	journalCmd.AddCommand(journalGetCmd)
	journalCmd.AddCommand(journalCountCmd)
	journalCmd.AddCommand(journalPriorityCmd)
}

// followJournal opens the SSE stream and prints entries as they arrive,
// reconnecting on transient failure with bounded backoff. Returns when the
// user hits Ctrl-C.
//
// Last-Event-ID is threaded across reconnects so a brief disconnect resumes
// without dropping or duplicating entries — the server replays from the
// last seen ID. Backoff caps at 30 s; permanent errors (auth, 4xx) bail
// out instead of retrying forever.
func followJournal(client *cli.Client, q url.Values) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	path := "/api/v1/journal/stream?" + q.Encode()
	lastID := ""
	backoff := 1 * time.Second
	const maxBackoff = 30 * time.Second

	for {
		err := client.WithContext(ctx).StreamSSE(ctx, path, lastID, func(e cli.SSEEvent) error {
			if e.Comment != "" && e.Data == "" {
				// Heartbeat — ignore.
				return nil
			}
			if e.ID != "" {
				lastID = e.ID
			}
			if e.Data == "" {
				return nil
			}
			var entry map[string]any
			if err := json.Unmarshal([]byte(e.Data), &entry); err != nil {
				return nil // skip malformed
			}
			printJournalEntry(entry)
			return nil
		})

		if ctx.Err() != nil {
			return nil // user-initiated exit
		}
		if err == nil {
			// Server closed cleanly — try to resume.
			err = fmt.Errorf("stream closed")
		}
		if isPermanentSSEError(err) {
			return err
		}

		fmt.Fprintf(os.Stderr, "%s[reconnecting in %s — %v]%s\n",
			cli.Dim, backoff, err, cli.Reset)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return nil
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// isPermanentSSEError returns true for errors that won't be fixed by
// reconnecting (auth failure, 4xx responses, malformed URL) — distinguishing
// these from transient network blips matters because retrying them in a
// hot loop spams the server log with noise.
func isPermanentSSEError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "status 401") ||
		strings.Contains(msg, "status 403") ||
		strings.Contains(msg, "status 404") ||
		strings.Contains(msg, "parse URL")
}

// printJournalEntry renders one journal entry as a single line in the
// same format the list view uses, so a `--follow` stream and a one-shot
// `crewship journal` listing render identically. Severity colour-maps
// to the chip (warn=yellow, error=red, notice=cyan, default=gray).
//
// Shared between the list view, the SSE reader, and the `get`
// subcommand — keeping a single formatter means every UI surface stays
// in sync when the format changes.
func printJournalEntry(e map[string]any) {
	ts, _ := e["ts"].(string)
	if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
		ts = t.Format("2006-01-02 15:04:05")
	}
	entryType, _ := e["entry_type"].(string)
	severity, _ := e["severity"].(string)
	summary, _ := e["summary"].(string)
	actor, _ := e["actor_type"].(string)

	color := severityColor(severity)

	fmt.Printf("%s%s%s  %s[%-8s]%s  %s%-22s%s  %s%-10s%s  %s\n",
		cli.Dim, ts, cli.Reset,
		color, severity, cli.Reset,
		cli.Bold, truncateString(entryType, 22), cli.Reset,
		cli.Dim, truncateString(actor, 10), cli.Reset,
		summary)
}

// severityColor maps a severity string to the cli colour token used by
// printJournalEntry. Extracted so the colour table lives in one place
// — historically two copies drifted apart and the `--follow` view
// rendered "notice" rows in a different colour from the list view.
func severityColor(severity string) string {
	switch severity {
	case "warn":
		return cli.Yellow
	case "error":
		return cli.Red
	case "notice":
		return cli.Cyan
	default:
		return cli.Gray
	}
}

// journalGetCmd fetches a single entry by ID. Useful for deep-link
// debugging — paste the ID surfaced by another tool (Slack, Linear, the
// web UI's anchor link) and dump the full entry without scrolling.
var journalGetCmd = &cobra.Command{
	Use:   "get <entry-id>",
	Short: "Fetch a single journal entry by ID",
	Long: `Fetch one journal entry by its stable ID. Output respects --format
(text|json|yaml). The entry is workspace-scoped — IDs from other
workspaces return 404.

Examples:
  crewship journal get j_a1b2c3d4
  crewship journal get j_a1b2c3d4 --format json | jq .payload`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		path := fmt.Sprintf("/api/v1/journal/%s", url.PathEscape(args[0]))
		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var entry map[string]any
		if err := cli.ReadJSON(resp, &entry); err != nil {
			return err
		}
		f := newFormatter()
		if f.Format == "json" {
			return f.JSON(entry)
		}
		if f.Format == "yaml" {
			return f.YAML(entry)
		}
		printJournalEntry(entry)
		// Indent additional context (payload + refs) under the line so
		// the per-line summary stays scannable in a terminal. Other
		// formats already include them in the structured output.
		if payload, ok := entry["payload"].(map[string]any); ok && len(payload) > 0 {
			b, _ := json.MarshalIndent(payload, "  ", "  ")
			fmt.Printf("  %spayload%s %s\n", cli.Dim, cli.Reset, string(b))
		}
		if refs, ok := entry["refs"].(map[string]any); ok && len(refs) > 0 {
			b, _ := json.MarshalIndent(refs, "  ", "  ")
			fmt.Printf("  %srefs%s    %s\n", cli.Dim, cli.Reset, string(b))
		}
		return nil
	},
}

// journalCountCmd prints the total count of entries matching filters.
// Same flag set as the list view (minus --lines / --follow), shared
// query-builder helpers below. Useful for shell scripting where you
// need a number, not a wall of rows.
var journalCountCmd = &cobra.Command{
	Use:   "count",
	Short: "Print the total count of entries matching filters",
	Long: `Return the total number of journal entries that match the same filters
the list view accepts. Cursor and limit are ignored — the count is
always over the full result set.

Examples:
  crewship journal count
  crewship journal count --severity error --since 24h
  crewship journal count --type budget.exceeded`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()

		q, err := buildCountQuery(cmd, client)
		if err != nil {
			return err
		}
		path := "/api/v1/journal/count"
		if encoded := q.Encode(); encoded != "" {
			path += "?" + encoded
		}
		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var body struct {
			Total int64 `json:"total"`
		}
		if err := cli.ReadJSON(resp, &body); err != nil {
			return err
		}
		f := newFormatter()
		if f.Format == "json" {
			return f.JSON(body)
		}
		if f.Format == "yaml" {
			return f.YAML(body)
		}
		fmt.Println(body.Total)
		return nil
	},
}

// buildCountQuery shares filter parsing between the list view and the
// count subcommand. Mirrors the parseJournalQuery surface on the server,
// kept narrow because the count endpoint has no use for cursor/limit.
func buildCountQuery(cmd *cobra.Command, client *cli.Client) (url.Values, error) {
	crewFlag, _ := cmd.Flags().GetString("crew")
	agentID, _ := cmd.Flags().GetString("agent")
	missionID, _ := cmd.Flags().GetString("mission")
	traceID, _ := cmd.Flags().GetString("trace-id")
	typeFilter, _ := cmd.Flags().GetString("type")
	excludeType, _ := cmd.Flags().GetString("exclude-type")
	severityFilter, _ := cmd.Flags().GetString("severity")
	actorFilter, _ := cmd.Flags().GetString("actor-type")
	priorityFilter, _ := cmd.Flags().GetString("priority")
	queryStr, _ := cmd.Flags().GetString("query")
	since, _ := cmd.Flags().GetString("since")
	until, _ := cmd.Flags().GetString("until")

	if err := validateCSV("severity", severityFilter, validSeverities); err != nil {
		return nil, err
	}
	if err := validateCSV("actor-type", actorFilter, validActorTypes); err != nil {
		return nil, err
	}
	if err := validateCSV("priority", priorityFilter, validPriorities); err != nil {
		return nil, err
	}

	q := url.Values{}
	if crewFlag != "" {
		crewID, err := resolveCrewID(client, crewFlag)
		if err != nil {
			return nil, err
		}
		q.Set("crew_id", crewID)
	}
	if agentID != "" {
		q.Set("agent_id", agentID)
	}
	if missionID != "" {
		q.Set("mission_id", missionID)
	}
	if traceID != "" {
		q.Set("trace_id", traceID)
	}
	if typeFilter != "" {
		q.Set("entry_type", typeFilter)
	}
	if excludeType != "" {
		q.Set("exclude_entry_type", excludeType)
	}
	if severityFilter != "" {
		q.Set("severity", severityFilter)
	}
	if actorFilter != "" {
		q.Set("actor_type", actorFilter)
	}
	if priorityFilter != "" {
		q.Set("priority", priorityFilter)
	}
	if queryStr != "" {
		q.Set("q", queryStr)
	}
	if since != "" {
		t, err := parseSince(since)
		if err != nil {
			return nil, fmt.Errorf("bad --since: %w", err)
		}
		q.Set("since", t.Format(time.RFC3339))
	}
	if until != "" {
		t, err := parseSince(until)
		if err != nil {
			return nil, fmt.Errorf("bad --until: %w", err)
		}
		q.Set("until", t.Format(time.RFC3339))
	}
	return q, nil
}
