package main

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// notifyChannelCmd groups outbound notification-channel operations
// (issue #850). A channel is a workspace-scoped delivery target — an
// e-mail address or a signed webhook — that a routine run fans out to
// when it completes or fails, so the news reaches someone who isn't
// watching the in-product inbox.
//
// Writes require MANAGER+ (enforced server-side by the route table).
var notifyChannelCmd = &cobra.Command{
	Use:     "notifychannel",
	Aliases: []string{"notify-channel"},
	Short:   "Manage outbound notification channels (email + signed webhook)",
	Long: `Create, list, delete, and test outbound notification channels.

On a routine run reaching a completed/failed terminal state, Crewship
fans the outcome out to every enabled channel in the workspace:
  - email:   sends via the instance mailer (must be configured)
  - webhook: POSTs a JSON payload signed with X-Crewship-Signature
             (HMAC-SHA256 of the body, "sha256=<hex>")

Examples:
  crewship notifychannel add --type webhook --url https://hooks.example.com/crewship
  crewship notifychannel add --type email --to ops@example.com
  crewship notifychannel list
  crewship notifychannel test nch_abc123
  crewship notifychannel rm nch_abc123 --yes`,
}

// notifyChannelRow mirrors the rendered/JSON columns.
type notifyChannelRow struct {
	ID        string   `json:"id"`
	Type      string   `json:"type"`
	URL       string   `json:"url,omitempty"`
	To        string   `json:"to,omitempty"`
	Events    []string `json:"events,omitempty"`
	Enabled   bool     `json:"enabled"`
	CreatedBy string   `json:"created_by,omitempty"`
	CreatedAt string   `json:"created_at,omitempty"`
}

var notifyChannelListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the workspace's notification channels",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Get("/api/v1/notification-channels")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var body struct {
			Channels []notifyChannelRow `json:"channels"`
		}
		if err := cli.ReadJSON(resp, &body); err != nil {
			return err
		}
		f := newFormatter()
		headers := []string{"ID", "TYPE", "TARGET", "EVENTS", "ENABLED", "CREATED"}
		rows := make([][]string, 0, len(body.Channels))
		for _, c := range body.Channels {
			target := c.URL
			if c.Type == "email" {
				target = c.To
			}
			rows = append(rows, []string{
				truncateString(c.ID, 24),
				c.Type,
				truncateString(target, 40),
				truncateString(strings.Join(c.Events, ","), 24),
				fmt.Sprintf("%v", c.Enabled),
				c.CreatedAt,
			})
		}
		return f.Auto(body.Channels, headers, rows)
	},
}

var notifyChannelAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Add an email or webhook notification channel",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		typ, _ := cmd.Flags().GetString("type")
		urlFlag, _ := cmd.Flags().GetString("url")
		to, _ := cmd.Flags().GetString("to")
		secret, _ := cmd.Flags().GetString("secret")
		events, _ := cmd.Flags().GetStringSlice("events")
		switch typ {
		case "webhook":
			if urlFlag == "" {
				return fmt.Errorf("--url is required for a webhook channel")
			}
		case "email":
			if to == "" {
				return fmt.Errorf("--to is required for an email channel")
			}
		default:
			return fmt.Errorf("--type must be 'email' or 'webhook'")
		}

		client := newAPIClient()
		resp, err := client.Post("/api/v1/notification-channels", map[string]any{
			"type": typ, "url": urlFlag, "to": to, "secret": secret, "events": events,
		})
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var created struct {
			notifyChannelRow
			Secret string `json:"secret,omitempty"`
		}
		if err := cli.ReadJSON(resp, &created); err != nil {
			return err
		}
		f := newFormatter()
		if f.Format == "json" {
			return f.JSON(created)
		}
		if f.Format == "yaml" {
			return f.YAML(created)
		}
		cli.PrintSuccess(fmt.Sprintf("Notification channel created: %s (%s)", created.ID, created.Type))
		if len(created.Events) > 0 {
			fmt.Printf("Notifies on: %s\n", strings.Join(created.Events, ", "))
		}
		if created.Secret != "" {
			fmt.Printf("\nWebhook signing secret (shown once — store it now):\n  %s\n", created.Secret)
			fmt.Println("\nVerify inbound requests: X-Crewship-Signature = \"sha256=\" + HMAC_SHA256(body, secret)")
		}
		return nil
	},
}

var notifyChannelTestCmd = &cobra.Command{
	Use:   "test <id>",
	Short: "Send a synthetic test notification to one channel",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		id := args[0]
		client := newAPIClient()
		resp, err := client.Post("/api/v1/notification-channels/"+url.PathEscape(id)+"/test", nil)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Test notification sent to %s", id))
		return nil
	},
}

var notifyChannelRmCmd = &cobra.Command{
	Use:     "rm <id>",
	Aliases: []string{"delete", "remove"},
	Short:   "Delete a notification channel",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		id := args[0]
		if err := confirmAction(cmd, fmt.Sprintf("Delete notification channel %s?", id)); err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Delete("/api/v1/notification-channels/" + url.PathEscape(id))
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Notification channel deleted: %s", id))
		return nil
	},
}

func init() {
	notifyChannelAddCmd.Flags().String("type", "", "Channel type: email | webhook (required)")
	notifyChannelAddCmd.Flags().String("url", "", "Webhook URL (required for --type webhook)")
	notifyChannelAddCmd.Flags().String("to", "", "Destination email address (required for --type email)")
	notifyChannelAddCmd.Flags().String("secret", "", "Webhook signing secret (optional; auto-generated when blank)")
	notifyChannelAddCmd.Flags().StringSlice("events", nil, "Run outcomes to notify on: failed, completed, or all (default: failed)")

	notifyChannelRmCmd.Flags().Bool("yes", false, "Skip confirmation prompt")

	notifyChannelCmd.AddCommand(notifyChannelListCmd)
	notifyChannelCmd.AddCommand(notifyChannelAddCmd)
	notifyChannelCmd.AddCommand(notifyChannelTestCmd)
	notifyChannelCmd.AddCommand(notifyChannelRmCmd)
}
