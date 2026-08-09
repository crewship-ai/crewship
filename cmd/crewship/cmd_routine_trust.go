package main

// Standing approval grants. A routine's wait:approval gate blocks every
// run until a human decides. Once the same human has decided the same
// way on the same routine body enough times, the decision has stopped
// carrying information — this is how they say so, and how they take it
// back.
//
// The grant is pinned to the routine's definition hash, so editing the
// routine re-arms the gate. `trust list` prints that hash next to each
// grant precisely so the re-arming is visible rather than surprising.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"text/tabwriter"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

type trustGrantRow struct {
	ID              string `json:"id"`
	StepID          string `json:"step_id"`
	DefinitionHash  string `json:"definition_hash"`
	GrantedByUserID string `json:"granted_by_user_id"`
	GrantedAt       string `json:"granted_at"`
	Reason          string `json:"reason,omitempty"`
	PriorApprovals  int    `json:"prior_approvals"`
	MaxUses         *int   `json:"max_uses,omitempty"`
	Uses            int    `json:"uses"`
	ExpiresAt       string `json:"expires_at,omitempty"`
	RevokedAt       string `json:"revoked_at,omitempty"`
	Live            bool   `json:"live"`
}

type trustListResponse struct {
	Slug           string          `json:"slug"`
	DefinitionHash string          `json:"definition_hash"`
	Grants         []trustGrantRow `json:"grants"`
}

var routineTrustCmd = &cobra.Command{
	Use:   "trust",
	Short: "Manage standing approval grants on a routine's gates",
	Long: `A wait:approval gate blocks every run until a human decides. When the
same person has approved the same gate on the same routine body over and
over, a standing grant lets that gate stop asking.

A grant is scoped to ONE gate of ONE routine, and pinned to the routine's
current definition hash. Editing the routine changes that hash, so the
gate starts asking again — trust never silently carries onto a body
nobody reviewed.

Examples:
  crewship routine trust list <slug>
  crewship routine trust grant <slug> --step publish --reason "approved 12x, identical every time"
  crewship routine trust grant <slug> --step publish --max-uses 20 --expires 2026-12-31T00:00:00Z
  crewship routine trust revoke <slug> <grant-id> --reason "policy review"
`,
}

var routineTrustListCmd = &cobra.Command{
	Use:   "list <slug>",
	Short: "List standing approval grants on a routine (revoked ones included)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Get(fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/trust",
			client.GetWorkspaceID(), url.PathEscape(args[0])))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out trustListResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		if out.Grants == nil {
			out.Grants = []trustGrantRow{} // "[]", never "null"
		}

		f := resolvedFormatter(cmd)
		switch f.Format {
		case "json":
			return f.JSON(out)
		case "yaml":
			return f.YAML(out)
		case "ndjson":
			return f.NDJSON(out.Grants)
		}
		if len(out.Grants) == 0 {
			fmt.Printf("No standing approval grants on %s — every gate still asks.\n", out.Slug)
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "GRANT ID\tSTEP\tSTATE\tUSES\tGRANTED\tDEFINITION\tREASON")
		for _, g := range out.Grants {
			state := "live"
			switch {
			case g.RevokedAt != "":
				state = "revoked"
			case !g.Live:
				// Expired or used up — distinct from revoked, which was
				// a decision somebody made.
				state = "spent"
			}
			uses := fmt.Sprintf("%d", g.Uses)
			if g.MaxUses != nil {
				uses = fmt.Sprintf("%d/%d", g.Uses, *g.MaxUses)
			}
			// A grant pinned to a definition that is no longer current is
			// already inert; say so rather than making the reader compare
			// two hashes by eye.
			definition := shortID(g.DefinitionHash)
			if g.DefinitionHash != out.DefinitionHash {
				definition += " (stale)"
			}
			reason := g.Reason
			if len(reason) > 40 {
				reason = reason[:37] + "..."
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				g.ID, g.StepID, state, uses, formatTimestamp(g.GrantedAt), definition, reason)
		}
		return w.Flush()
	},
}

var (
	trustGrantStep     string
	trustGrantReason   string
	trustGrantMaxUses  int
	trustGrantExpires  string
	trustGrantPriorApp int
)

var routineTrustGrantCmd = &cobra.Command{
	Use:   "grant <slug>",
	Short: "Stop a routine's gate from asking for approval",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		if trustGrantStep == "" {
			return fmt.Errorf("--step is required: trust is granted per gate, not per routine")
		}
		if trustGrantExpires != "" {
			if _, err := time.Parse(time.RFC3339, trustGrantExpires); err != nil {
				return fmt.Errorf("--expires must be RFC3339 (e.g. 2026-12-31T00:00:00Z): %w", err)
			}
		}

		if trustGrantMaxUses < 0 {
			return fmt.Errorf("--max-uses must be a positive count (omit the flag for unlimited)")
		}

		body := map[string]any{"step_id": trustGrantStep}
		if trustGrantReason != "" {
			body["reason"] = trustGrantReason
		}
		if trustGrantMaxUses > 0 {
			body["max_uses"] = trustGrantMaxUses
		}
		if trustGrantExpires != "" {
			body["expires_at"] = trustGrantExpires
		}
		if trustGrantPriorApp > 0 {
			body["prior_approvals"] = trustGrantPriorApp
		}

		client := newAPIClient()
		resp, err := client.Post(fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/trust",
			client.GetWorkspaceID(), url.PathEscape(args[0])), body)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out struct {
			ID             string `json:"id"`
			StepID         string `json:"step_id"`
			DefinitionHash string `json:"definition_hash"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		f := resolvedFormatter(cmd)
		switch f.Format {
		case "json":
			return f.JSON(out)
		case "yaml":
			return f.YAML(out)
		}
		fmt.Printf("Gate %q on %s will now auto-approve.\n", out.StepID, args[0])
		fmt.Printf("  grant:      %s\n", out.ID)
		fmt.Printf("  definition: %s\n", shortID(out.DefinitionHash))
		fmt.Printf("\nEditing the routine re-arms this gate. Revoke sooner with:\n")
		fmt.Printf("  crewship routine trust revoke %s %s\n", args[0], out.ID)
		return nil
	},
}

var trustRevokeReason string

var routineTrustRevokeCmd = &cobra.Command{
	Use:   "revoke <slug> <grant-id>",
	Short: "Withdraw a standing grant so the gate asks again",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		path := fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/trust/%s",
			client.GetWorkspaceID(), url.PathEscape(args[0]), url.PathEscape(args[1]))
		if trustRevokeReason != "" {
			path += "?reason=" + url.QueryEscape(trustRevokeReason)
		}
		resp, err := client.Delete(path)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		fmt.Printf("Standing grant %s revoked — the gate asks again.\n", args[1])
		return nil
	},
}

func init() {
	routineTrustGrantCmd.Flags().StringVar(&trustGrantStep, "step", "", "step id of the wait:approval gate (required)")
	routineTrustGrantCmd.Flags().StringVar(&trustGrantReason, "reason", "", "why this gate no longer needs a human")
	routineTrustGrantCmd.Flags().IntVar(&trustGrantMaxUses, "max-uses", 0, "auto-approve at most N times (default: unlimited)")
	routineTrustGrantCmd.Flags().StringVar(&trustGrantExpires, "expires", "", "RFC3339 instant after which the grant stops firing")
	routineTrustGrantCmd.Flags().IntVar(&trustGrantPriorApp, "prior-approvals", 0, "how many manual approvals earned this grant (audit)")
	routineTrustRevokeCmd.Flags().StringVar(&trustRevokeReason, "reason", "", "why trust is being withdrawn")

	routineTrustCmd.AddCommand(routineTrustListCmd)
	routineTrustCmd.AddCommand(routineTrustGrantCmd)
	routineTrustCmd.AddCommand(routineTrustRevokeCmd)

	pipelineCmd.AddCommand(routineTrustCmd)
}
