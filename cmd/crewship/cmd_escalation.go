package main

import (
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

var escalationCmd = &cobra.Command{
	Use:   "escalation",
	Short: "Manage crew escalations",
}

// escalationListCmd lists escalations under a single crew. The server
// route is /api/v1/crews/{crewId}/escalations and accepts ?status= as
// the canonical narrowing filter. --limit and --since are applied
// client-side because the server endpoint doesn't yet support them
// (audit gap noted in the task) — both are best-effort guards against
// runaway output, not a substitute for server-side pagination.
var escalationListCmd = &cobra.Command{
	Use:   "list",
	Short: "List escalations for a crew",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		crewSlug, _ := cmd.Flags().GetString("crew")
		statusFilter, _ := cmd.Flags().GetString("status")
		limit, _ := cmd.Flags().GetInt("limit")
		since, _ := cmd.Flags().GetString("since")

		if crewSlug == "" {
			return fmt.Errorf("--crew is required (crew slug or ID)")
		}

		var sinceTime time.Time
		var sinceSet bool
		if since != "" {
			t, err := parseSince(since)
			if err != nil {
				return fmt.Errorf("bad --since: %w", err)
			}
			sinceTime = t
			sinceSet = true
		}

		client := newAPIClient()

		crewID, err := resolveCrewID(client, crewSlug)
		if err != nil {
			return err
		}
		path := "/api/v1/crews/" + crewID + "/escalations"

		if statusFilter != "" {
			path += "?status=" + url.QueryEscape(statusFilter)
		}

		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		// Mirror of the API's escalationItem (internal/api/escalation_handler.go
		// ListEscalations) — --format json must pass every server field
		// through. The CLI used to re-marshal a truncated subset, which made
		// e.g. filtering CREDENTIAL escalations by .type impossible.
		var escalations []struct {
			ID                 string  `json:"id"`
			Type               string  `json:"type"`
			FromName           string  `json:"from_name"`
			FromSlug           string  `json:"from_slug"`
			Reason             string  `json:"reason"`
			Context            *string `json:"context"`
			Metadata           *string `json:"metadata"`
			PeerConversationID *string `json:"peer_conversation_id"`
			Status             string  `json:"status"`
			Resolution         *string `json:"resolution"`
			Action             *string `json:"action"`
			RedirectTo         *string `json:"redirect_to"`
			ResolvedBy         *string `json:"resolved_by"`
			ResolvedAt         *string `json:"resolved_at"`
			CreatedAt          string  `json:"created_at"`
			CredentialID       *string `json:"credential_id"`
			// The two clocks. DeadlineAt bounds the AGENT's long poll;
			// AnswerDeadlineAt is when the question stops being answerable
			// by a human, and AgentGaveUpAt says the asking run already
			// continued without an answer. Null on rows raised before the
			// respective columns existed.
			DeadlineAt       *string `json:"deadline_at"`
			AnswerDeadlineAt *string `json:"answer_deadline_at"`
			AgentGaveUpAt    *string `json:"agent_gave_up_at"`
		}
		if err := cli.ReadJSON(resp, &escalations); err != nil {
			return err
		}

		// Client-side --since / --limit. Cheaper than asking the server
		// to grow new filter params right now; if the dataset balloons,
		// promote to server-side filters.
		if sinceSet {
			kept := escalations[:0]
			for _, e := range escalations {
				if t, err := time.Parse(time.RFC3339Nano, e.CreatedAt); err == nil && !t.Before(sinceTime) {
					kept = append(kept, e)
				}
			}
			escalations = kept
		}
		if limit > 0 && len(escalations) > limit {
			escalations = escalations[:limit]
		}

		f := newFormatter()
		// ANSWER BY, not DEADLINE. "When does this stop being answerable" is
		// the question an operator scanning PENDING rows most needs answered,
		// and the column used to print `deadline_at` — which bounds the
		// AGENT's long poll and runs out in 300 s. An operator reading that as
		// their own countdown is the console half of the bug the two clocks
		// fixed in the server, so the table shows the human's clock and the
		// agent's give-up is annotated on the status instead.
		headers := []string{"ID", "TYPE", "FROM", "REASON", "STATUS", "ANSWER BY", "CREATED"}
		var rows [][]string
		for _, e := range escalations {
			reason := e.Reason
			if len(reason) > 50 {
				reason = reason[:47] + "..."
			}
			deadline := "—"
			if e.AnswerDeadlineAt != nil && *e.AnswerDeadlineAt != "" {
				deadline = *e.AnswerDeadlineAt
			}
			// Still answerable, and answering is still worth doing — but the
			// run that asked has moved on and will not receive it.
			status := e.Status
			if e.AgentGaveUpAt != nil && *e.AgentGaveUpAt != "" && e.Status == "PENDING" {
				status += " (agent moved on)"
			}
			// #1199: show the full ID. Escalation IDs are short cuids
			// (~21 chars), not "absurdly long" — truncating them here
			// used to produce a value that isn't resolvable by
			// `escalation resolve` at all (false 404), since the
			// resolve endpoint requires an exact ID and there's no
			// prefix-matching fallback like `mission get` has.
			rows = append(rows, []string{e.ID, e.Type, e.FromSlug, reason, status, deadline, e.CreatedAt})
		}
		return f.Auto(escalations, headers, rows)
	},
}

var escalationResolveCmd = &cobra.Command{
	Use:   "resolve <id>",
	Short: "Mark an escalation as resolved",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		resolution, _ := cmd.Flags().GetString("resolution")
		action, _ := cmd.Flags().GetString("action")
		redirectTo, _ := cmd.Flags().GetString("redirect-to")
		// Guard the action/redirect-to combinations before the PATCH. The API
		// defaults a missing action to "approve", so without these checks
		// `--redirect-to x` (no --action) would silently APPROVE instead of
		// redirect.
		switch action {
		case "", "approve", "reject", "redirect":
		default:
			return fmt.Errorf("--action must be approve, reject, or redirect")
		}
		if redirectTo != "" && action != "redirect" {
			return fmt.Errorf("--redirect-to requires --action redirect")
		}
		if action == "redirect" && redirectTo == "" {
			return fmt.Errorf("--action redirect requires --redirect-to")
		}
		body := map[string]interface{}{}
		if resolution != "" {
			body["resolution"] = resolution
		}
		if action != "" {
			body["action"] = action
		}
		if redirectTo != "" {
			body["redirect_to"] = redirectTo
		}

		client := newAPIClient()
		resp, err := client.Patch("/api/v1/escalations/"+args[0]+"/resolve", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		// The server says whether the answer reached the run that asked. A
		// decision made after the agent's wait window closed is still recorded
		// and a staged credential is still activated — but the operator must
		// not walk away thinking they unblocked that run, so the note is
		// printed rather than swallowed with the body.
		var out struct {
			AgentStillWaiting *bool  `json:"agent_still_waiting"`
			Note              string `json:"note"`
		}
		_ = cli.ReadJSON(resp, &out)

		cli.PrintSuccess(fmt.Sprintf("Escalation %s resolved.", args[0]))
		if out.Note != "" {
			fmt.Println(out.Note)
		}
		return nil
	},
}

// escalationCancelCmd withdraws a question instead of deciding it.
//
// Deliberately not `resolve --action cancel`: an agent reading action=reject
// has been told "no, do not do that", which it should act on. A cancellation
// says the question stopped mattering — nobody ever considered it — and
// collapsing the two would have the CLI lie to the agent on the operator's
// behalf.
var escalationCancelCmd = &cobra.Command{
	Use:   "cancel <id>",
	Short: "Withdraw an escalation without deciding it",
	Long: `Withdraw a question an agent asked, without answering it.

Use this when the question stopped mattering — the deploy was rolled back, the
task was reassigned, the agent was restarted. The escalation reaches the
terminal state CANCELLED, any agent still waiting is unblocked with an explicit
"no answer" warning, and the withdrawal is journaled with your user id.

To say NO to the agent, resolve it instead:
  crewship escalation resolve <id> --action reject --resolution "..."

Examples:
  crewship escalation cancel esc_abc
  crewship escalation cancel esc_abc --reason "the deploy was rolled back"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		reason, _ := cmd.Flags().GetString("reason")
		body := map[string]interface{}{}
		if reason != "" {
			body["reason"] = reason
		}

		client := newAPIClient()
		resp, err := client.Post("/api/v1/escalations/"+args[0]+"/cancel", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess(fmt.Sprintf("Escalation %s cancelled.", args[0]))
		return nil
	},
}

// escalationSweepExpiredCmd forces the deadline sweep now.
//
// The server runs this on a timer and every escalation read path triggers it
// for its own workspace, so an operator rarely needs it. It exists because a
// deadline that can only be observed by waiting is a deadline nobody can
// verify — this is how you check the mechanism is alive, and how an
// acceptance test drives it through the binary.
var escalationSweepExpiredCmd = &cobra.Command{
	Use:   "sweep-expired",
	Short: "Expire every escalation whose answer deadline has passed",
	Long: `Move every PENDING escalation past its ANSWER deadline in this workspace to EXPIRED.

An escalation carries two clocks: deadline_at bounds the agent's long poll
(300 s), and answer_deadline_at is how long a human can still answer (7 days).
Only the second one is swept. An agent giving up on a poll is not a human
declining to answer, and expiring on the first is what once made "Approve"
return 409 five minutes after an agent asked.

A question nobody answers before its ANSWER deadline is terminal: the row must
stop claiming somebody might still decide it, and a CREDENTIAL escalation
disposes of its staged credential on the way out so no unreachable secret is
left in the vault.

The server sweeps on its own timer and on every escalation read, so this is a
diagnostic and a forcing function, not routine maintenance. ADMIN+.

  crewship escalation sweep-expired                # prints the number expired
  crewship escalation sweep-expired --format json  # {"expired": N}`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Post("/api/v1/escalations/sweep-expired", map[string]interface{}{})
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var body struct {
			Expired int `json:"expired"`
		}
		if err := cli.ReadJSON(resp, &body); err != nil {
			return err
		}
		f := newFormatter()
		return f.AutoHuman(body, func() { fmt.Println(strconv.Itoa(body.Expired)) })
	},
}

// escalationPendingCountCmd hits the workspace-wide aggregator at
// GET /api/v1/escalations/pending-count. Drives dashboard tiles and
// alerting that needs "how many escalations are unresolved across all
// crews" without per-crew fan-out.
var escalationPendingCountCmd = &cobra.Command{
	Use:   "pending-count",
	Short: "Print the count of unresolved escalations across all crews in the workspace",
	Long: `Return the workspace-wide pending escalation count. Backed by
GET /api/v1/escalations/pending-count — cheaper than enumerating per-
crew lists when you only need the dashboard number.

Examples:
  crewship escalation pending-count             # prints the integer
  crewship escalation pending-count --format json    # {"count": N}`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Get("/api/v1/escalations/pending-count")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var body struct {
			Count int `json:"count"`
		}
		if err := cli.ReadJSON(resp, &body); err != nil {
			return err
		}
		f := newFormatter()
		return f.AutoHuman(body, func() { fmt.Println(strconv.Itoa(body.Count)) })
	},
}

func init() {
	escalationListCmd.Flags().String("crew", "", "Filter by crew slug or ID")
	escalationListCmd.Flags().String("status", "", "Filter by status: PENDING|RESOLVED|EXPIRED|CANCELLED")
	escalationListCmd.Flags().Int("limit", 0, "Cap rows returned client-side (0 = unbounded)")
	escalationListCmd.Flags().String("since", "", "Only entries newer than this (RFC3339 or 1h/24h/7d duration)")

	escalationResolveCmd.Flags().String("resolution", "", "Resolution notes")
	escalationResolveCmd.Flags().String("action", "", "Resolution action: approve|reject|redirect (default approve)")
	escalationResolveCmd.Flags().String("redirect-to", "", "Agent slug to redirect to (when --action redirect)")

	escalationCancelCmd.Flags().String("reason", "", "Why the question is being withdrawn (recorded in the journal)")

	escalationCmd.AddCommand(escalationListCmd)
	escalationCmd.AddCommand(escalationResolveCmd)
	escalationCmd.AddCommand(escalationCancelCmd)
	escalationCmd.AddCommand(escalationSweepExpiredCmd)
	escalationCmd.AddCommand(escalationPendingCountCmd)
}
