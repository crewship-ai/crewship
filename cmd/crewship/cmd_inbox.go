package main

import (
	"fmt"
	"net/url"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// inboxCmd is the CLI mirror of the /inbox web surface — the unified
// "stuff that needs the human" feed introduced in migration v85.
//
// Crewship is CLI-first: everything the UI exposes must have a
// scriptable equivalent so workspace operators can pipe-glue the
// inbox into their own automation (cron + jq + crewship inbox list,
// for example, to ping Slack when an unread waitpoint piles up).
//
// All four state transitions (read / unread / resolved) live here so
// a script can fully drive the inbox without ever hitting the web UI.
var inboxCmd = &cobra.Command{
	Use:   "inbox",
	Short: "List and triage human-in-the-loop inbox items (waitpoints, escalations, failed runs, messages)",
	Long: `Manage the unified human-in-the-loop inbox — the canonical "things needing
your attention" feed. Backed by inbox_items (migration v85), populated
write-through by waitpoint creation, escalation creation, and run-
failure handlers.

The inbox is the CLI counterpart of the /inbox web page. Same items,
same lifecycle (unread → read → resolved), and the same kind taxonomy
(waitpoint, escalation, failed_run, message).

Examples:
  crewship inbox list                       # show unread items
  crewship inbox list --state all           # include resolved
  crewship inbox list --kind waitpoint      # narrow by kind
  crewship inbox list --format json | jq    # script-friendly output
  crewship inbox read <id>                  # mark as read
  crewship inbox resolve <id> --action approved
  crewship inbox unread <id>                # flip back to unread

Status:
  list      — live (GET /api/v1/inbox)
  read      — live (PATCH /api/v1/inbox/{id} state=read)
  unread    — live (PATCH /api/v1/inbox/{id} state=unread)
  resolve   — live (PATCH /api/v1/inbox/{id} state=resolved)`,
}

var inboxListCmd = &cobra.Command{
	Use:   "list",
	Short: "List inbox items",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()

		state, _ := cmd.Flags().GetString("state")
		kind, _ := cmd.Flags().GetString("kind")
		limit, _ := cmd.Flags().GetInt("limit")

		q := url.Values{}
		q.Set("workspace_id", cli.ResolveWorkspace(flagWorkspace, cliCfg))
		if state != "" && state != "all" {
			q.Set("state", state)
		} else if state == "all" {
			q.Set("state", "all")
		}
		if kind != "" {
			q.Set("kind", kind)
		}
		if limit > 0 {
			q.Set("limit", fmt.Sprintf("%d", limit))
		}
		path := "/api/v1/inbox?" + q.Encode()

		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var body struct {
			Rows []struct {
				ID             string `json:"id"`
				Kind           string `json:"kind"`
				SourceID       string `json:"source_id"`
				Title          string `json:"title"`
				BodyMD         string `json:"body_md"`
				SenderName     string `json:"sender_name"`
				State          string `json:"state"`
				Priority       string `json:"priority"`
				Blocking       bool   `json:"blocking"`
				CreatedAt      string `json:"created_at"`
				ResolvedAction string `json:"resolved_action"`
			} `json:"rows"`
			Count       int `json:"count"`
			UnreadCount int `json:"unread_count"`
		}
		if err := cli.ReadJSON(resp, &body); err != nil {
			return err
		}

		f := newFormatter()
		if f.Format == "json" {
			return f.JSON(body.Rows)
		}
		if f.Format == "yaml" {
			return f.YAML(body.Rows)
		}
		// "quiet" suppresses table output entirely so a script can
		// pipe the exit status without parsing rows. Match approvals'
		// + journal's behavior so cross-command UX stays consistent.
		if f.Format == "quiet" {
			return nil
		}

		// Table output — color the STATE column so an unread waitpoint
		// is impossible to miss when the user is scanning a 50-row feed.
		// Same chip idiom as approvals.
		for _, r := range body.Rows {
			stateColor := cli.Gray
			switch r.State {
			case "unread":
				stateColor = cli.Yellow
			case "read":
				stateColor = cli.Cyan
			case "resolved":
				stateColor = cli.Green
			}
			kindColor := cli.Dim
			switch r.Kind {
			case "waitpoint":
				kindColor = cli.Yellow
			case "escalation":
				kindColor = cli.Red
			case "failed_run":
				kindColor = cli.Red
			case "message":
				kindColor = cli.Cyan
			}
			fmt.Printf("%s%-32s%s  %s[%-8s]%s  %s%-10s%s  %s%-9s%s  %-16s  %s\n",
				cli.Dim, truncateString(r.ID, 32), cli.Reset,
				stateColor, r.State, cli.Reset,
				kindColor, r.Kind, cli.Reset,
				cli.Bold, r.Priority, cli.Reset,
				truncateString(r.SenderName, 16),
				truncateString(r.Title, 60),
			)
		}
		fmt.Printf("\n%s%d items · %d unread%s\n", cli.Dim, body.Count, body.UnreadCount, cli.Reset)
		return nil
	},
}

var inboxReadCmd = &cobra.Command{
	Use:   "read <id>",
	Short: "Mark an inbox item as read (still visible, just no longer unread)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return patchInboxState(args[0], "read", "")
	},
}

var inboxUnreadCmd = &cobra.Command{
	Use:   "unread <id>",
	Short: "Flip an inbox item back to unread",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return patchInboxState(args[0], "unread", "")
	},
}

var inboxResolveCmd = &cobra.Command{
	Use:   "resolve <id>",
	Short: "Mark an inbox item resolved",
	Long: `Mark an inbox item as resolved. The optional --action records what
the user did so the audit trail records the decision shape, not just
"someone closed it":

  approved | denied | retried | cancelled | acknowledged | dismissed

This is the inbox-side resolve only — it does NOT call the source
endpoint (e.g. it won't approve a waitpoint through to the executor).
For source-aware actions, use the matching subcommand instead:

  crewship approvals approve <id>           # waitpoints via approvals queue
  crewship escalation resolve <id> ...      # escalations via escalation lifecycle

Examples:
  crewship inbox resolve <id> --action approved
  crewship inbox resolve <id> --action retried
  crewship inbox resolve <id>               # generic resolve, no action`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		action, _ := cmd.Flags().GetString("action")
		return patchInboxState(args[0], "resolved", action)
	},
}

// patchInboxState issues a PATCH to /api/v1/inbox/{id} with the new
// state + optional resolved_action. Shared by all three transition
// subcommands so the request shape stays in one place.
func patchInboxState(id, state, action string) error {
	if err := requireAuth(); err != nil {
		return err
	}
	if err := requireWorkspace(); err != nil {
		return err
	}
	client := newAPIClient()

	body := map[string]string{"state": state}
	if action != "" {
		body["resolved_action"] = action
	}
	path := "/api/v1/inbox/" + url.PathEscape(id) + "?workspace_id=" + url.QueryEscape(cli.ResolveWorkspace(flagWorkspace, cliCfg))
	resp, err := client.Patch(path, body)
	if err != nil {
		return err
	}
	if err := cli.CheckError(resp); err != nil {
		return err
	}
	cli.PrintSuccess(fmt.Sprintf("Inbox %s → %s", id, state))
	return nil
}

func init() {
	inboxListCmd.Flags().String("state", "unread", "Filter by state: unread|read|resolved|all")
	inboxListCmd.Flags().String("kind", "", "Filter by kind: waitpoint|escalation|failed_run|message")
	inboxListCmd.Flags().Int("limit", 50, "Max rows to return (server caps at 500)")

	inboxResolveCmd.Flags().String("action", "", "Action recorded with resolution: approved|denied|retried|cancelled|acknowledged|dismissed")

	inboxCmd.AddCommand(inboxListCmd)
	inboxCmd.AddCommand(inboxReadCmd)
	inboxCmd.AddCommand(inboxUnreadCmd)
	inboxCmd.AddCommand(inboxResolveCmd)
}
