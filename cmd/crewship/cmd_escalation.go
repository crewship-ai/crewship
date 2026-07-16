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
		headers := []string{"ID", "TYPE", "FROM", "REASON", "STATUS", "CREATED"}
		var rows [][]string
		for _, e := range escalations {
			reason := e.Reason
			if len(reason) > 50 {
				reason = reason[:47] + "..."
			}
			// #1199: show the full ID. Escalation IDs are short cuids
			// (~21 chars), not "absurdly long" — truncating them here
			// used to produce a value that isn't resolvable by
			// `escalation resolve` at all (false 404), since the
			// resolve endpoint requires an exact ID and there's no
			// prefix-matching fallback like `mission get` has.
			rows = append(rows, []string{e.ID, e.Type, e.FromSlug, reason, e.Status, e.CreatedAt})
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
		resp.Body.Close()

		cli.PrintSuccess(fmt.Sprintf("Escalation %s resolved.", args[0]))
		return nil
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
	escalationListCmd.Flags().String("status", "", "Filter by status: PENDING|RESOLVED")
	escalationListCmd.Flags().Int("limit", 0, "Cap rows returned client-side (0 = unbounded)")
	escalationListCmd.Flags().String("since", "", "Only entries newer than this (RFC3339 or 1h/24h/7d duration)")

	escalationResolveCmd.Flags().String("resolution", "", "Resolution notes")
	escalationResolveCmd.Flags().String("action", "", "Resolution action: approve|reject|redirect (default approve)")
	escalationResolveCmd.Flags().String("redirect-to", "", "Agent slug to redirect to (when --action redirect)")

	escalationCmd.AddCommand(escalationListCmd)
	escalationCmd.AddCommand(escalationResolveCmd)
	escalationCmd.AddCommand(escalationPendingCountCmd)
}
