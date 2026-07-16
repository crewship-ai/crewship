package main

// crewship hire / rehire — PR-D F5 ephemeral-agent CLI surface.
//
// Both commands hit the public API (Hire: POST /agents/hire; Rehire:
// POST /agents/{id}/rehire) and render the same payload shape, so
// the CLI can pipe `crewship hire` output into `crewship agent get`
// without an intermediate conversion.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

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
	ApprovalID    string  `json:"approval_id,omitempty"`
	Decision      string  `json:"decision"`
}

var hireCmd = &cobra.Command{
	Use:   "hire",
	Short: "Hire a short-lived (ephemeral) agent into a crew",
	Long: `Hire a short-lived "contractor" agent into a crew for a bounded task.

The crew's autonomy_level decides what happens next:

  strict   → rejected (operator must dial down policy)
  guided   → staged pending approval (blocking inbox waitpoint + a row
             in the approvals queue; decide with 'crewship approvals
             approve/deny <id>' or 'crewship hire approve <agent-id>')
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
			hired, err := printHireResponse(resp, "Hire submitted (awaiting inbox approval)")
			if err != nil {
				return err
			}
			if waitForApproval, _ := flags.GetBool("wait"); waitForApproval && hired.PendingReview {
				waitTimeout, _ := flags.GetDuration("wait-timeout")
				waitInterval, _ := flags.GetDuration("wait-interval")
				return waitForHireApproval(cmd, client, hired.ID, waitTimeout, waitInterval)
			}
			return nil
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		_, err = printHireResponse(resp, "Agent hired")
		return err
	},
}

// waitForHireApproval polls a staged (PENDING_REVIEW) hire's agent status
// via client.PollHireApproval — the same helper `crewship wait` and
// `mission start --wait` build their polling on — until an operator
// approves it (POST /agents/{id}/approve-hire flips status away from
// PENDING_REVIEW) or it ghosts (TTL elapses before anyone approves it).
func waitForHireApproval(cmd *cobra.Command, client *cli.Client, agentID string, timeout, interval time.Duration) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	start := time.Now()
	var lastStatus string
	status, err := client.PollHireApproval(ctx, agentID, interval, func(h *cli.HireStatus) {
		if h.Status != lastStatus {
			lastStatus = h.Status
			fmt.Fprintf(os.Stderr, "%s[wait]%s %s status=%s elapsed=%s\n",
				cli.Dim, cli.Reset, agentID, h.Status, time.Since(start).Truncate(time.Second))
		}
	})
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("timed out after %s waiting for hire %s to resolve",
				time.Since(start).Truncate(time.Second), agentID)
		}
		return err
	}

	if strings.EqualFold(status.Status, "PENDING_REVIEW") && status.ExpiredAt != nil {
		return fmt.Errorf("hire %s ghosted before being approved (TTL elapsed at %s)", agentID, *status.ExpiredAt)
	}
	cli.PrintSuccess(fmt.Sprintf("Hire %s resolved: status=%s", agentID, status.Status))
	return nil
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
		_, err = printHireResponse(resp, "Agent rehired")
		return err
	},
}

// printHireResponse decodes the API body into a hireResponseShape and
// renders it via the configured formatter. JSON/yaml/ndjson modes get
// the raw payload via AutoDetail's fallthrough; table mode gets a
// human-friendly key/value list. Returns the decoded body so callers
// (e.g. --wait) can act on PendingReview/ID without a second decode.
func printHireResponse(resp *http.Response, headline string) (hireResponseShape, error) {
	var body hireResponseShape
	if err := cli.ReadJSON(resp, &body); err != nil {
		return body, err
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
	if body.ApprovalID != "" {
		pairs = append(pairs, []string{"Approval", body.ApprovalID + " (decide: crewship approvals approve/deny)"})
	}
	if body.Decision != "" {
		pairs = append(pairs, []string{"Decision", body.Decision})
	}
	cli.PrintSuccess(headline + ": " + body.Name + " (" + body.ID + ")")
	f := newFormatter()
	if err := f.AutoDetail(body, pairs); err != nil {
		return body, fmt.Errorf("format response: %w", err)
	}
	return body, nil
}

// hireApproveCmd approves a guided-autonomy hire that is sitting in
// PENDING_REVIEW (it lands there with a blocking inbox waitpoint). This
// is the CLI counterpart to the UI inbox "Approve" button — without it
// there was no terminal path to release a staged hire.
var hireApproveCmd = &cobra.Command{
	Use:   "approve <agent-id-or-slug>",
	Short: "Approve a staged (PENDING_REVIEW) ephemeral hire",
	Long: `Approve an ephemeral agent that a guided-autonomy hire left in
PENDING_REVIEW, flipping it to IDLE so it can serve work. Mirrors the
UI inbox approval. The agent id is printed by 'crewship hire' and shown
in 'crewship agent list'.

Guided hires also appear in the standard approvals queue (issue #1209):
'crewship approvals approve/deny <approval-id>' decides the same hire —
approve flips the agent to IDLE exactly like this command; deny ghosts
the staged agent. Both surfaces stay in sync whichever one you use.`,
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
		resp, err := client.Post("/api/v1/agents/"+agentID+"/approve-hire", map[string]interface{}{})
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		// 409 = not PENDING_REVIEW (already approved, or hired under
		// non-guided autonomy). Surface the server's reason verbatim.
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Hire approved: %s is now active", args[0]))
		return nil
	},
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
	hireCmd.Flags().Bool("wait", false, "Block until a PENDING_REVIEW hire is approved or ghosts (no-op when the hire is already live)")
	hireCmd.Flags().Duration("wait-timeout", 30*time.Minute, "Max time to wait with --wait (0 = forever)")
	hireCmd.Flags().Duration("wait-interval", 2*time.Second, "Poll interval for --wait")

	rehireCmd.Flags().Int("ttl", 0, "New TTL in minutes (default: 30, max: 1440)")
	rehireCmd.Flags().String("reason", "", "One-line justification (required, appended to hire history)")
	rehireCmd.Flags().BoolP("yes", "y", false, "Skip confirmation prompt")

	hireCmd.AddCommand(hireApproveCmd)
	rootCmd.AddCommand(hireCmd)
	rootCmd.AddCommand(rehireCmd)
}
