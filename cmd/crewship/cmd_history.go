package main

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// historyCmd is "what did I run lately?" — a workspace-wide list of recent
// agent runs with timestamps, status, trigger, and (optionally) the prompt
// that kicked the run off. Reads `/api/v1/runs` for the run list and
// `/api/v1/chats/{chatId}/messages` for the prompt preview when --prompts
// is passed.
//
// Different from `crewship run list`:
//   - Adds an optional first-message preview per run so the user can scan
//     "what was I asking about?" without opening each chat.
//   - Defaults to the last 24h window.
//   - Pretty single-line rendering with truncation.
var historyCmd = &cobra.Command{
	Use:   "history",
	Short: "Recent agent runs across the workspace",
	Long: `Show recent runs with timestamp, agent, status, trigger, and (optionally)
the first user prompt for each run.

Examples:
  crewship history
  crewship history --limit 50
  crewship history --since 7d --status failed
  crewship history --prompts             # also fetch first user message`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()

		limit, _ := cmd.Flags().GetInt("limit")
		since, _ := cmd.Flags().GetString("since")
		statusFlag, _ := cmd.Flags().GetString("status")
		agentFlag, _ := cmd.Flags().GetString("agent")
		withPrompts, _ := cmd.Flags().GetBool("prompts")

		var sinceTime time.Time
		if since != "" {
			t, err := parseSince(since)
			if err != nil {
				return fmt.Errorf("bad --since: %w", err)
			}
			sinceTime = t
		}

		q := url.Values{}
		q.Set("limit", fmt.Sprintf("%d", limit))
		if statusFlag != "" {
			q.Set("status", statusFlag)
		}
		if agentFlag != "" {
			id, err := resolveAgentID(client, agentFlag)
			if err != nil {
				return err
			}
			q.Set("agent_id", id)
		}

		resp, err := client.Get("/api/v1/runs?" + q.Encode())
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var body struct {
			Data []struct {
				ID          string  `json:"id"`
				AgentSlug   *string `json:"agent_slug"`
				AgentName   *string `json:"agent_name"`
				ChatID      *string `json:"chat_id"`
				Status      string  `json:"status"`
				TriggerType string  `json:"trigger_type"`
				CreatedAt   string  `json:"created_at"`
				FinishedAt  *string `json:"finished_at"`
			} `json:"data"`
		}
		if err := cli.ReadJSON(resp, &body); err != nil {
			return err
		}

		// Apply --since filter client-side (the runs API has no since param).
		filtered := body.Data[:0]
		for _, r := range body.Data {
			if sinceTime.IsZero() {
				filtered = append(filtered, r)
				continue
			}
			t, err := time.Parse(time.RFC3339, r.CreatedAt)
			if err != nil || t.After(sinceTime) {
				filtered = append(filtered, r)
			}
		}

		f := newFormatter()
		if f.Format == "json" {
			return f.JSON(filtered)
		}
		if f.Format == "yaml" {
			return f.YAML(filtered)
		}

		if len(filtered) == 0 {
			fmt.Printf("%sNo runs.%s\n", cli.Dim, cli.Reset)
			return nil
		}

		// Optional prompt preview pass — sequential to avoid hammering the
		// chat API. Bounded by the page (limit ≤ 100) so total fetches are
		// predictable. Failures are silent — a missing preview is better
		// than a broken history listing.
		previews := map[string]string{}
		if withPrompts {
			for _, r := range filtered {
				if r.ChatID == nil || *r.ChatID == "" {
					continue
				}
				if p := fetchFirstUserPrompt(client, *r.ChatID); p != "" {
					previews[r.ID] = p
				}
			}
		}

		for _, r := range filtered {
			ts := r.CreatedAt
			if t, err := time.Parse(time.RFC3339, r.CreatedAt); err == nil {
				ts = t.Format("2006-01-02 15:04")
			}
			slug := "?"
			if r.AgentSlug != nil {
				slug = *r.AgentSlug
			} else if r.AgentName != nil {
				slug = *r.AgentName
			}
			statusColor := cli.Gray
			switch r.Status {
			case "completed", "succeeded":
				statusColor = cli.Green
			case "failed", "error":
				statusColor = cli.Red
			case "running":
				statusColor = cli.Yellow
			}

			fmt.Printf("%s%s%s  %s%-18s%s  %s%-10s%s  %-6s",
				cli.Dim, ts, cli.Reset,
				cli.Bold, truncateString(slug, 18), cli.Reset,
				statusColor, r.Status, cli.Reset,
				r.TriggerType)

			if preview, ok := previews[r.ID]; ok {
				fmt.Printf("  %q", truncateString(firstLine(preview), 60))
			}
			fmt.Println()
		}
		return nil
	},
}

// firstLine returns the first non-empty line of `s` with leading/trailing
// whitespace stripped — keeps the preview single-line even if the prompt
// was multi-line.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

// fetchFirstUserPrompt fetches messages for a chat and returns the first
// USER role content, or empty on any failure or absence.
func fetchFirstUserPrompt(c *cli.Client, chatID string) string {
	resp, err := c.Get("/api/v1/chats/" + url.PathEscape(chatID) + "/messages?limit=10")
	if err != nil {
		return ""
	}
	if err := cli.CheckError(resp); err != nil {
		return ""
	}
	var body struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := cli.ReadJSON(resp, &body); err != nil {
		return ""
	}
	for _, m := range body.Messages {
		if strings.EqualFold(m.Role, "user") || strings.EqualFold(m.Role, "human") {
			return m.Content
		}
	}
	return ""
}

func init() {
	historyCmd.Flags().Int("limit", 20, "Max runs to list")
	historyCmd.Flags().String("since", "24h", "Time window (1h, 24h, 7d, or RFC3339)")
	historyCmd.Flags().String("status", "", "Filter by status (running|completed|failed)")
	historyCmd.Flags().String("agent", "", "Filter by agent slug or ID")
	historyCmd.Flags().Bool("prompts", false, "Fetch first user prompt per run (slower)")
}
