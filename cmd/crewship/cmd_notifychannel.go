package main

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// notifyChannelCmd groups outbound notification-channel operations
// (issue #850, extended by #1412). A channel is a delivery target — an
// e-mail address, a signed webhook, or a shoutrrr service URL (Slack,
// Discord, Telegram) — either workspace-wide (admin-managed) or personal
// (a member's own). Workspace channels feed the legacy run-terminal
// broadcast (events) AND the #1412 category x channel preference matrix;
// personal channels are usable only in their owner's own matrix (see
// `crewship notify prefs`).
//
// Workspace-channel writes require ADMIN/OWNER; a personal channel
// (--personal) is self-service for any role (enforced server-side).
var notifyChannelCmd = &cobra.Command{
	Use:     "notifychannel",
	Aliases: []string{"notify-channel"},
	Short:   "Manage outbound notification channels (email, webhook, Slack, Discord, Telegram)",
	Long: `Create, list, delete, and test outbound notification channels.

Three delivery mechanisms:
  - email:    sends via the instance mailer (must be configured)
  - webhook:  POSTs a JSON payload signed with X-Crewship-Signature
              (HMAC-SHA256 of the body, "sha256=<hex>")
  - shoutrrr: Slack / Discord / Telegram via an Apprise-style service URL
              (--provider slack|discord|telegram --url <service-url>)

A channel is either WORKSPACE-scoped (admin-managed, feeds both the legacy
run-terminal broadcast and the #1412 preference matrix for every member the
admin allowlists) or PERSONAL (--personal; a member's own channel, usable
only in their own preference matrix — see 'crewship notify prefs').

Examples:
  crewship notifychannel add --type webhook --url https://hooks.example.com/crewship
  crewship notifychannel add --type email --to ops@example.com
  crewship notifychannel add --type shoutrrr --provider slack --url slack://hook:TOKEN@webhook --personal
  crewship notifychannel list
  crewship notifychannel providers
  crewship notifychannel deliveries --status failed
  crewship notifychannel test nch_abc123
  crewship notifychannel rm nch_abc123 --yes`,
}

// notifyChannelRow mirrors the rendered/JSON columns.
type notifyChannelRow struct {
	ID          string   `json:"id"`
	Type        string   `json:"type"`
	Provider    string   `json:"provider,omitempty"`
	URL         string   `json:"url,omitempty"`
	To          string   `json:"to,omitempty"`
	Events      []string `json:"events,omitempty"`
	Enabled     bool     `json:"enabled"`
	CreatedBy   string   `json:"created_by,omitempty"`
	CreatedAt   string   `json:"created_at,omitempty"`
	Scope       string   `json:"scope,omitempty"`
	OwnerUserID string   `json:"owner_user_id,omitempty"`
	Categories  []string `json:"categories,omitempty"`
	MinPriority string   `json:"min_priority,omitempty"`
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
		headers := []string{"ID", "TYPE", "SCOPE", "TARGET", "CATEGORIES", "ENABLED", "CREATED"}
		rows := make([][]string, 0, len(body.Channels))
		for _, c := range body.Channels {
			target := c.URL
			switch c.Type {
			case "email":
				target = c.To
			case "shoutrrr":
				target = c.Provider // the service url itself is never returned by List
			}
			scope := c.Scope
			if scope == "" {
				scope = "workspace"
			}
			cats := "all"
			if len(c.Categories) > 0 {
				cats = strings.Join(c.Categories, ",")
			}
			rows = append(rows, []string{
				truncateString(c.ID, 24),
				c.Type,
				scope,
				truncateString(target, 32),
				truncateString(cats, 24),
				fmt.Sprintf("%v", c.Enabled),
				c.CreatedAt,
			})
		}
		return f.Auto(body.Channels, headers, rows)
	},
}

var notifyChannelAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Add an email, webhook, or shoutrrr (Slack/Discord/Telegram) notification channel",
	Long: `Add a notification channel.

  --type webhook            requires --url (signed JSON POST)
  --type email               requires --to
  --type shoutrrr            requires --provider (slack|discord|telegram) and --url
                              (the shoutrrr service URL, e.g. slack://hook:TOKEN@webhook)

By default a channel is WORKSPACE-scoped (admin-managed, requires ADMIN/OWNER).
Pass --personal to add your OWN channel instead — any member may add a personal
channel; it is only usable in YOUR preference matrix (see 'crewship notify prefs').

--categories restricts a WORKSPACE channel to a subset of the 9 notification
categories (approvals, escalations, runs.failed, runs.completed, chat.replies,
security, budget, system, memory). Omit for "every category".`,
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
		provider, _ := cmd.Flags().GetString("provider")
		personal, _ := cmd.Flags().GetBool("personal")
		categories, _ := cmd.Flags().GetStringSlice("categories")
		minPriority, _ := cmd.Flags().GetString("min-priority")
		switch typ {
		case "webhook":
			if urlFlag == "" {
				return fmt.Errorf("--url is required for a webhook channel")
			}
		case "email":
			if to == "" {
				return fmt.Errorf("--to is required for an email channel")
			}
		case "shoutrrr":
			if provider == "" {
				return fmt.Errorf("--provider is required for a shoutrrr channel (slack, discord, or telegram)")
			}
			if urlFlag == "" {
				return fmt.Errorf("--url is required for a shoutrrr channel (the service url, e.g. slack://hook:TOKEN@webhook)")
			}
		default:
			return fmt.Errorf("--type must be 'email', 'webhook', or 'shoutrrr'")
		}

		client := newAPIClient()
		resp, err := client.Post("/api/v1/notification-channels", map[string]any{
			"type": typ, "url": urlFlag, "to": to, "secret": secret, "events": events,
			"provider": provider, "shoutrrr_url": urlFlag, "personal": personal,
			"categories": categories, "min_priority": minPriority,
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
		return f.AutoHuman(created, func() {
			cli.PrintSuccess(fmt.Sprintf("Notification channel created: %s (%s)", created.ID, created.Type))
			if created.Scope == "user" {
				fmt.Println("Scope: personal (only usable in your own preference matrix)")
			}
			if len(created.Events) > 0 {
				fmt.Printf("Notifies on: %s\n", strings.Join(created.Events, ", "))
			}
			if created.Secret != "" {
				switch created.Type {
				case "shoutrrr":
					fmt.Printf("\nService URL (shown once — store it now):\n  %s\n", created.Secret)
				default:
					fmt.Printf("\nWebhook signing secret (shown once — store it now):\n  %s\n", created.Secret)
					fmt.Println("\nVerify inbound requests: X-Crewship-Signature = \"sha256=\" + HMAC_SHA256(body, secret)")
				}
			}
		})
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

// notifyChannelProvidersCmd lists the shoutrrr providers this instance
// supports and whether each is admin-enabled (issue #1412).
var notifyChannelProvidersCmd = &cobra.Command{
	Use:   "providers",
	Short: "List supported notification providers (slack, discord, telegram) and their enabled state",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Get("/api/v1/notification-providers")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var body struct {
			Providers []struct {
				Provider string `json:"provider"`
				Scheme   string `json:"scheme"`
				Enabled  bool   `json:"enabled"`
			} `json:"providers"`
		}
		if err := cli.ReadJSON(resp, &body); err != nil {
			return err
		}
		f := newFormatter()
		headers := []string{"PROVIDER", "SCHEME", "ENABLED"}
		rows := make([][]string, 0, len(body.Providers))
		for _, p := range body.Providers {
			rows = append(rows, []string{p.Provider, p.Scheme + "://", fmt.Sprintf("%v", p.Enabled)})
		}
		return f.Auto(body.Providers, headers, rows)
	},
}

// notifyChannelDeliveriesCmd surfaces the delivery log — "why didn't my
// notification arrive?" Admin-only server-side (see NotifyDeliveriesHandler).
var notifyChannelDeliveriesCmd = &cobra.Command{
	Use:   "deliveries",
	Short: "Show the outbound notification delivery log (admin only)",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		status, _ := cmd.Flags().GetString("status")
		channelID, _ := cmd.Flags().GetString("channel")
		category, _ := cmd.Flags().GetString("category")
		limit, _ := cmd.Flags().GetInt("limit")

		q := url.Values{}
		if status != "" {
			q.Set("status", status)
		}
		if channelID != "" {
			q.Set("channel_id", channelID)
		}
		if category != "" {
			q.Set("category", category)
		}
		if limit > 0 {
			q.Set("limit", fmt.Sprintf("%d", limit))
		}
		path := "/api/v1/notification-deliveries"
		if enc := q.Encode(); enc != "" {
			path += "?" + enc
		}

		client := newAPIClient()
		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var body struct {
			Deliveries []struct {
				ID        string `json:"id"`
				ChannelID string `json:"channel_id"`
				UserID    string `json:"user_id"`
				Category  string `json:"category"`
				Status    string `json:"status"`
				Error     string `json:"error"`
				Attempts  int    `json:"attempts"`
				CreatedAt string `json:"created_at"`
			} `json:"deliveries"`
		}
		if err := cli.ReadJSON(resp, &body); err != nil {
			return err
		}
		f := newFormatter()
		headers := []string{"ID", "CHANNEL", "USER", "CATEGORY", "STATUS", "ATTEMPTS", "CREATED"}
		rows := make([][]string, 0, len(body.Deliveries))
		for _, d := range body.Deliveries {
			rows = append(rows, []string{
				truncateString(d.ID, 20), truncateString(d.ChannelID, 20), truncateString(d.UserID, 16),
				d.Category, d.Status, fmt.Sprintf("%d", d.Attempts), d.CreatedAt,
			})
		}
		return f.Auto(body.Deliveries, headers, rows)
	},
}

func init() {
	notifyChannelAddCmd.Flags().String("type", "", "Channel type: email | webhook | shoutrrr (required)")
	notifyChannelAddCmd.Flags().String("url", "", "Webhook URL, or the shoutrrr service URL for --type shoutrrr")
	notifyChannelAddCmd.Flags().String("to", "", "Destination email address (required for --type email)")
	notifyChannelAddCmd.Flags().String("secret", "", "Webhook signing secret (optional; auto-generated when blank)")
	notifyChannelAddCmd.Flags().StringSlice("events", nil, "Run outcomes to notify on: failed, completed, or all (default: failed) — legacy #850 path")
	notifyChannelAddCmd.Flags().String("provider", "", "shoutrrr provider: slack | discord | telegram (required for --type shoutrrr)")
	notifyChannelAddCmd.Flags().Bool("personal", false, "Create a personal channel owned by you, instead of a workspace-wide one (any role)")
	notifyChannelAddCmd.Flags().StringSlice("categories", nil, "Admin category allowlist for a workspace channel (default: every category)")
	notifyChannelAddCmd.Flags().String("min-priority", "", "Priority floor: low | medium | high | urgent (default: low)")

	notifyChannelRmCmd.Flags().Bool("yes", false, "Skip confirmation prompt")

	notifyChannelDeliveriesCmd.Flags().String("status", "", "Filter: pending | sent | failed | dropped_pref | dropped_rate")
	notifyChannelDeliveriesCmd.Flags().String("channel", "", "Filter by channel id")
	notifyChannelDeliveriesCmd.Flags().String("category", "", "Filter by category")
	notifyChannelDeliveriesCmd.Flags().Int("limit", 0, "Max rows (default: server default, 100)")

	notifyChannelCmd.AddCommand(notifyChannelListCmd)
	notifyChannelCmd.AddCommand(notifyChannelAddCmd)
	notifyChannelCmd.AddCommand(notifyChannelTestCmd)
	notifyChannelCmd.AddCommand(notifyChannelRmCmd)
	notifyChannelCmd.AddCommand(notifyChannelProvidersCmd)
	notifyChannelCmd.AddCommand(notifyChannelDeliveriesCmd)
}
