package main

// `crewship work` — the CLI half of the durable work ledger
// (docs/prd/WEBHOOKS-AGENT-PARALLELISM-IMPLEMENTATION-1-0.md §9).
//
// Six endpoints, six commands. The ledger is the thing you reach for when an
// agent did not run and nobody can say why: it records what was accepted
// before anything started, so "we never received it", "we received it and
// filtered it", and "it ran and failed" are three different answers rather
// than one shrug.
//
// The command that had to be written carefully is `cancel`. Cancelling is a
// REQUEST, and the API answers with what it actually did — stopped it, asked
// and is waiting, or found it already finished. A CLI that printed
// "cancelled" for all three would be the most common way that distinction
// gets lost, since the CLI is what agents drive.

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// The wire shapes. Only the fields the CLI renders are declared; the API is
// free to add more, and `-f json` prints the decoded struct, so anything shown
// here is something a script can rely on.
//
// Every multi-word field carries an explicit `yaml:` tag because yaml.v3 does
// not read json tags — it lowercases the Go field name — so without them
// `-f json` and `-f yaml` print the same data under different keys, and a
// script written against one silently fails against the other.

// WorkItemRow is exported because workItemDetail embeds it and both are
// rendered as machine output. yaml.v3 cannot reflect into an unexported
// embedded struct, so `-f yaml` on a work item panicked while `-f json`
// worked — the shape of the whole class of defect TestEmbeddedJSONInlineIsAlsoYAMLSafe
// exists to catch.
type WorkItemRow struct {
	ID                 string  `json:"id"`
	Source             string  `json:"source"`
	SourceRef          string  `json:"source_ref" yaml:"source_ref"`
	DomainKind         string  `json:"domain_kind" yaml:"domain_kind"`
	DomainID           string  `json:"domain_id" yaml:"domain_id"`
	AgentID            string  `json:"agent_id" yaml:"agent_id"`
	CrewID             string  `json:"crew_id" yaml:"crew_id"`
	SessionID          string  `json:"session_id" yaml:"session_id"`
	Class              string  `json:"class"`
	AuthorizedByUserID string  `json:"authorized_by_user_id" yaml:"authorized_by_user_id"`
	InputSHA256        string  `json:"input_sha256" yaml:"input_sha256"`
	TargetRevision     string  `json:"target_revision" yaml:"target_revision"`
	State              string  `json:"state"`
	StateReason        string  `json:"state_reason" yaml:"state_reason"`
	Generation         int64   `json:"generation"`
	AttemptCount       int     `json:"attempt_count" yaml:"attempt_count"`
	Priority           int     `json:"priority"`
	EligibleAt         string  `json:"eligible_at" yaml:"eligible_at"`
	DeadlineAt         *string `json:"deadline_at" yaml:"deadline_at"`
	ReplayOf           *string `json:"replay_of" yaml:"replay_of"`
	ReplayReason       string  `json:"replay_reason" yaml:"replay_reason"`
	CreatedAt          string  `json:"created_at" yaml:"created_at"`
	UpdatedAt          string  `json:"updated_at" yaml:"updated_at"`
	TerminalAt         *string `json:"terminal_at" yaml:"terminal_at"`
}

type workAttemptRow struct {
	RunID          string  `json:"run_id" yaml:"run_id"`
	Attempt        int     `json:"attempt"`
	Generation     int64   `json:"generation"`
	LeaseOwner     string  `json:"lease_owner" yaml:"lease_owner"`
	LeaseExpiresAt string  `json:"lease_expires_at" yaml:"lease_expires_at"`
	RuntimeLocator string  `json:"runtime_locator" yaml:"runtime_locator"`
	StartedAt      string  `json:"started_at" yaml:"started_at"`
	EndedAt        *string `json:"ended_at" yaml:"ended_at"`
	EndReason      string  `json:"end_reason" yaml:"end_reason"`
	ExitEvidence   string  `json:"exit_evidence" yaml:"exit_evidence"`
	CostUSD        float64 `json:"cost_usd" yaml:"cost_usd"`
}

type workEventRow struct {
	Seq        int64  `json:"seq"`
	At         string `json:"at"`
	FromState  string `json:"from_state" yaml:"from_state"`
	ToState    string `json:"to_state" yaml:"to_state"`
	RunID      string `json:"run_id" yaml:"run_id"`
	Generation int64  `json:"generation"`
	Reason     string `json:"reason"`
}

type workItemDetail struct {
	WorkItemRow `json:",inline" yaml:",inline"`
	Attempts    []workAttemptRow `json:"attempts"`
	Events      []workEventRow   `json:"events"`
}

type workItemPageBody struct {
	Items      []WorkItemRow `json:"items"`
	NextCursor *string       `json:"next_cursor" yaml:"next_cursor"`
}

type workCancelBody struct {
	ID      string `json:"id"`
	State   string `json:"state"`
	Outcome string `json:"outcome"`
	Detail  string `json:"detail"`
}

type workDeliveryRow struct {
	ID               string  `json:"id"`
	EndpointID       string  `json:"endpoint_id" yaml:"endpoint_id"`
	EndpointKind     string  `json:"endpoint_kind" yaml:"endpoint_kind"`
	Profile          string  `json:"profile"`
	SourceDeliveryID string  `json:"source_delivery_id" yaml:"source_delivery_id"`
	EventType        string  `json:"event_type" yaml:"event_type"`
	EventAction      string  `json:"event_action" yaml:"event_action"`
	SigningKeyID     string  `json:"signing_key_id" yaml:"signing_key_id"`
	BodySHA256       string  `json:"body_sha256" yaml:"body_sha256"`
	BodyBytes        int64   `json:"body_bytes" yaml:"body_bytes"`
	FilterDecision   string  `json:"filter_decision" yaml:"filter_decision"`
	FilterReason     string  `json:"filter_reason" yaml:"filter_reason"`
	TargetRevision   string  `json:"target_revision" yaml:"target_revision"`
	WorkID           *string `json:"work_id" yaml:"work_id"`
	ReceivedAt       string  `json:"received_at" yaml:"received_at"`
	DedupExpiresAt   string  `json:"dedup_expires_at" yaml:"dedup_expires_at"`
	RawBodyAvailable bool    `json:"raw_body_available" yaml:"raw_body_available"`
	RawBodyExpiresAt *string `json:"raw_body_expires_at" yaml:"raw_body_expires_at"`
}

type workDeliveryPageBody struct {
	Items      []workDeliveryRow `json:"items"`
	NextCursor *string           `json:"next_cursor" yaml:"next_cursor"`
}

var workCmd = &cobra.Command{
	Use:   "work",
	Short: "Inspect the durable work ledger — what was accepted, what ran, and why it ended",
	Long: `Read the work ledger, and cancel or replay one item.

Every piece of agent work is recorded before anything starts running, so the
ledger can tell "we never received it" from "we filtered it" from "it ran and
failed". A work item keeps its id across every retry; a manual replay mints a
new one that points back at the original.

Nothing here returns the accepted input, the payload of a delivery, or model
output — only the fingerprints and the state.`,
}

// workshortID keeps a table readable without making the first column useless
// under -f quiet, where the whole point is to pipe ids into the next command.
func workShortID(f *cli.Formatter, id string) string {
	short := id
	if len(short) > 14 {
		short = short[:14] + "…"
	}
	return f.ShortID(id, short)
}

// workShortTime drops the fixed nine-digit fraction the ledger writes. The
// full value is one `-f json` away, and a table of 30-character timestamps
// pushes every other column off the terminal.
func workShortTime(ts string) string {
	if len(ts) >= 19 && strings.Contains(ts, "T") {
		return ts[:19] + "Z"
	}
	return ts
}

func workDeref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// workspacePath builds a workspace-scoped path. The client resolves a
// configured slug to its id, so a user who set `workspace: my-team` in the
// CLI config still gets a path the server's {workspaceId} matcher accepts.
func workspacePath(client *cli.Client, suffix string) string {
	return "/api/v1/workspaces/" + url.PathEscape(client.GetWorkspaceID()) + suffix
}

func workClient() (*cli.Client, error) {
	if err := requireAuth(); err != nil {
		return nil, err
	}
	if err := requireWorkspace(); err != nil {
		return nil, err
	}
	return newAPIClient(), nil
}

// workGetJSON runs one authenticated GET and decodes it.
func workGetJSON(client *cli.Client, path string, out any) error {
	resp, err := client.Get(path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return err
	}
	return cli.ReadJSON(resp, out)
}

var workListCmd = &cobra.Command{
	Use:   "list",
	Short: "List work items in the workspace",
	Long: `List accepted work, newest identifiers last.

Results are paginated at 100 rows. When more remain, the command prints the
cursor to pass back as --after.

An unrecognised --state, --class or --source is refused by the server rather
than answered with an empty page: "nothing is running" is the wrong answer to
a typo.`,
	Example: `  crewship work list
  crewship work list --state running
  crewship work list --source webhook --class background
  crewship work list --agent research-agent -f json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := workClient()
		if err != nil {
			return err
		}
		query := url.Values{}
		for flag, param := range map[string]string{
			"state": "state", "class": "class", "source": "source",
			"agent": "agent_id", "after": "after",
		} {
			if value, _ := cmd.Flags().GetString(flag); value != "" {
				query.Set(param, value)
			}
		}
		path := workspacePath(client, "/work-items")
		if encoded := query.Encode(); encoded != "" {
			path += "?" + encoded
		}
		var page workItemPageBody
		if err := workGetJSON(client, path, &page); err != nil {
			return err
		}
		f := resolvedFormatter(cmd)
		return f.AutoHuman(page, func() {
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tSTATE\tCLASS\tSOURCE\tAGENT\tATTEMPTS\tCREATED")
			for _, item := range page.Items {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%d\t%s\n",
					workShortID(f, item.ID), item.State, item.Class, item.Source,
					item.AgentID, item.AttemptCount, workShortTime(item.CreatedAt))
			}
			_ = w.Flush()
			if len(page.Items) == 0 {
				fmt.Println("(no work items)")
			}
			if page.NextCursor != nil {
				fmt.Printf("\nMore results: crewship work list --after %s\n", *page.NextCursor)
			}
		})
	},
}

var workGetCmd = &cobra.Command{
	Use:   "get <work-id>",
	Short: "Show one work item with its attempts and history",
	Long: `Show one work item, every attempt it made, and its whole state history.

The attempt rows carry the run id (the same identifier the journal uses), the
lease, the runtime locator recovery consults before deciding a process is
gone, and the cost. The history is append-only and in sequence order.

The accepted input is never shown — for a chat or assignment source it is the
conversation. Its fingerprint is.`,
	Example: `  crewship work get cwk0000000000000
  crewship work get cwk0000000000000 -f json | jq '.events[-1]'`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := workClient()
		if err != nil {
			return err
		}
		var detail workItemDetail
		if err := workGetJSON(client, workspacePath(client, "/work-items/"+url.PathEscape(args[0])), &detail); err != nil {
			return err
		}
		f := resolvedFormatter(cmd)
		return f.AutoHuman(detail, func() {
			pairs := [][]string{
				{"ID", detail.ID},
				{"State", detail.State},
				{"Reason", detail.StateReason},
				{"Class", detail.Class},
				{"Source", detail.Source + workSuffix(" ref ", detail.SourceRef)},
				{"Agent", detail.AgentID},
				{"Crew", detail.CrewID},
				{"Session", detail.SessionID},
				{"Authorized by", detail.AuthorizedByUserID},
				{"Input sha256", detail.InputSHA256},
				{"Target revision", detail.TargetRevision},
				{"Generation", fmt.Sprint(detail.Generation)},
				{"Attempts", fmt.Sprint(detail.AttemptCount)},
				{"Eligible at", workShortTime(detail.EligibleAt)},
				{"Deadline", workShortTime(workDeref(detail.DeadlineAt))},
				{"Replay of", workDeref(detail.ReplayOf) + workSuffix(" — ", detail.ReplayReason)},
				{"Created", workShortTime(detail.CreatedAt)},
				{"Terminal at", workShortTime(workDeref(detail.TerminalAt))},
			}
			f.Detail(pairs)

			fmt.Println("\nAttempts")
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			// The locator is in the table rather than only in -f json: an item
			// parked in needs_reconciliation cannot be diagnosed without it,
			// and that is exactly when somebody is reading this by eye.
			fmt.Fprintln(w, "#\tRUN\tGEN\tOWNER\tLOCATOR\tSTARTED\tENDED\tCOST\tEND REASON")
			for _, a := range detail.Attempts {
				fmt.Fprintf(w, "%d\t%s\t%d\t%s\t%s\t%s\t%s\t%.4f\t%s\n",
					a.Attempt, workShortID(f, a.RunID), a.Generation, a.LeaseOwner, a.RuntimeLocator,
					workShortTime(a.StartedAt), workShortTime(workDeref(a.EndedAt)), a.CostUSD, a.EndReason)
			}
			_ = w.Flush()
			if len(detail.Attempts) == 0 {
				fmt.Println("(never claimed)")
			}

			fmt.Println("\nHistory")
			w = tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "SEQ\tAT\tFROM\tTO\tREASON")
			for _, e := range detail.Events {
				fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\n",
					e.Seq, workShortTime(e.At), e.FromState, e.ToState, e.Reason)
			}
			_ = w.Flush()
		})
	},
}

func workSuffix(sep, value string) string {
	if value == "" {
		return ""
	}
	return sep + value
}

var workCancelCmd = &cobra.Command{
	Use:   "cancel <work-id>",
	Short: "Ask for a work item to stop",
	Long: `Request a stop. Safe to repeat.

Cancelling is a request, not an immediate result, and this command reports
which of three things happened:

  cancelled         the work had not started; it was stopped atomically.
  requested         the work holds a runtime. It has been signalled, and it
                    becomes cancelled only once the stop is CONFIRMED — or
                    needs_reconciliation if it cannot be.
  already_terminal  it finished first. The state shown is the real one.

Cancelling does not undo external effects that already happened.`,
	Example: `  crewship work cancel cwk0000000000000
  crewship work cancel cwk0000000000000 -f json | jq -r .outcome`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := workClient()
		if err != nil {
			return err
		}
		resp, err := client.Post(workspacePath(client, "/work-items/"+url.PathEscape(args[0])+"/cancel"), nil)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out workCancelBody
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			// The outcome leads, because it is the part that is easy to
			// misread as "done".
			switch out.Outcome {
			case "cancelled":
				cli.PrintSuccess(out.ID + " is cancelled (" + out.Detail + ")")
			case "requested":
				cli.PrintWarning(out.ID + " is still " + out.State + " — cancel requested, not yet confirmed")
			default:
				cli.PrintWarning(out.ID + " already finished as " + out.State + "; nothing was stopped")
			}
			fmt.Fprintln(os.Stderr, out.Detail)
		})
	},
}

var workReplayCmd = &cobra.Command{
	Use:   "replay <work-id>",
	Short: "Create a new work item from a finished one",
	Long: `Replay finished work as a NEW work item.

This is not a retry. A retry keeps the work id and mints a new run id, and the
queue does that on its own for a repeatable failure. A replay is a new
authorization: it runs under YOUR identity, not the original requester's, and
the new item records what it is a replay of and why.

--target-revision is inherited from the original unless you name one, because
running against a different revision has to be a deliberate act.

Replaying live work is refused — cancel it first. Replaying webhook-sourced
work whose raw payload has passed its retention is also refused, with the
expiry named: the original bytes are gone and only the sender can produce them
again.`,
	Example: `  crewship work replay cwk0000000000000 --reason "provider outage"
  crewship work replay cwk0000000000000 --target-revision 7`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := workClient()
		if err != nil {
			return err
		}
		body := map[string]any{}
		if reason, _ := cmd.Flags().GetString("reason"); reason != "" {
			body["reason"] = reason
		}
		if revision, _ := cmd.Flags().GetString("target-revision"); revision != "" {
			body["target_revision"] = revision
		}
		resp, err := client.Post(workspacePath(client, "/work-items/"+url.PathEscape(args[0])+"/replay"), body)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var created WorkItemRow
		if err := cli.ReadJSON(resp, &created); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(created, func() {
			cli.PrintSuccess("Replayed " + args[0] + " as " + created.ID + " (" + created.State + ")")
			fmt.Fprintln(os.Stderr, "Follow it with: crewship work get "+created.ID)
		})
	},
}

var workDeliveriesCmd = &cobra.Command{
	Use:   "deliveries",
	Short: "Inspect the webhook delivery ledger",
	Long: `Read the record of inbound webhooks.

Every delivery is recorded before anything is dispatched, including the ones
the filter rejected — so a ping or a filtered event is auditable rather than
invisible. The raw payload is never returned; the ledger reports whether it is
still held, which is what decides whether a replay can work.`,
}

var workDeliveriesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List recorded webhook deliveries",
	Long: `List deliveries, paginated at 100 rows.

Passing --endpoint together with --source-id asks the identity question — "did
you receive this one" — and answers with zero or one row. A sender's delivery
id is only unique within an endpoint, so --source-id on its own is refused.`,
	Example: `  crewship work deliveries list
  crewship work deliveries list --decision ignored
  crewship work deliveries list --endpoint ep_123 --source-id 8f4c1a2e-...`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := workClient()
		if err != nil {
			return err
		}
		query := url.Values{}
		for flag, param := range map[string]string{
			"endpoint": "endpoint_id", "source-id": "source_delivery_id",
			"decision": "decision", "event-type": "event_type", "after": "after",
		} {
			if value, _ := cmd.Flags().GetString(flag); value != "" {
				query.Set(param, value)
			}
		}
		path := workspacePath(client, "/webhook-deliveries")
		if encoded := query.Encode(); encoded != "" {
			path += "?" + encoded
		}
		var page workDeliveryPageBody
		if err := workGetJSON(client, path, &page); err != nil {
			return err
		}
		f := resolvedFormatter(cmd)
		return f.AutoHuman(page, func() {
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tENDPOINT\tPROFILE\tEVENT\tDECISION\tWORK\tPAYLOAD\tRECEIVED")
			for _, d := range page.Items {
				payload := "dropped"
				if d.RawBodyAvailable {
					payload = "held"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					workShortID(f, d.ID), d.EndpointID, d.Profile, d.EventType,
					d.FilterDecision, workShortID(f, workDeref(d.WorkID)), payload,
					workShortTime(d.ReceivedAt))
			}
			_ = w.Flush()
			if len(page.Items) == 0 {
				fmt.Println("(no deliveries)")
			}
			if page.NextCursor != nil {
				fmt.Printf("\nMore results: crewship work deliveries list --after %s\n", *page.NextCursor)
			}
		})
	},
}

var workDeliveriesGetCmd = &cobra.Command{
	Use:   "get <delivery-id>",
	Short: "Show one webhook delivery",
	Long: `Show one delivery record: which endpoint and profile accepted it, what the
filter decided and why, the work it produced, and whether its payload is still
held.

The payload itself is not returned. body_sha256 and body_bytes identify it
without publishing whatever the sender put inside.`,
	Example: `  crewship work deliveries get cdl0000000000000`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := workClient()
		if err != nil {
			return err
		}
		var d workDeliveryRow
		if err := workGetJSON(client, workspacePath(client, "/webhook-deliveries/"+url.PathEscape(args[0])), &d); err != nil {
			return err
		}
		f := resolvedFormatter(cmd)
		return f.AutoHuman(d, func() {
			payload := "dropped — a replay of this delivery is unavailable"
			if d.RawBodyAvailable {
				payload = "held"
				if d.RawBodyExpiresAt != nil && *d.RawBodyExpiresAt != "" {
					payload += " until " + workShortTime(*d.RawBodyExpiresAt)
				}
			}
			f.Detail([][]string{
				{"ID", d.ID},
				{"Endpoint", d.EndpointID + " (" + d.EndpointKind + ")"},
				{"Profile", d.Profile},
				{"Source delivery id", d.SourceDeliveryID},
				{"Event", strings.TrimSpace(d.EventType + " " + d.EventAction)},
				{"Signing key", d.SigningKeyID},
				{"Body", fmt.Sprintf("%s (%d bytes)", d.BodySHA256, d.BodyBytes)},
				{"Filter", d.FilterDecision + workSuffix(" — ", d.FilterReason)},
				{"Target revision", d.TargetRevision},
				{"Work", workDeref(d.WorkID)},
				{"Payload", payload},
				{"Received", workShortTime(d.ReceivedAt)},
				{"Dedup until", workShortTime(d.DedupExpiresAt)},
			})
		})
	},
}

func init() {
	workListCmd.Flags().String("state", "", "Filter by state: queued, starting, running, waiting, retry_wait, succeeded, failed, expired, cancelled, needs_reconciliation")
	workListCmd.Flags().String("class", "", "Filter by capacity class: chat or background")
	workListCmd.Flags().String("source", "", "Filter by producer: webhook, chat, assignment, schedule, pipeline_step, manual")
	workListCmd.Flags().String("agent", "", "Filter by the agent the work runs as")
	workListCmd.Flags().String("after", "", "Resume from a previous page's cursor")

	workReplayCmd.Flags().String("reason", "", "Why this is being replayed; recorded on the new work item")
	workReplayCmd.Flags().String("target-revision", "", "Replay against a different target revision (default: the original's)")

	workDeliveriesListCmd.Flags().String("endpoint", "", "Filter by endpoint id")
	workDeliveriesListCmd.Flags().String("source-id", "", "The sender's own delivery id; requires --endpoint")
	workDeliveriesListCmd.Flags().String("decision", "", "Filter by filter decision: accepted or ignored")
	workDeliveriesListCmd.Flags().String("event-type", "", "Filter by event type, e.g. issues or push")
	workDeliveriesListCmd.Flags().String("after", "", "Resume from a previous page's cursor")

	workDeliveriesCmd.AddCommand(workDeliveriesListCmd)
	workDeliveriesCmd.AddCommand(workDeliveriesGetCmd)

	workCmd.AddCommand(workListCmd)
	workCmd.AddCommand(workGetCmd)
	workCmd.AddCommand(workCancelCmd)
	workCmd.AddCommand(workReplayCmd)
	workCmd.AddCommand(workDeliveriesCmd)
}
