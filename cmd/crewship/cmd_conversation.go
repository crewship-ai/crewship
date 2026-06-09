package main

import (
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// conversationCmd groups conversation-history operations. Today it has a
// single subcommand, `search`, backed by POST /api/v1/conversations/search.
//
// Distinct from `crewship recall` (which searches the Crew Journal — the
// workspace-wide event memory): conversation search is scoped to ONE agent's
// chat sessions and answers "what did this agent and I discuss before?".
// The two surfaces share the FTS5 substrate but have different scopes and
// mental models, so they stay separate commands.
var conversationCmd = &cobra.Command{
	Use:     "conversation",
	Aliases: []string{"conv"},
	Short:   "Search and inspect agent conversation history",
	Long: `Operate on agent chat conversation history.

Subcommands:
  search   Keyword (BM25) search across an agent's past chat sessions`,
}

// conversationSearchCmd drives POST /api/v1/conversations/search. The agent
// is resolved from a slug or ID; the server verifies it belongs to the
// caller's workspace before the agent-scoped search runs.
var conversationSearchCmd = &cobra.Command{
	Use:   "search <agent-slug-or-id> <query>",
	Short: "Search an agent's past conversations by keyword",
	Long: `Keyword search across one agent's recorded chat sessions, ranked
by BM25 relevance. Returns matched messages with their session id and
timestamp so you can follow up.

Search is from-now-on: only conversations recorded after the feature
shipped are indexed (there is no backfill of older history).

Examples:
  crewship conversation search backend-bot "deploy pipeline"
  crewship conversation search agent_123 "rate limit" --limit 50
  crewship conversation search backend-bot "auth" --format json`,
	Args: cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		agentID, err := resolveAgentID(client, args[0])
		if err != nil {
			return err
		}

		query := strings.Join(args[1:], " ")
		limit, _ := cmd.Flags().GetInt("limit")

		resp, err := client.Post("/api/v1/conversations/search", map[string]any{
			"agent_id": agentID,
			"query":    query,
			"limit":    limit,
		})
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var result struct {
			Count int    `json:"count"`
			Query string `json:"query"`
			Hits  []struct {
				ID          string `json:"id"`
				SessionID   string `json:"session_id"`
				AgentID     string `json:"agent_id"`
				Role        string `json:"role"`
				Content     string `json:"content"`
				ToolSummary string `json:"tool_summary"`
				Timestamp   string `json:"ts"`
			} `json:"hits"`
		}
		if err := cli.ReadJSON(resp, &result); err != nil {
			return err
		}

		f := newFormatter()
		if f.Format == "json" {
			return f.JSON(result)
		}
		if f.Format == "yaml" {
			return f.YAML(result)
		}

		if result.Count == 0 {
			fmt.Printf("No conversation matches for %q.\n", query)
			return nil
		}

		fmt.Printf("%s%d match(es) for %q%s\n\n", cli.Bold, result.Count, query, cli.Reset)
		for _, h := range result.Hits {
			snippet := h.Content
			if snippet == "" {
				snippet = h.ToolSummary
			}
			snippet = strings.ReplaceAll(snippet, "\n", " ")
			if len([]rune(snippet)) > 160 {
				snippet = string([]rune(snippet)[:157]) + "..."
			}
			fmt.Printf("%s%s%s  %s[%s]%s  session=%s\n  %s\n\n",
				cli.Dim, h.Timestamp, cli.Reset,
				cli.Cyan, h.Role, cli.Reset,
				h.SessionID,
				snippet)
		}
		return nil
	},
}

func init() {
	conversationSearchCmd.Flags().Int("limit", 20, "Maximum number of hits (1-100)")
	conversationCmd.AddCommand(conversationSearchCmd)
}
