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
// e-mail address, a signed webhook, or a chat/push destination (Discord,
// Slack, Telegram, ntfy, …) — either workspace-wide (admin-managed) or personal
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
	Short:   "Manage outbound notification channels (email, webhook, chat and push)",
	Long: `Create, list, delete, and test outbound notification channels.

Three delivery mechanisms:
  - email:   sends via the instance mailer (must be configured)
  - webhook: POSTs a JSON payload signed with X-Crewship-Signature
             (HMAC-SHA256 of the body, "sha256=<hex>")
  - chat:    Discord, Slack, Telegram, ntfy, Gotify, Pushover, Mattermost,
             Matrix, Teams, Google Chat, Opsgenie. Pick one with --provider
             and fill in its fields with --field key=value.

A channel is either WORKSPACE-scoped (admin-managed, feeds both the legacy
run-terminal broadcast and the #1412 preference matrix for every member the
admin allowlists) or PERSONAL (--personal; a member's own channel, usable
only in their own preference matrix — see 'crewship notify prefs').

Examples:
  crewship notifychannel add --type webhook --url https://hooks.example.com/crewship
  crewship notifychannel add --type email --to ops@example.com
  crewship notifychannel add --type chat --provider slack \
      --field webhook_url=https://hooks.slack.com/services/T0/B0/XXX --personal
  crewship notifychannel list
  crewship notifychannel providers
  crewship notifychannel providers --provider telegram
  crewship notifychannel test-draft --type chat --provider ntfy --field topic=my-alerts
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
	Short: "Add an email, webhook, or chat/push notification channel",
	Long: `Add a notification channel.

  --type webhook            requires --url (signed JSON POST)
  --type email              requires --to
  --type chat               requires --provider and one --field per form input

Run 'crewship notifychannel providers' to see every provider and the fields
it needs, then pass them as --field key=value:

  crewship notifychannel add --type chat --provider discord \
      --field webhook_url=https://discord.com/api/webhooks/123/abc

Advanced: --url still accepts a pre-composed delivery URL instead of --field,
for scripting and for restoring a channel from a backup.

By default a channel is WORKSPACE-scoped (admin-managed, requires ADMIN/OWNER).
Pass --personal to add your OWN channel instead — any member may add a personal
channel; it is only usable in YOUR preference matrix (see 'crewship notify prefs').

--categories restricts a WORKSPACE channel to a subset of the notification
categories. Omit for "every category".

` + categoryHelp(),
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
		fieldFlags, _ := cmd.Flags().GetStringArray("field")
		switch typ {
		case "webhook":
			if urlFlag == "" {
				return fmt.Errorf("--url is required for a webhook channel")
			}
		case "email":
			if to == "" {
				return fmt.Errorf("--to is required for an email channel")
			}
		case "chat", "shoutrrr":
			// "shoutrrr" is the library's name and leaked into the CLI as a
			// flag VALUE, which makes it API surface — it keeps working as a
			// hidden alias so existing scripts don't break, but "chat" is
			// what we document.
			typ = "shoutrrr"
			if provider == "" {
				return fmt.Errorf("--provider is required for a chat channel " +
					"(run 'crewship notifychannel providers' to list them)")
			}
			if len(fieldFlags) == 0 && urlFlag == "" {
				return fmt.Errorf("give the provider's fields with --field key=value "+
					"(run 'crewship notifychannel providers --provider %s' to see which), "+
					"or pass a pre-composed --url", provider)
			}
		default:
			return fmt.Errorf("--type must be 'email', 'webhook', or 'chat'")
		}

		fields, err := parseFieldFlags(fieldFlags)
		if err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Post("/api/v1/notification-channels", map[string]any{
			"type": typ, "url": urlFlag, "to": to, "secret": secret, "events": events,
			"provider": provider, "shoutrrr_url": urlFlag, "fields": fields, "personal": personal,
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

// notifyChannelProvidersCmd lists the chat/push providers this instance
// supports, the form fields each needs, and whether it is admin-enabled.
var notifyChannelProvidersCmd = &cobra.Command{
	Use:   "providers",
	Short: "List chat/push providers and the fields each one needs",
	Long: `List every provider this instance supports and whether it is enabled.

Pass --provider <name> to print that provider's form: which fields it needs,
what each one is, and where to find the value. Those field keys are what
'notifychannel add --field key=value' expects.`,
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
			Providers []notifyProviderRow `json:"providers"`
		}
		if err := cli.ReadJSON(resp, &body); err != nil {
			return err
		}
		f := newFormatter()

		// Detail view: one provider's form, so a user can fill it in without
		// leaving the terminal or guessing field names.
		if want, _ := cmd.Flags().GetString("provider"); want != "" {
			for _, p := range body.Providers {
				if !strings.EqualFold(p.Provider, want) {
					continue
				}
				return f.AutoHuman(p, func() {
					fmt.Printf("%s — %s\n", p.Label, p.Blurb)
					if !p.Enabled {
						fmt.Println("(disabled on this instance)")
					}
					fmt.Println()
					for _, fl := range p.Fields {
						req := "optional"
						if fl.Required {
							req = "required"
						}
						fmt.Printf("  --field %s=…  (%s)\n", fl.Key, req)
						fmt.Printf("      %s\n", fl.Label)
						if fl.Help != "" {
							fmt.Printf("      %s\n", fl.Help)
						}
						if fl.HelpURL != "" {
							fmt.Printf("      %s\n", fl.HelpURL)
						}
						fmt.Println()
					}
				})
			}
			return fmt.Errorf("unknown provider %q (run 'crewship notifychannel providers' to list them)", want)
		}

		headers := []string{"PROVIDER", "NAME", "ENABLED", "FIELDS"}
		rows := make([][]string, 0, len(body.Providers))
		for _, p := range body.Providers {
			keys := make([]string, 0, len(p.Fields))
			for _, fl := range p.Fields {
				if fl.Required {
					keys = append(keys, fl.Key)
				}
			}
			rows = append(rows, []string{p.Provider, p.Label, fmt.Sprintf("%v", p.Enabled), strings.Join(keys, ", ")})
		}
		return f.Auto(body.Providers, headers, rows)
	},
}

// notifyProviderRow mirrors the providers-registry response, including each
// provider's form definition — the CLI renders the same questions as the UI
// rather than carrying its own copy of the provider list.
type notifyProviderRow struct {
	Provider string `json:"provider"`
	Label    string `json:"label"`
	Blurb    string `json:"blurb"`
	Enabled  bool   `json:"enabled"`
	Fields   []struct {
		Key      string `json:"key"`
		Label    string `json:"label"`
		Required bool   `json:"required"`
		Help     string `json:"help"`
		HelpURL  string `json:"help_url"`
	} `json:"fields"`
}

// parseFieldFlags turns repeated --field key=value flags into a map.
func parseFieldFlags(flags []string) (map[string]string, error) {
	if len(flags) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(flags))
	for _, raw := range flags {
		// SplitN so a value containing '=' (a URL with a query string, which
		// Google Chat webhooks always have) survives intact.
		parts := strings.SplitN(raw, "=", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
			return nil, fmt.Errorf("--field must be key=value, got %q", raw)
		}
		out[strings.TrimSpace(parts[0])] = parts[1]
	}
	return out, nil
}

var notifyChannelTestDraftCmd = &cobra.Command{
	Use:   "test-draft",
	Short: "Send a test notification without saving a channel",
	Long: `Verify a provider's settings BEFORE creating the channel.

Takes the same --type/--provider/--field flags as 'add', sends one test
notification, and stores nothing. Use it to confirm a"Channel type: email | webhook | chat (required)"URL or token
is right instead of saving a channel and finding out later.

  crewship notifychannel test-draft --type chat --provider"Provider name for --type chat (see 'notifychannel providers')"\
      --field webhook_url=https://discord.com/api/webhooks/123/abc`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		typ, _ := cmd.Flags().GetString("type")
		if typ == "chat" {
			typ = "shoutrrr"
		}
		provider, _ := cmd.Flags().GetString("provider")
		urlFlag, _ := cmd.Flags().GetString("url")
		to, _ := cmd.Flags().GetString("to")
		secret, _ := cmd.Flags().GetString("secret")
		fieldFlags, _ := cmd.Flags().GetStringArray("field")
		fields, err := parseFieldFlags(fieldFlags)
		if err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Post("/api/v1/notification-channels/test", map[string]any{
			"type": typ, "provider": provider, "fields": fields,
			"url": urlFlag, "shoutrrr_url": urlFlag, "to": to, "secret": secret,
		})
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		f := newFormatter()
		return f.AutoHuman(map[string]any{"ok": true}, func() {
			cli.PrintSuccess("Test notification sent — nothing was saved")
		})
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
	notifyChannelAddCmd.Flags().String("type", "", "Channel type: email | webhook | chat (required)")
	notifyChannelAddCmd.Flags().String("url", "", "Webhook URL, or a pre-composed delivery URL for --type chat (advanced)")
	notifyChannelAddCmd.Flags().String("to", "", "Destination email address (required for --type email)")
	notifyChannelAddCmd.Flags().String("secret", "", "Webhook signing secret (optional; auto-generated when blank)")
	notifyChannelAddCmd.Flags().StringSlice("events", nil, "Run outcomes to notify on: failed, completed, or all (default: failed) — legacy #850 path")
	notifyChannelAddCmd.Flags().String("provider", "", "Provider name for --type chat (see 'notifychannel providers')")
	notifyChannelAddCmd.Flags().Bool("personal", false, "Create a personal channel owned by you, instead of a workspace-wide one (any role)")
	notifyChannelAddCmd.Flags().StringSlice("categories", nil, "Admin category allowlist for a workspace channel (default: every category)")
	notifyChannelAddCmd.Flags().String("min-priority", "", "Priority floor: low | medium | high | urgent (default: low)")

	notifyChannelAddCmd.Flags().StringArray("field", nil,
		"Provider form field as key=value; repeatable (see 'notifychannel providers --provider <name>')")

	notifyChannelProvidersCmd.Flags().String("provider", "", "Show one provider's form fields instead of the list")

	notifyChannelTestDraftCmd.Flags().String("type", "", "Channel type: email | webhook | chat (required)")
	notifyChannelTestDraftCmd.Flags().String("provider", "", "Provider name (required for --type chat)")
	notifyChannelTestDraftCmd.Flags().StringArray("field", nil, "Provider form field as key=value; repeatable")
	notifyChannelTestDraftCmd.Flags().String("url", "", "Webhook URL, or a pre-composed delivery URL for --type chat")
	notifyChannelTestDraftCmd.Flags().String("to", "", "Destination email address (for --type email)")
	notifyChannelTestDraftCmd.Flags().String("secret", "", "Webhook signing secret (optional)")

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
	notifyChannelCmd.AddCommand(notifyChannelTestDraftCmd)
	notifyChannelCmd.AddCommand(notifyChannelDeliveriesCmd)
}
