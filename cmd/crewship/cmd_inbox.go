package main

import (
	"fmt"
	"net/url"
	"strings"

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

See also: 'crewship notification' for the low-level per-entity event
log. Notifications back the same flows as inbox items but at a
different granularity — notifications are entity-scoped, inbox is
human-attention-scoped.

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

// inboxCountCmd is the scriptable mirror of GET /api/v1/inbox/count —
// the bell-badge endpoint. Returns a single integer (or the full JSON
// object if --format json is set) so a shell can branch on the unread
// volume without parsing the full list payload.
//
// Note: client-side filters (--crew/--workspace/--priority/--blocking)
// were considered but the server doesn't filter inbox by any of those
// today — kind + state are the only supported narrowing parameters.
// We don't synthesise client-side filtering because the list endpoint
// caps at 500 rows, so a slow filter would silently miss items beyond
// the cap. If the server grows real filters, add the flags here.
var inboxCountCmd = &cobra.Command{
	Use:   "count",
	Short: "Print the number of unread inbox items (bell-badge endpoint)",
	Long: `Return the count of unread items in the workspace inbox. Backed by
GET /api/v1/inbox/count — cheaper than 'inbox list' for polling loops
that only need the bell-badge number.

Examples:
  crewship inbox count                  # prints the integer
  crewship inbox count --format json    # {"unread_count": N}`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Get("/api/v1/inbox/count")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var body struct {
			UnreadCount int `json:"unread_count"`
		}
		if err := cli.ReadJSON(resp, &body); err != nil {
			return err
		}
		f := newFormatter()
		if f.Format == "json" {
			return f.JSON(body)
		}
		if f.Format == "yaml" {
			return f.YAML(body)
		}
		fmt.Println(body.UnreadCount)
		return nil
	},
}

// inboxBulkCmd groups multi-item state transitions. The server has no
// /inbox/bulk endpoint today; we compose the per-item PATCH /inbox/{id}
// call instead. Failures are reported per-item but don't abort the
// loop — partial success is the realistic outcome (one stale id
// shouldn't void a 200-item clean-up).
var inboxBulkCmd = &cobra.Command{
	Use:   "bulk",
	Short: "Bulk operations on inbox items (read / resolve)",
	Long: `Apply the same state transition to multiple inbox items in one go.

The server has no dedicated bulk endpoint; this command iterates the
per-item PATCH /inbox/{id} call. Failures are reported per-item but do
NOT abort the loop — a stale id won't void a 200-item clean-up.

Examples:
  crewship inbox bulk read --ids id1,id2,id3
  crewship inbox bulk resolve --ids id1,id2 --action acknowledged
  crewship inbox bulk read --all-unread       # mark every unread item read`,
}

var inboxBulkReadCmd = &cobra.Command{
	Use:   "read",
	Short: "Bulk-mark inbox items as read",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runInboxBulk(cmd, "read", "")
	},
}

var inboxBulkResolveCmd = &cobra.Command{
	Use:   "resolve",
	Short: "Bulk-mark inbox items as resolved",
	RunE: func(cmd *cobra.Command, args []string) error {
		action, _ := cmd.Flags().GetString("action")
		return runInboxBulk(cmd, "resolved", action)
	},
}

// runInboxBulk resolves the target id set (either explicit --ids or
// every unread item via --all-unread), then issues a PATCH per id. We
// keep the loop sequential — concurrent PATCHes against the same
// inbox row would race on state, and the per-id cost is dominated by
// network latency anyway (bulk-reading 50 items takes ~5s, fine for
// a TUI flow). If this becomes a bottleneck, batch the PATCH calls
// behind a worker pool with a bounded fan-out.
func runInboxBulk(cmd *cobra.Command, state, action string) error {
	if err := requireAuth(); err != nil {
		return err
	}
	if err := requireWorkspace(); err != nil {
		return err
	}

	idsRaw, _ := cmd.Flags().GetString("ids")
	allUnread, _ := cmd.Flags().GetBool("all-unread")
	if idsRaw == "" && !allUnread {
		return fmt.Errorf("either --ids <csv> or --all-unread is required")
	}
	if idsRaw != "" && allUnread {
		return fmt.Errorf("--ids and --all-unread are mutually exclusive")
	}

	client := newAPIClient()

	// Build the target id list. --all-unread fetches the current unread
	// page (server-side, capped at 500) so a single CLI invocation can
	// clear the queue without the user copy-pasting ids by hand.
	const unreadPageCap = 500
	var ids []string
	if allUnread {
		q := url.Values{}
		q.Set("workspace_id", cli.ResolveWorkspace(flagWorkspace, cliCfg))
		q.Set("state", "unread")
		q.Set("limit", fmt.Sprintf("%d", unreadPageCap))
		resp, err := client.Get("/api/v1/inbox?" + q.Encode())
		if err != nil {
			return fmt.Errorf("list unread: %w", err)
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var body struct {
			Rows []struct {
				ID string `json:"id"`
			} `json:"rows"`
		}
		if err := cli.ReadJSON(resp, &body); err != nil {
			return err
		}
		for _, r := range body.Rows {
			ids = append(ids, r.ID)
		}
		if len(ids) == 0 {
			cli.PrintSuccess("Inbox is already clean — nothing to do.")
			return nil
		}
		// Server caps the page at 500 and exposes no cursor today. If
		// we hit the cap, refuse to silently miss the tail — make the
		// user re-run after this batch, or narrow with explicit --ids.
		if len(ids) == unreadPageCap {
			return fmt.Errorf(
				"more than %d unread items — re-run after this batch, or pass --ids to target a specific subset",
				unreadPageCap,
			)
		}
	} else {
		for _, raw := range strings.Split(idsRaw, ",") {
			id := strings.TrimSpace(raw)
			if id != "" {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			return fmt.Errorf("--ids parsed to empty list (got %q)", idsRaw)
		}
	}

	ok, fail := 0, 0
	for _, id := range ids {
		if err := patchInboxState(id, state, action); err != nil {
			fmt.Printf("%s%s%s  %s\n", cli.Red, id, cli.Reset, err)
			fail++
			continue
		}
		ok++
	}
	fmt.Printf("\n%s%d ok / %d failed%s\n", cli.Dim, ok, fail, cli.Reset)
	if fail > 0 {
		return fmt.Errorf("%d of %d items failed", fail, len(ids))
	}
	return nil
}

func init() {
	inboxListCmd.Flags().String("state", "unread", "Filter by state: unread|read|resolved|all")
	inboxListCmd.Flags().String("kind", "", "Filter by kind: waitpoint|escalation|failed_run|message")
	inboxListCmd.Flags().Int("limit", 50, "Max rows to return (server caps at 500)")

	inboxResolveCmd.Flags().String("action", "", "Action recorded with resolution: approved|denied|retried|cancelled|acknowledged|dismissed")

	inboxBulkReadCmd.Flags().String("ids", "", "Comma-separated inbox item IDs")
	inboxBulkReadCmd.Flags().Bool("all-unread", false, "Apply to every currently-unread item (server cap: 500 per call)")
	inboxBulkResolveCmd.Flags().String("ids", "", "Comma-separated inbox item IDs")
	inboxBulkResolveCmd.Flags().Bool("all-unread", false, "Apply to every currently-unread item (server cap: 500 per call)")
	inboxBulkResolveCmd.Flags().String("action", "", "Action recorded with each resolution: approved|denied|retried|cancelled|acknowledged|dismissed")

	inboxBulkCmd.AddCommand(inboxBulkReadCmd)
	inboxBulkCmd.AddCommand(inboxBulkResolveCmd)

	inboxCmd.AddCommand(inboxListCmd)
	inboxCmd.AddCommand(inboxReadCmd)
	inboxCmd.AddCommand(inboxUnreadCmd)
	inboxCmd.AddCommand(inboxResolveCmd)
	inboxCmd.AddCommand(inboxCountCmd)
	inboxCmd.AddCommand(inboxBulkCmd)
}
