package main

// crewship hire / rehire — PR-D F5 ephemeral-agent CLI surface.
//
// Both commands hit the public API (Hire: POST /agents/hire; Rehire:
// POST /agents/{id}/rehire) and render the same payload shape, so
// the CLI can pipe `crewship hire` output into `crewship agent get`
// without an intermediate conversion.

import (
	"fmt"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// hireResponseShape mirrors internal/api/hireResponse. Re-declared
// here because internal/api is a server-side package the CLI can't
// import without bringing the whole API graph into the binary.
type hireResponseShape struct {
	ID            string  `json:"id"`
	CrewID        *string `json:"crew_id"`
	WorkspaceID   string  `json:"workspace_id"`
	Slug          string  `json:"slug"`
	Name          string  `json:"name"`
	Status        string  `json:"status"`
	Ephemeral     bool    `json:"ephemeral"`
	ExpiresAt     *string `json:"expires_at"`
	ExpiredAt     *string `json:"expired_at"`
	ParentLeadID  *string `json:"parent_lead_id"`
	HireReason    *string `json:"hire_reason"`
	PendingReview bool    `json:"pending_review"`
	InboxItemID   string  `json:"inbox_item_id,omitempty"`
	Decision      string  `json:"decision"`
}

var hireCmd = &cobra.Command{
	Use:   "hire",
	Short: "Hire a short-lived (ephemeral) agent into a crew",
	Long: `Hire a short-lived "contractor" agent into a crew for a bounded task.

The crew's autonomy_level decides what happens next:

  strict   → rejected (operator must dial down policy)
  guided   → blocking inbox approval (the hire is staged)
  trusted  → auto-spawned, logged to inbox
  full     → auto-spawned, journal-only

The agent ghosts automatically when its TTL elapses. Rehire it with
'crewship rehire <id>' to extend.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		flags := cmd.Flags()
		crew, _ := flags.GetString("crew")
		template, _ := flags.GetString("template")
		reason, _ := flags.GetString("reason")
		if crew == "" {
			return fmt.Errorf("--crew is required")
		}
		if template == "" {
			return fmt.Errorf("--template is required")
		}
		if reason == "" {
			return fmt.Errorf("--reason is required (audit trail)")
		}

		body := map[string]interface{}{
			"template_slug": template,
			"reason":        reason,
		}
		// Crew accepts either id or slug. We send whichever shape
		// the user gave us — slug-flavoured strings go into
		// crew_slug, raw CUIDs go into crew_id. The server accepts
		// both.
		if looksLikeCUID(crew) {
			body["crew_id"] = crew
		} else {
			body["crew_slug"] = crew
		}
		if v, _ := flags.GetString("model"); v != "" {
			body["model"] = v
		}
		if flags.Changed("ttl") {
			ttl, _ := flags.GetInt("ttl")
			body["ttl_minutes"] = ttl
		}
		if v, _ := flags.GetString("parent-lead"); v != "" {
			body["parent_lead_id"] = v
		}

		// On strict crews the call will 403 — we can't tell that
		// from the client without a round-trip, so just inform the
		// user and let the server be authoritative on the rejection
		// reason (PR-D Hire returns autonomy_level in the body).
		if err := confirmAction(cmd, fmt.Sprintf("Hire ephemeral agent into crew %q?", crew)); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Post("/api/v1/agents/hire", body)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		// 202 (guided/pending) is not an error — surface it to the
		// user explicitly so they know to check the inbox.
		if resp.StatusCode == http.StatusAccepted {
			return printHireResponse(resp, "Hire submitted (awaiting inbox approval)")
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		return printHireResponse(resp, "Agent hired")
	},
}

var rehireCmd = &cobra.Command{
	Use:   "rehire <agent-slug-or-id>",
	Short: "Resurrect a ghosted ephemeral agent for another TTL window",
	Long: `Rehire an existing ephemeral agent: resets its TTL, clears the
ghost state, appends the new reason to the agent's hire-history. The
agent picks up where it left off — memory files are preserved on
disk so the next chat resumes with full context.

Rehiring a still-live ephemeral is also allowed and just extends its
expires_at without changing the live/ghost count toward the quota.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		agentID, err := resolveAgentID(client, args[0])
		if err != nil {
			return err
		}

		flags := cmd.Flags()
		reason, _ := flags.GetString("reason")
		if reason == "" {
			return fmt.Errorf("--reason is required (history trail)")
		}
		body := map[string]interface{}{"reason": reason}
		if flags.Changed("ttl") {
			ttl, _ := flags.GetInt("ttl")
			body["ttl_minutes"] = ttl
		}

		if err := confirmAction(cmd, fmt.Sprintf("Rehire agent %q?", args[0])); err != nil {
			return err
		}

		resp, err := client.Post("/api/v1/agents/"+agentID+"/rehire", body)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		return printHireResponse(resp, "Agent rehired")
	},
}

// printHireResponse decodes the API body into a hireResponseShape and
// renders it via the configured formatter. JSON/yaml/ndjson modes get
// the raw payload via AutoDetail's fallthrough; table mode gets a
// human-friendly key/value list.
func printHireResponse(resp *http.Response, headline string) error {
	var body hireResponseShape
	if err := cli.ReadJSON(resp, &body); err != nil {
		return err
	}
	pairs := [][]string{
		{"ID", body.ID},
		{"Slug", body.Slug},
		{"Name", body.Name},
		{"Status", body.Status},
	}
	if body.ExpiresAt != nil {
		pairs = append(pairs, []string{"Expires At", *body.ExpiresAt})
	}
	if body.HireReason != nil {
		pairs = append(pairs, []string{"History", *body.HireReason})
	}
	if body.PendingReview {
		pairs = append(pairs, []string{"Review", "PENDING APPROVAL (inbox " + body.InboxItemID + ")"})
	}
	if body.Decision != "" {
		pairs = append(pairs, []string{"Decision", body.Decision})
	}
	cli.PrintSuccess(headline + ": " + body.Name + " (" + body.ID + ")")
	f := newFormatter()
	if err := f.AutoDetail(body, pairs); err != nil {
		return fmt.Errorf("format response: %w", err)
	}
	return nil
}

func init() {
	// Hire flags. --crew is the only "must specify shape" flag; the
	// rest have sensible server-side defaults.
	hireCmd.Flags().String("crew", "", "Crew slug or id (required)")
	hireCmd.Flags().String("template", "", "Template slug to spawn from (required, see `crewship template list`)")
	hireCmd.Flags().String("model", "", "LLM model override (default: template default)")
	hireCmd.Flags().Int("ttl", 0, "TTL in minutes (default: 30, max: 1440)")
	hireCmd.Flags().String("reason", "", "One-line justification (required, written to audit trail)")
	hireCmd.Flags().String("parent-lead", "", "Lead agent ID that authored this hire (optional)")
	hireCmd.Flags().BoolP("yes", "y", false, "Skip confirmation prompt")

	rehireCmd.Flags().Int("ttl", 0, "New TTL in minutes (default: 30, max: 1440)")
	rehireCmd.Flags().String("reason", "", "One-line justification (required, appended to hire history)")
	rehireCmd.Flags().BoolP("yes", "y", false, "Skip confirmation prompt")

	rootCmd.AddCommand(hireCmd)
	rootCmd.AddCommand(rehireCmd)
}
