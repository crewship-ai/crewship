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
// workspace-wide event memory): conversation search reads the chat
// transcripts themselves and answers "what did we actually say?".
// The two surfaces share the FTS5 substrate but have different scopes and
// mental models, so they stay separate commands.
var conversationCmd = &cobra.Command{
	Use:     "conversation",
	Aliases: []string{"conv"},
	Short:   "Search and inspect agent conversation history",
	Long: `Operate on agent chat conversation history.

Subcommands:
  search   Keyword (BM25) search across past chat sessions`,
}

// conversationSearchCmd drives POST /api/v1/conversations/search.
//
// The default scope is the whole workspace — the same thing ⌘K asks for,
// and the common case: you remember the sentence, not who said it. --agent
// narrows to one agent, resolved from a slug or ID; either way the server
// derives the workspace from the session and verifies the agent belongs to
// it before searching.
var conversationSearchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search past conversations by keyword",
	Long: `Keyword search across recorded chat sessions, ranked by BM25
relevance. Returns matched messages with the agent that said them, their
session id and timestamp, so you can follow up.

Searches every agent in the workspace unless --agent narrows it.

Search is from-now-on: only conversations recorded after the feature
shipped are indexed (there is no backfill of older history).

Examples:
  crewship conversation search "deploy pipeline"
  crewship conversation search "rate limit" --agent backend-bot --limit 50
  crewship conversation search "auth" --format json`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		agentRef, _ := cmd.Flags().GetString("agent")
		queryArgs := args

		// Backwards compatibility with the agent-first positional form this
		// command shipped with (`search backend-bot "deploy"`). It is only
		// reachable when --agent is absent AND a second argument follows,
		// which is exactly the shape that used to be required — a
		// single-argument call has always been a usage error until now, so
		// nothing that worked before changes meaning.
		legacyPositional := agentRef == "" && len(args) >= 2
		if legacyPositional {
			agentRef = args[0]
			queryArgs = args[1:]
		}

		agentID := ""
		if agentRef != "" {
			resolved, err := resolveAgentID(client, agentRef)
			if err != nil {
				if legacyPositional {
					// The first word was read as an agent because two bare
					// words were passed. Say so, rather than leaving the
					// caller staring at "agent not found: deploy".
					return fmt.Errorf("%w\n(the first argument is read as an agent when several are given — "+
						"quote the phrase to search every agent: crewship conversation search %q)",
						err, strings.Join(args, " "))
				}
				return err
			}
			agentID = resolved
		}

		query := strings.Join(queryArgs, " ")
		limit, _ := cmd.Flags().GetInt("limit")

		body := map[string]any{
			"query": query,
			"limit": limit,
		}
		// Omitted, not empty: an absent agent_id is what selects the
		// workspace scope on the server.
		if agentID != "" {
			body["agent_id"] = agentID
		}

		resp, err := client.Post("/api/v1/conversations/search", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var result struct {
			Count int    `json:"count"`
			Query string `json:"query"`
			Scope string `json:"scope"`
			Hits  []struct {
				ID          string `json:"id"`
				SessionID   string `json:"session_id"`
				AgentID     string `json:"agent_id"`
				AgentSlug   string `json:"agent_slug"`
				AgentName   string `json:"agent_name"`
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
		return f.AutoHuman(result, func() {
			if result.Count == 0 {
				fmt.Printf("No conversation matches for %q.\n", query)
				return
			}

			scope := "this workspace"
			if agentID != "" {
				scope = "one agent"
			}
			fmt.Printf("%s%d match(es) for %q across %s%s\n\n", cli.Bold, result.Count, query, scope, cli.Reset)
			for _, h := range result.Hits {
				snippet := h.Content
				if snippet == "" {
					snippet = h.ToolSummary
				}
				snippet = strings.ReplaceAll(snippet, "\n", " ")
				if len([]rune(snippet)) > 160 {
					snippet = string([]rune(snippet)[:157]) + "..."
				}
				// Who said it matters once the scope is wider than one
				// agent, and costs nothing when it is not.
				who := h.AgentName
				if who == "" {
					who = h.AgentSlug
				}
				if who == "" {
					who = h.AgentID
				}
				fmt.Printf("%s%s%s  %s[%s]%s  %s  session=%s\n  %s\n\n",
					cli.Dim, h.Timestamp, cli.Reset,
					cli.Cyan, h.Role, cli.Reset,
					who,
					h.SessionID,
					snippet)
			}
		})
	},
}

func init() {
	conversationSearchCmd.Flags().Int("limit", 20, "Maximum number of hits (1-100)")
	conversationSearchCmd.Flags().String("agent", "", "Narrow the search to one agent (slug or ID); default is every agent in the workspace")
	conversationCmd.AddCommand(conversationSearchCmd)
}
