package main

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// explainCmd is "tell me what happened in this run, in plain English."
//
// Pure orchestration over existing primitives:
//  1. Look up the run (fetchRun, already used by retry).
//  2. Fetch journal entries filtered by the run's agent + start time.
//  3. Compose a prompt that includes the formatted entries.
//  4. Send the prompt to the default agent (or --agent override) using
//     the same streaming pipeline as `ask`.
//
// No new server-side surface area — everything is composed from existing
// HTTP routes. If the journal entries are noisy, --type filters can be
// added; defaults to all severities so we don't accidentally hide the
// failure the user is asking about.
var explainCmd = &cobra.Command{
	Use:   "explain <run-id>",
	Short: "Summarize what happened in a run via the default agent",
	Long: `Fetch journal entries for a run and ask an agent to summarize them.

Useful for understanding why a run failed, what tools it called, or what
decisions the keeper made. Reads /api/v1/runs and /api/v1/journal; sends
the formatted entries to the default agent (or --agent override) as
context for a one-shot summary.

Examples:
  crewship explain r_abc                       # summarize via default agent
  crewship explain r_abc --agent viktor
  crewship explain r_abc --types error,keeper.decision,peer.escalation`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		runID := args[0]
		client := newAPIClient()

		runMeta, err := fetchRun(client, runID)
		if err != nil {
			return err
		}
		if runMeta.AgentID == "" {
			return fmt.Errorf("run %s has no agent_id; cannot scope journal lookup", runID)
		}

		// Window: 5 min before created_at to now. Without a run-end timestamp
		// we may slightly over-fetch, but the agent can ignore unrelated entries.
		windowStart, err := runWindowStart(client, runID)
		if err != nil {
			windowStart = time.Now().Add(-1 * time.Hour) // wide fallback
		}

		typesFilter, _ := cmd.Flags().GetString("types")
		entriesText, err := fetchJournalForExplain(client, runMeta.AgentID, windowStart, typesFilter)
		if err != nil {
			return fmt.Errorf("fetch journal: %w", err)
		}
		if entriesText == "" {
			return fmt.Errorf("no journal entries found for agent in window starting %s", windowStart.Format(time.RFC3339))
		}

		// Resolve summarizing agent: --agent flag → CREWSHIP_DEFAULT_AGENT env
		// → config.default_agent.
		summarizerFlag, _ := cmd.Flags().GetString("agent")
		summarizerSlug := cli.ResolveDefaultAgent(summarizerFlag, cliCfg)
		if summarizerSlug == "" {
			return fmt.Errorf("no agent set to summarize. Use --agent, set CREWSHIP_DEFAULT_AGENT, or run 'crewship config set default-agent <slug>'")
		}
		summarizerID, err := resolveAgentID(client, summarizerSlug)
		if err != nil {
			return err
		}

		prompt := buildExplainPrompt(runID, entriesText)

		// Create a chat for the summary and stream the answer.
		resp, err := client.Post("/api/v1/agents/"+summarizerID+"/chats", map[string]string{
			"mode":   "CHAT",
			"origin": "CLI",
		})
		if err != nil {
			return fmt.Errorf("create chat: %w", err)
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var chatResult struct {
			ID string `json:"id"`
		}
		if err := cli.ReadJSON(resp, &chatResult); err != nil {
			return err
		}

		wsToken, err := cli.WSTokenFromServer(client)
		if err != nil {
			return fmt.Errorf("get WS token: %w", err)
		}
		server := cli.ResolveServer(flagServer, cliCfg)
		quiet, _ := cmd.Flags().GetBool("quiet")
		md := resolveMarkdownFromCmd(cmd)
		saveFile, err := openSaveFile(cmd)
		if err != nil {
			return err
		}
		if saveFile != nil {
			defer saveFile.Close()
		}

		if !quiet {
			fmt.Fprintf(os.Stderr, "%s[explain %s via %s]%s\n", cli.Dim, runID, summarizerSlug, cli.Reset)
		}
		return runStream(server, wsToken, summarizerID, summarizerSlug, chatResult.ID, prompt, quiet, md, saveFile)
	},
}

// runWindowStart returns the run's created_at. Falls back to 1 hour ago if
// the run is not in the recent page (we keep this scoped to fetchRun's
// 100-row window for now — explaining ancient runs needs a richer index).
func runWindowStart(client *cli.Client, runID string) (time.Time, error) {
	q := url.Values{}
	q.Set("limit", "100")
	resp, err := client.Get("/api/v1/runs?" + q.Encode())
	if err != nil {
		return time.Time{}, err
	}
	if err := cli.CheckError(resp); err != nil {
		return time.Time{}, err
	}
	var body struct {
		Data []struct {
			ID        string `json:"id"`
			CreatedAt string `json:"created_at"`
		} `json:"data"`
	}
	if err := cli.ReadJSON(resp, &body); err != nil {
		return time.Time{}, err
	}
	for _, r := range body.Data {
		if r.ID != runID {
			continue
		}
		if t, err := time.Parse(time.RFC3339, r.CreatedAt); err == nil {
			return t.Add(-5 * time.Minute), nil
		}
	}
	return time.Time{}, fmt.Errorf("run %s not in recent window", runID)
}

// fetchJournalForExplain pulls journal entries for the agent since `from`
// and renders them as a compact text block suitable for inclusion in a
// summarization prompt. Caps at 200 entries to keep the prompt bounded.
func fetchJournalForExplain(client *cli.Client, agentID string, from time.Time, types string) (string, error) {
	q := url.Values{}
	q.Set("agent_id", agentID)
	q.Set("since", from.UTC().Format(time.RFC3339))
	q.Set("limit", "200")
	if types != "" {
		q.Set("entry_type", types)
	}
	resp, err := client.Get("/api/v1/journal?" + q.Encode())
	if err != nil {
		return "", err
	}
	if err := cli.CheckError(resp); err != nil {
		return "", err
	}
	var body struct {
		Entries []map[string]any `json:"entries"`
	}
	if err := cli.ReadJSON(resp, &body); err != nil {
		return "", err
	}
	if len(body.Entries) == 0 {
		return "", nil
	}
	// Reverse so oldest-first reads naturally as a timeline.
	var sb strings.Builder
	for i := len(body.Entries) - 1; i >= 0; i-- {
		e := body.Entries[i]
		ts, _ := e["ts"].(string)
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			ts = t.Format("15:04:05")
		}
		entryType, _ := e["entry_type"].(string)
		severity, _ := e["severity"].(string)
		summary, _ := e["summary"].(string)
		fmt.Fprintf(&sb, "%s  [%s/%s]  %s\n", ts, severity, entryType, summary)
	}
	return sb.String(), nil
}

// buildExplainPrompt is the canonical prompt template for explanations.
// Kept separate so a future user-defined override (a config key, or a
// --template flag) is a one-line change.
func buildExplainPrompt(runID, entries string) string {
	return strings.TrimSpace(fmt.Sprintf(`Summarize what happened in this agent run. Be concise: 3-6 bullet points.
Highlight any errors, escalations, keeper denials, budget warnings, or
unexpected control flow. If the run completed normally, say so plainly.

Run ID: %s

Journal entries (oldest first):
%s`, runID, entries))
}

func init() {
	explainCmd.Flags().String("agent", "", "Agent to summarize via (default: default-agent config)")
	explainCmd.Flags().String("types", "", "Comma-separated entry types to include (default: all)")
	explainCmd.Flags().BoolP("quiet", "q", false, "Suppress meta lines")
	explainCmd.Flags().Bool("markdown", false, "Render markdown ANSI styling")
	explainCmd.Flags().Bool("no-markdown", false, "Disable markdown ANSI styling")
	explainCmd.Flags().String("save", "", "Tee the response to this path")
}
