package main

import (
	"fmt"
	"net/url"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// notifyTemplatesCmd manages the wording of the notifications Crewship
// generates ITSELF.
//
// A routine's `notify` step has always written its own message. Everything the
// product generates — "Pipeline x completed", "Scheduled routine failed: y" —
// had its wording computed in Go, one string per producer, with no way to
// change it. These are the overrides.
//
// Distinct from `notify prefs` (whether a category reaches YOU) and from
// `notifychannel` (where it goes). This is what it says when it gets there.
var notifyTemplatesCmd = &cobra.Command{
	Use:   "templates",
	Short: "Set what Crewship's own notifications say",
	Long: `Override the wording of the notifications Crewship generates itself.

A template replaces the title, the body, or both, for one category. Leave a
field out to keep what the producer computed.

Two namespaces are available:

  {{ vars.<fact> }}      the source event's own facts — which ones depend on
                         the event; see 'crewship notifychannel deliveries'
                         and the Activity timeline for what an event carries
  {{ source.title }}     what the producer computed, so a template can ADD to
  {{ source.body }}      a message rather than only replace it
  {{ source.category }}
  {{ source.kind }}

A reference to a fact this particular event does not carry renders empty; a
template that renders to nothing falls back to the producer's own wording, so
a notification never arrives with a blank subject line.

Templates apply ONLY to what Crewship generates. A routine's notify step and
an agent's message carry an author's words and are never rewritten.

Scope is the category, optionally narrowed to one channel with --channel —
wording belongs to the event, and the narrowing is for the case where one
destination genuinely wants something different.

` + categoryHelp() + `

Examples:
  crewship notify templates list
  crewship notify templates set --category routines.failed \
      --title "{{ vars.pipeline_slug }} failed" \
      --body "after {{ vars.total_duration_ms }}ms — {{ source.body }}"
  crewship notify templates set --category routines.completed --channel nch_abc123 \
      --title "[done] {{ source.title }}"
  crewship notify templates rm --category routines.failed`,
}

// notifyTemplateRow mirrors the API payload for CLI I/O without importing the
// server-side package into the CLI binary.
type notifyTemplateRow struct {
	Category  string `json:"category"`
	ChannelID string `json:"channel_id"`
	Title     string `json:"title"`
	Body      string `json:"body"`
}

var notifyTemplatesListCmd = &cobra.Command{
	Use:   "list",
	Short: "Show the workspace's notification templates",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		resp, err := newAPIClient().Get("/api/v1/notification-templates")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var body struct {
			Templates []notifyTemplateRow `json:"templates"`
		}
		if err := cli.ReadJSON(resp, &body); err != nil {
			return err
		}
		f := newFormatter()
		headers := []string{"CATEGORY", "CHANNEL", "TITLE", "BODY"}
		rows := make([][]string, 0, len(body.Templates))
		for _, t := range body.Templates {
			channel := t.ChannelID
			if channel == "" {
				channel = "(all)"
			}
			rows = append(rows, []string{
				t.Category, truncateString(channel, 24),
				truncateString(t.Title, 40), truncateString(t.Body, 40),
			})
		}
		return f.Auto(body.Templates, headers, rows)
	},
}

var notifyTemplatesSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Set the wording for one notification category",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		category, _ := cmd.Flags().GetString("category")
		if category == "" {
			return fmt.Errorf("--category is required")
		}
		title, _ := cmd.Flags().GetString("title")
		body, _ := cmd.Flags().GetString("body")
		if title == "" && body == "" {
			return fmt.Errorf("give --title, --body, or both (to remove a template use 'notify templates rm')")
		}
		channel, _ := cmd.Flags().GetString("channel")

		resp, err := newAPIClient().Put("/api/v1/notification-templates", map[string]any{
			"category": category, "channel_id": channel, "title": title, "body": body,
		})
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		scope := "all channels"
		if channel != "" {
			scope = "channel " + channel
		}
		fmt.Printf("Template set: %s on %s\n", category, scope)
		return nil
	},
}

var notifyTemplatesRmCmd = &cobra.Command{
	Use:   "rm",
	Short: "Remove a template, restoring Crewship's own wording",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		category, _ := cmd.Flags().GetString("category")
		if category == "" {
			return fmt.Errorf("--category is required")
		}
		channel, _ := cmd.Flags().GetString("channel")

		q := url.Values{"category": {category}}
		if channel != "" {
			q.Set("channel_id", channel)
		}
		resp, err := newAPIClient().Delete("/api/v1/notification-templates?" + q.Encode())
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		fmt.Printf("Template removed: %s\n", category)
		return nil
	},
}

func init() {
	notifyTemplatesSetCmd.Flags().String("category", "", "Category to reword (see 'crewship notify templates --help')")
	notifyTemplatesSetCmd.Flags().String("channel", "", "Narrow to one channel id; omit to apply to every channel")
	notifyTemplatesSetCmd.Flags().String("title", "", "Title template; omit to keep the producer's title")
	notifyTemplatesSetCmd.Flags().String("body", "", "Body template; omit to keep the producer's body")

	notifyTemplatesRmCmd.Flags().String("category", "", "Category whose template to remove")
	notifyTemplatesRmCmd.Flags().String("channel", "", "Channel id, when removing a channel-specific template")

	notifyTemplatesCmd.AddCommand(notifyTemplatesListCmd, notifyTemplatesSetCmd, notifyTemplatesRmCmd)
	notifyCmd.AddCommand(notifyTemplatesCmd)
}
