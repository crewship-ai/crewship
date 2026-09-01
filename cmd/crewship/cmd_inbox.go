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

See also: 'crewship notifychannel' for where those items are delivered
OUTWARD — email, webhook, chat and push. The inbox is the origin, the
journal is the record, a channel is the way out.

Examples:
  crewship inbox list                       # show unread items
  crewship inbox list --state all           # include resolved
  crewship inbox list --kind waitpoint      # narrow by kind
  crewship inbox list --format json | jq    # script-friendly output
  crewship inbox list --all                 # walk every page, not just the first
  crewship inbox list --offset 50           # skip the first 50 rows
  crewship inbox read <id>                  # mark as read
  crewship inbox resolve <id> --action approved
  crewship inbox unread <id>                # flip back to unread

Status:
  list      — live (GET /api/v1/inbox)
  get       — live (GET /api/v1/inbox/{id})
  read      — live (PATCH /api/v1/inbox/{id} state=read)
  unread    — live (PATCH /api/v1/inbox/{id} state=unread)
  resolve   — live (PATCH /api/v1/inbox/{id} state=resolved)
  archive   — live (PATCH /api/v1/inbox/{id} state=resolved action=archived)`,
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
		offset, _ := cmd.Flags().GetInt("offset")
		fetchAll, _ := cmd.Flags().GetBool("all")

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
		// Field tags carry explicit yaml:"..." matching each json:"..." tag —
		// gopkg.in/yaml.v3 does NOT fall back to a json tag when no yaml
		// tag is present, it lowercases the raw Go field name instead
		// (SourceID -> "sourceid" rather than "source_id"). Without the
		// yaml tag, --format yaml and --format json return the same data
		// under different key casing (#1211).
		var body struct {
			Rows []struct {
				ID             string                 `json:"id" yaml:"id"`
				Kind           string                 `json:"kind" yaml:"kind"`
				SourceID       string                 `json:"source_id" yaml:"source_id"`
				Title          string                 `json:"title" yaml:"title"`
				BodyMD         string                 `json:"body_md" yaml:"body_md"`
				SenderType     string                 `json:"sender_type" yaml:"sender_type"`
				SenderName     string                 `json:"sender_name" yaml:"sender_name"`
				AvatarSeed     string                 `json:"avatar_seed,omitempty" yaml:"avatar_seed,omitempty"`
				AvatarStyle    string                 `json:"avatar_style,omitempty" yaml:"avatar_style,omitempty"`
				State          string                 `json:"state" yaml:"state"`
				Priority       string                 `json:"priority" yaml:"priority"`
				Blocking       bool                   `json:"blocking" yaml:"blocking"`
				Payload        map[string]interface{} `json:"payload" yaml:"payload"`
				CreatedAt      string                 `json:"created_at" yaml:"created_at"`
				ResolvedAction string                 `json:"resolved_action" yaml:"resolved_action"`
				// See the note on the `get` struct below: the API sends these,
				// the CLI used to drop them.
				TargetRole string `json:"target_role,omitempty" yaml:"target_role,omitempty"`
				ResolvedBy string `json:"resolved_by_user_id,omitempty" yaml:"resolved_by_user_id,omitempty"`
				ResolvedAt string `json:"resolved_at,omitempty" yaml:"resolved_at,omitempty"`
			} `json:"rows" yaml:"rows"`
			Count       int  `json:"count" yaml:"count"`
			UnreadCount int  `json:"unread_count" yaml:"unread_count"`
			HasMore     bool `json:"has_more" yaml:"has_more"`
		}

		// The API grew offset/has_more pagination for the web inbox, which
		// walks every page on load. Without --offset/--all the CLI could not
		// reach past the first --limit rows, so the two clients disagreed
		// about what "the whole feed" is and a scripted triage job silently
		// worked on a prefix.
		//
		// Offset paging is not stable across a mutating feed: a row that
		// leaves the filter between two requests shifts the window and is
		// never returned. --all is exact only on a quiet workspace; the real
		// fix is keyset pagination, server-side.
		acc := body.Rows[:0]
		unread := 0
		for {
			if offset > 0 {
				q.Set("offset", fmt.Sprintf("%d", offset))
			}
			resp, err := client.Get("/api/v1/inbox?" + q.Encode())
			if err != nil {
				return err
			}
			if err := cli.CheckError(resp); err != nil {
				return err
			}
			body.Rows = nil
			if err := cli.ReadJSON(resp, &body); err != nil {
				return err
			}
			acc = append(acc, body.Rows...)
			unread = body.UnreadCount
			if !fetchAll || !body.HasMore || len(body.Rows) == 0 {
				break
			}
			offset += len(body.Rows)
		}
		body.Rows = acc
		body.Count = len(acc)
		body.UnreadCount = unread

		f := newFormatter()
		// "quiet" suppresses table output entirely so a script can
		// pipe the exit status without parsing rows. Match approvals'
		// + journal's behavior so cross-command UX stays consistent.
		if f.Format == "quiet" {
			return nil
		}
		return f.AutoHuman(body.Rows, func() {
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
		})
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
  crewship approvals deny <id>              # (both incl. ephemeral-hire reviews)
  crewship escalation resolve <id> ...      # escalations via escalation lifecycle
  crewship hire approve <agent-id>          # ephemeral-hire PENDING_REVIEW waitpoints

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

// tierReason names the credential tier that forces a second approver, using
// the label the SERVER sent. The CLI deliberately keeps no copy of which tier
// that is: against a server whose tier table has moved, a locally-derived "L4"
// would be a confident wrong answer.
// fourEyesAgent names the agent the four-eyes rule is about: whoever OWNS it
// cannot resolve. The payload's agent_name is authoritative because the server
// compares against agents.created_by_user_id of the REQUESTING agent, and on a
// keeper credential escalation the sender is the keeper rather than that agent.
// Falls back to the sender for the escalations-backed flow, where the two are
// the same, and to a description when neither is known.
func fourEyesAgent(payload map[string]any, sender string) string {
	if payload != nil {
		if name, ok := payload["agent_name"].(string); ok && name != "" {
			return name
		}
	}
	if sender != "" {
		return sender
	}
	return "the agent that raised this"
}

func tierReason(label string) string {
	if label == "" {
		return "the credential's tier"
	}
	return label + " credentials"
}

// inboxGetCmd is the read-detail counterpart of `inbox list`: it fetches
// ONE item with its full body + payload (the context the list view
// omits), giving the CLI parity with the web detail pane. An agent
// triaging via CLI uses this to read the change plan / escalation context
// before deciding. Backed by GET /api/v1/inbox/{id}.
var inboxGetCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Show a single inbox item with its full body and context",
	Long: `Fetch one inbox item by id, including its markdown body and the
structured payload (Context) that 'inbox list' leaves out. This is the
CLI counterpart of the web detail pane.

Examples:
  crewship inbox get <id>
  crewship inbox get <id> --format json | jq .payload`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		path := "/api/v1/inbox/" + url.PathEscape(args[0]) +
			"?workspace_id=" + url.QueryEscape(cli.ResolveWorkspace(flagWorkspace, cliCfg))
		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		// See the matching comment on inboxListCmd's row struct above —
		// explicit yaml tags are required here for the same reason (#1211).
		var item struct {
			ID             string                 `json:"id" yaml:"id"`
			Kind           string                 `json:"kind" yaml:"kind"`
			SourceID       string                 `json:"source_id" yaml:"source_id"`
			Title          string                 `json:"title" yaml:"title"`
			BodyMD         string                 `json:"body_md" yaml:"body_md"`
			SenderType     string                 `json:"sender_type" yaml:"sender_type"`
			SenderName     string                 `json:"sender_name" yaml:"sender_name"`
			AvatarSeed     string                 `json:"avatar_seed,omitempty" yaml:"avatar_seed,omitempty"`
			AvatarStyle    string                 `json:"avatar_style,omitempty" yaml:"avatar_style,omitempty"`
			State          string                 `json:"state" yaml:"state"`
			Priority       string                 `json:"priority" yaml:"priority"`
			Blocking       bool                   `json:"blocking" yaml:"blocking"`
			ResolvedAction string                 `json:"resolved_action" yaml:"resolved_action"`
			CreatedAt      string                 `json:"created_at" yaml:"created_at"`
			Payload        map[string]interface{} `json:"payload" yaml:"payload"`
			// The API has always sent these three; the CLI dropped them, so an
			// operator (or an agent) driving through the CLI could not see who a
			// row was addressed to, who closed it, or when. Addressing is the
			// difference between "someone else may take this" and "only I can",
			// and it is what the target-role/decider contract is checked against.
			TargetRole string `json:"target_role,omitempty" yaml:"target_role,omitempty"`
			ResolvedBy string `json:"resolved_by_user_id,omitempty" yaml:"resolved_by_user_id,omitempty"`
			ResolvedAt string `json:"resolved_at,omitempty" yaml:"resolved_at,omitempty"`
			// Four-eyes on a credential escalation (#1574), as the server
			// computes it at read time. The CLI is the third surface offering a
			// resolve on this row, and it was the third one saying nothing about
			// the refusal that resolve would answer with.
			SecondApproverRequired    bool   `json:"second_approver_required,omitempty" yaml:"second_approver_required,omitempty"`
			SecondApproverByWorkspace bool   `json:"second_approver_by_workspace,omitempty" yaml:"second_approver_by_workspace,omitempty"`
			SecondApproverByTier      bool   `json:"second_approver_by_tier,omitempty" yaml:"second_approver_by_tier,omitempty"`
			SecurityLevelLabel        string `json:"security_level_label,omitempty" yaml:"security_level_label,omitempty"`
			// Evidence is the facts block: the consequences of granting this
			// credential, as query results rather than advice. Pointers all the way
			// down because absent ("the query failed, nobody knows") and
			// exists:false ("we looked, there is none") are different answers, and
			// flattening them turns a database outage into an argument.
			Evidence *struct {
				LastBackup *struct {
					Exists   bool   `json:"exists" yaml:"exists"`
					AgeHours int    `json:"age_hours" yaml:"age_hours"`
					Scope    string `json:"scope" yaml:"scope"`
				} `json:"last_backup,omitempty" yaml:"last_backup,omitempty"`
				NarrowerCredential *struct {
					Exists        bool   `json:"exists" yaml:"exists"`
					Name          string `json:"name,omitempty" yaml:"name,omitempty"`
					SecurityLevel int    `json:"security_level,omitempty" yaml:"security_level,omitempty"`
				} `json:"narrower_credential,omitempty" yaml:"narrower_credential,omitempty"`
			} `json:"evidence,omitempty" yaml:"evidence,omitempty"`
		}
		if err := cli.ReadJSON(resp, &item); err != nil {
			return err
		}

		f := newFormatter()
		if f.Format == "quiet" {
			return nil
		}
		return f.AutoHuman(item, func() {
			// Human detail view.
			fmt.Printf("%s%s%s\n", cli.Bold, item.Title, cli.Reset)
			fmt.Printf("%s%s · %s · %s%s\n", cli.Dim, item.Kind, item.State, item.Priority, cli.Reset)
			from := item.SenderName
			if from == "" {
				from = item.SenderType
			}
			if from != "" {
				fmt.Printf("%sfrom %s%s\n", cli.Dim, from, cli.Reset)
			}
			fmt.Printf("%sid %s%s\n", cli.Dim, item.ID, cli.Reset)
			if item.ResolvedAction != "" {
				fmt.Printf("%sresolved · %s%s\n", cli.Green, item.ResolvedAction, cli.Reset)
			}
			// Before the body, because it decides whether resolving this from
			// here will work at all. Silent against a pre-#1574 server rather
			// than guessing — the CLI has no copy of the rule to guess with.
			if item.SecondApproverRequired {
				why := "workspace policy"
				switch {
				case item.SecondApproverByTier && item.SecondApproverByWorkspace:
					why = "workspace policy + " + tierReason(item.SecurityLevelLabel)
				case item.SecondApproverByTier:
					why = tierReason(item.SecurityLevelLabel)
				}
				// The agent whose OWNER is refused — not the sender. On a keeper
				// credential escalation the keeper raises the item, so SenderName
				// is "Keeper" while the server compares the approver against the
				// owner of the agent that ASKED. "whoever owns Keeper cannot
				// resolve it" names nobody: the Keeper has no owner, and Riley
				// does. A security notice pointing at the wrong party is as bad as
				// one that never renders.
				who := fourEyesAgent(item.Payload, from)
				fmt.Printf("%s2nd approver required · %s%s\n", cli.Yellow, why, cli.Reset)
				fmt.Printf("%swhoever owns %s cannot resolve it — someone else has to%s\n",
					cli.Dim, who, cli.Reset)
			}
			// Before the body for the same reason the four-eyes notice is: these
			// decide whether to press the button, and the body is the model's case
			// for the verdict it already reached.
			if ev := item.Evidence; ev != nil {
				fmt.Printf("\n%sChecked against the database — not the agent's account%s\n", cli.Dim, cli.Reset)
				if b := ev.LastBackup; b != nil {
					if b.Exists {
						// The scope qualifier travels with the number: backup_catalog is
						// scoped to a workspace, never a table, and "6h ago" read as
						// "this table can be restored" is the invention to avoid.
						fmt.Printf("  last backup          %dh ago (%s-wide, not this table)\n", b.AgeHours, b.Scope)
					} else {
						fmt.Printf("  last backup          %snone recorded%s\n", cli.Yellow, cli.Reset)
					}
				}
				if n := ev.NarrowerCredential; n != nil {
					if n.Exists {
						fmt.Printf("  narrower credential  %s (L%d)\n", n.Name, n.SecurityLevel)
					} else {
						fmt.Printf("  narrower credential  none for this provider\n")
					}
				}
			}
			if item.BodyMD != "" {
				fmt.Printf("\n%s\n", item.BodyMD)
			}
			if len(item.Payload) > 0 {
				fmt.Printf("\n%sContext:%s\n", cli.Bold, cli.Reset)
				for k, v := range item.Payload {
					fmt.Printf("  %s%-18s%s %v\n", cli.Dim, k, cli.Reset, v)
				}
			}
		})
	},
}

// inboxArchiveCmd is the CLI mirror of the web "Archive" action — the
// Gmail-style "get it out of my inbox without making a decision" move.
// It's resolve with a dedicated resolved_action ("archived") so the
// audit trail distinguishes an archive from an explicit approve/dismiss,
// and the web Archived tab (state=resolved) picks it up. Restore with
// `crewship inbox unread <id>`.
//
// Only non-decision kinds archive: a waitpoint/escalation is source-
// managed (the server 409s a resolved PATCH for those), so this is for
// messages, failed-run notices, and advisories.
var inboxArchiveCmd = &cobra.Command{
	Use:   "archive <id>",
	Short: "Archive an inbox item (clear it from the inbox without a decision)",
	Long: `Archive an inbox item — move it out of the active inbox into the
Archived view without recording an approve/deny/dismiss decision. This
is the CLI counterpart of the web Archive button.

Archiving maps to resolve with action=archived. Restore an archived
item with 'crewship inbox unread <id>'.

Only non-decision items can be archived (messages, failed-run notices,
advisories). Waitpoints, escalations, and ephemeral-hire PENDING_REVIEW
items are source-managed — resolve those through their decision flow
instead: 'crewship approvals approve <id>' or 'crewship approvals deny
<id>' (hire reviews appear there too), 'crewship escalation resolve
<id>', or 'crewship hire approve <agent-id>'.

Examples:
  crewship inbox archive <id>
  crewship inbox unread <id>     # restore an archived item`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return patchInboxState(args[0], "resolved", "archived")
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
		return f.AutoHuman(body, func() {
			fmt.Println(body.UnreadCount)
		})
	},
}

// inboxBulkCmd groups multi-item state transitions. It submits the id set to
// the server-side POST /api/v1/inbox/bulk endpoint (one request flips many
// rows) in chunks of 500 — the endpoint's id cap. The server SKIPS, never
// closes, anything that needs an explicit human decision (source-managed
// waitpoints/escalations + blocking rows) on a resolve, and reports
// updated / skipped / not_found counts.
var inboxBulkCmd = &cobra.Command{
	Use:   "bulk",
	Short: "Bulk operations on inbox items (read / resolve)",
	Long: `Apply the same state transition to multiple inbox items in one go.

Backed by POST /api/v1/inbox/bulk — a single request flips the whole set
(chunked at 500 ids per call). On a resolve the server skips decision
items (source-managed waitpoints/escalations and blocking rows) rather
than closing them, and reports updated / skipped / not_found counts.

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

// inboxBulkChunk matches the server's /inbox/bulk id cap (bulkMaxIDs). A
// larger --ids set is submitted in chunks of this size so the request never
// trips the 400 "too many ids" guard.
const inboxBulkChunk = 500

// runInboxBulk resolves the target id set (either explicit --ids or every
// unread item via --all-unread), then submits them to the server-side
// POST /api/v1/inbox/bulk endpoint in chunks of 500 (the endpoint's id cap).
// One request flips many rows atomically; the server applies the same
// decision-item protection it uses for the web UI (source-managed kinds +
// blocking rows are SKIPPED on resolve, never closed), so the response
// reports updated / skipped / not_found counts which we aggregate.
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
	// clear the queue without the user copy-pasting ids by hand. We leave
	// workspace_id off the query so the client injects the resolved
	// workspace id (CUID) — passing the raw configured value here can be a
	// slug, which is the inbox-403 trap the bulk flow used to hit.
	var ids []string
	if allUnread {
		q := url.Values{}
		q.Set("state", "unread")
		q.Set("limit", fmt.Sprintf("%d", inboxBulkChunk))
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
		if len(ids) == inboxBulkChunk {
			return fmt.Errorf(
				"hit the %d unread-item page cap; no items were changed because this command cannot tell whether more unread items exist — pass --ids to target a specific subset",
				inboxBulkChunk,
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

	totalUpdated, totalSkipped, totalNotFound := 0, 0, 0
	for start := 0; start < len(ids); start += inboxBulkChunk {
		end := start + inboxBulkChunk
		if end > len(ids) {
			end = len(ids)
		}
		reqBody := map[string]interface{}{
			"ids":   ids[start:end],
			"state": state,
		}
		if action != "" {
			reqBody["resolved_action"] = action
		}
		resp, err := client.Post("/api/v1/inbox/bulk", reqBody)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out struct {
			Updated  int `json:"updated"`
			Skipped  int `json:"skipped"`
			NotFound int `json:"not_found"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		totalUpdated += out.Updated
		totalSkipped += out.Skipped
		totalNotFound += out.NotFound
	}

	fmt.Printf("\n%s%d updated · %d skipped · %d not found%s\n",
		cli.Dim, totalUpdated, totalSkipped, totalNotFound, cli.Reset)
	return nil
}

func init() {
	inboxListCmd.Flags().String("state", "unread", "Filter by state: unread|read|resolved|all")
	inboxListCmd.Flags().String("kind", "", "Filter by kind: waitpoint|escalation|failed_run|message")
	inboxListCmd.Flags().Int("limit", 50, "Max rows to return (server caps at 500)")
	inboxListCmd.Flags().Int("offset", 0, "Skip this many rows (server-side offset pagination)")
	inboxListCmd.Flags().Bool("all", false, "Walk every page until the server reports has_more=false")

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
	inboxCmd.AddCommand(inboxGetCmd)
	inboxCmd.AddCommand(inboxResolveCmd)
	inboxCmd.AddCommand(inboxArchiveCmd)
	inboxCmd.AddCommand(inboxCountCmd)
	inboxCmd.AddCommand(inboxBulkCmd)
}
