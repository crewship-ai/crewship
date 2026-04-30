package main

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// recallCmd is a free-text search across the Crew Journal — the canonical
// workspace memory of past events. It hits the journal `?q=` FTS5 endpoint
// and renders results in a snippet form optimised for "did we discuss / try
// / decide X before?" lookups.
//
// Why it's separate from `crewship journal`:
//   - `journal` is for filtered tailing/auditing (by type, severity, time).
//   - `recall` is for question-answering ("how was the auth fix shipped?").
//
// The two share the underlying endpoint but differ in defaults, presentation,
// and the user's mental model. Forcing one command to do both lost the plot.
var recallCmd = &cobra.Command{
	Use:   "recall <query>",
	Short: "Search the Crew Journal by free text",
	Long: `Free-text search across the Crew Journal. Returns matched entries
with timestamp, type, summary, and a body snippet.

Examples:
  crewship recall "auth migration"
  crewship recall "rate limit" --since 30d --limit 30
  crewship recall "keeper denied" --crew backend-team
  crewship recall "deploy" --format json | jq '.[].summary'`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		query := strings.Join(args, " ")
		// Server caps q at 200 chars — fail fast with a friendly message
		// instead of letting the API return a 400.
		if len(query) > 200 {
			return fmt.Errorf("query too long: %d chars (max 200)", len(query))
		}

		client := newAPIClient()

		limit, _ := cmd.Flags().GetInt("limit")
		since, _ := cmd.Flags().GetString("since")
		crewFlag, _ := cmd.Flags().GetString("crew")
		agentID, _ := cmd.Flags().GetString("agent")

		q := url.Values{}
		q.Set("q", query)
		q.Set("limit", fmt.Sprintf("%d", limit))
		if since != "" {
			t, err := parseSince(since)
			if err != nil {
				return fmt.Errorf("bad --since: %w", err)
			}
			q.Set("since", t.Format(time.RFC3339))
		}
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

		resp, err := client.Get("/api/v1/journal?" + q.Encode())
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

		if len(body.Entries) == 0 {
			fmt.Printf("%sNo matches.%s\n", cli.Dim, cli.Reset)
			return nil
		}

		fmt.Printf("%s%d match%s for %q%s\n\n",
			cli.Bold, len(body.Entries), pluralize(len(body.Entries)), query, cli.Reset)
		for _, e := range body.Entries {
			printRecallEntry(e, query)
		}
		return nil
	},
}

func pluralize(n int) string {
	if n == 1 {
		return ""
	}
	return "es"
}

// printRecallEntry renders one journal hit as a snippet card.
//
//	2026-04-30 10:13  [peer.escalation]  backend-team / viktor
//	  agent escalated to lead — repeated DB lock timeout
//	  body: "...lock contention on credentials table during reload..."
func printRecallEntry(e map[string]any, query string) {
	ts, _ := e["ts"].(string)
	if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
		ts = t.Format("2006-01-02 15:04")
	}
	entryType, _ := e["entry_type"].(string)
	summary, _ := e["summary"].(string)
	severity, _ := e["severity"].(string)
	crew, _ := e["crew_id"].(string)
	agent, _ := e["agent_id"].(string)

	scope := ""
	switch {
	case crew != "" && agent != "":
		scope = crew + " / " + agent
	case crew != "":
		scope = crew
	case agent != "":
		scope = agent
	}

	color := cli.Gray
	switch severity {
	case "warn":
		color = cli.Yellow
	case "error":
		color = cli.Red
	case "notice":
		color = cli.Cyan
	}

	fmt.Printf("%s%s%s  %s[%s]%s  %s%s%s\n",
		cli.Dim, ts, cli.Reset,
		color, truncateString(entryType, 22), cli.Reset,
		cli.Dim, scope, cli.Reset)
	fmt.Printf("  %s\n", highlightQuery(summary, query))

	if body, ok := e["body"]; ok {
		if snippet := bodySnippet(body, query, 180); snippet != "" {
			fmt.Printf("  %s%s%s\n", cli.Dim, snippet, cli.Reset)
		}
	}
	fmt.Println()
}

// bodySnippet renders a short excerpt of the journal entry body, biased
// toward the position where the search query first appears so the operator
// sees the relevant text rather than always the first N bytes.
func bodySnippet(body any, query string, maxLen int) string {
	var s string
	switch v := body.(type) {
	case string:
		s = v
	case map[string]any:
		// Pick a likely-meaningful field if present.
		for _, key := range []string{"text", "content", "message", "summary"} {
			if val, ok := v[key].(string); ok && val != "" {
				s = val
				break
			}
		}
	}
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)

	// Window around first match (case-insensitive); fall back to head.
	if query != "" {
		if idx := strings.Index(strings.ToLower(s), strings.ToLower(query)); idx > maxLen/2 {
			start := idx - maxLen/2
			s = "..." + s[start:]
		}
	}
	if len(s) > maxLen {
		s = s[:maxLen] + "..."
	}
	return highlightQuery(s, query)
}

// highlightQuery wraps case-insensitive matches of `q` in s with bold ANSI.
// No regex — case-insensitive substring scan keeps it cheap and predictable.
func highlightQuery(s, q string) string {
	if q == "" || s == "" {
		return s
	}
	lowerS := strings.ToLower(s)
	lowerQ := strings.ToLower(q)
	if !strings.Contains(lowerS, lowerQ) {
		return s
	}
	var out strings.Builder
	i := 0
	for i < len(s) {
		idx := strings.Index(strings.ToLower(s[i:]), lowerQ)
		if idx < 0 {
			out.WriteString(s[i:])
			break
		}
		out.WriteString(s[i : i+idx])
		out.WriteString(cli.Bold)
		out.WriteString(s[i+idx : i+idx+len(q)])
		out.WriteString(cli.Reset)
		i = i + idx + len(q)
	}
	return out.String()
}

func init() {
	recallCmd.Flags().Int("limit", 20, "Max matches to return")
	recallCmd.Flags().String("since", "", "Time window (1h, 24h, 7d, or RFC3339)")
	recallCmd.Flags().String("crew", "", "Filter by crew slug or ID")
	recallCmd.Flags().String("agent", "", "Filter by agent ID")
}
