package main

// Routine waitpoint subcommands. Waitpoints are HITL pause primitives
// — a routine that includes a `wait` step of kind `approval` parks
// here until a human (or another agent) approves/rejects via API.
// CLI exposes the same pause + decision flow.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/spf13/cobra"
)

type waitpointRow struct {
	DecisionForm   *pipeline.DecisionForm `json:"decision_form,omitempty" yaml:"decision_form,omitempty"`
	Token          string                 `json:"token" yaml:"token"`
	PipelineRunID  string                 `json:"pipeline_run_id" yaml:"pipeline_run_id"`
	StepID         string                 `json:"step_id" yaml:"step_id"`
	Kind           string                 `json:"kind" yaml:"kind"`
	Prompt         string                 `json:"prompt" yaml:"prompt"`
	InvokingCrewID string                 `json:"invoking_crew_id,omitempty" yaml:"invoking_crew_id,omitempty"`
	TimeoutAt      string                 `json:"timeout_at" yaml:"timeout_at"`
	CreatedAt      string                 `json:"created_at" yaml:"created_at"`
	// CallbackAvailable: `callback-url <token>` can mint a public URL for
	// this waitpoint. The URL itself is never listed (#1910).
	CallbackAvailable bool `json:"callback_available" yaml:"callback_available"`
}

var routineWaitpointsCmd = &cobra.Command{
	Use:   "waitpoints",
	Short: "Inspect + decide on pending HITL approval waitpoints",
	Long: `Waitpoints are pause primitives created when a routine's wait step
of kind=approval fires. Each waitpoint blocks the run goroutine until
a decision arrives (approve / reject) or the timeout elapses. List
shows all pending waitpoints in the workspace; approve/reject wakes
the parked run. Rich forms return action_id and typed data; legacy gates keep their approval marker.

Examples:
  crewship routine waitpoints list
  crewship routine waitpoints show <token>
  crewship routine waitpoints approve <token> --comment "LGTM"
  crewship routine waitpoints reject <token>  --comment "needs revision"
  crewship routine waitpoints callback-url <token>

<token> is the waitpoint's public id. It is not a credential: completing a
waitpoint without signing in needs a callback URL, which callback-url mints
and shows once.
`,
}

var routineWaitpointsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List pending waitpoints in this workspace",
	RunE: func(cmd *cobra.Command, _ []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		resp, err := client.Get(fmt.Sprintf("/api/v1/workspaces/%s/pipelines/waitpoints", ws))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var rows []waitpointRow
		if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		// Machine formats (global -f/--format, or the legacy --json bool)
		// pass the API rows through — the harness and any HITL automation
		// extract the token from here, so the table must never be what a
		// JSON consumer gets ("No pending waitpoints." broke jq parsing
		// and made approval gates look like a product bug).
		f := resolvedFormatter(cmd)
		if rows == nil {
			rows = []waitpointRow{} // "[]", never "null"
		}
		if f.Format == "json" {
			return f.JSON(rows)
		}
		switch f.Format {
		case "yaml":
			return f.YAML(rows)
		case "ndjson":
			return f.NDJSON(rows)
		}
		if len(rows) == 0 {
			fmt.Println("No pending waitpoints.")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "TOKEN\tRUN ID\tSTEP\tKIND\tCREATED\tTIMEOUT\tPROMPT")
		for _, r := range rows {
			prompt := r.Prompt
			if len(prompt) > 60 {
				prompt = prompt[:57] + "..."
			}
			// The token is the only identifier waitpoints approve/
			// reject accepts; truncating it here would leave users
			// unable to act on the row. Show the full token; we
			// still shortID() the run_id since that one is just a
			// link target, not an action argument.
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				r.Token, shortID(r.PipelineRunID), r.StepID, r.Kind,
				formatTimestamp(r.CreatedAt), formatTimestamp(r.TimeoutAt), prompt)
		}
		return w.Flush()
	},
}

var routineWaitpointsShowCmd = &cobra.Command{
	Use:   "show <token>",
	Short: "Show full prompt + metadata for a pending waitpoint",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		resp, err := client.Get(fmt.Sprintf("/api/v1/workspaces/%s/pipelines/waitpoints", ws))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var rows []waitpointRow
		if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		for _, r := range rows {
			if r.Token != args[0] {
				continue
			}
			// The waitpoint row IS the machine payload — the human view adds
			// a curl recipe and a "Prompt:" heading, both of which are
			// instructions to a person rather than data.
			return resolvedFormatter(cmd).AutoHuman(r, func() {
				w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintf(w, "Token:\t%s\n", r.Token)
				fmt.Fprintf(w, "Run ID:\t%s\n", r.PipelineRunID)
				fmt.Fprintf(w, "Step:\t%s\n", r.StepID)
				fmt.Fprintf(w, "Kind:\t%s\n", r.Kind)
				if r.InvokingCrewID != "" {
					fmt.Fprintf(w, "Invoking crew:\t%s\n", r.InvokingCrewID)
				}
				fmt.Fprintf(w, "Created:\t%s\n", formatTimestamp(r.CreatedAt))
				fmt.Fprintf(w, "Timeout:\t%s\n", formatTimestamp(r.TimeoutAt))
				_ = w.Flush()
				if r.DecisionForm != nil {
					raw, _ := json.MarshalIndent(r.DecisionForm, "", "  ")
					fmt.Printf("\nDecision form:\n%s\nUse --action <id> --input <JSON> with approve or reject to match the action.\n", raw)
				}
				if r.CallbackAvailable {
					fmt.Println("\nTo let an external system complete this waitpoint without signing in, mint")
					fmt.Println("a callback URL (shown once; minting again replaces it):")
					fmt.Printf("  crewship routine waitpoints callback-url %s\n", r.Token)
				}
				fmt.Println("\nPrompt:")
				fmt.Println(r.Prompt)
			})
		}
		return cli.NotFoundf("waitpoint not found (already decided, expired, or wrong token)")
	},
}

var routineWaitpointsApproveCmd = &cobra.Command{
	Use:   "approve <token>",
	Short: "Approve a pending waitpoint (run resumes with the accepted decision)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		comment, _ := cmd.Flags().GetString("comment")
		return decideWaitpointWithForm(cmd, args[0], true, comment)
	},
}

var routineWaitpointsRejectCmd = &cobra.Command{
	Use:   "reject <token>",
	Short: "Reject a pending waitpoint (run resumes with rejection signal)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		comment, _ := cmd.Flags().GetString("comment")
		return decideWaitpointWithForm(cmd, args[0], false, comment)
	},
}

func decideWaitpointWithForm(cmd *cobra.Command, token string, approved bool, comment string) error {
	action, _ := cmd.Flags().GetString("action")
	input, _ := cmd.Flags().GetString("input")
	var data map[string]any
	if input != "" {
		var err error
		data, err = parseInputFixture(input)
		if err != nil {
			return err
		}
		if action == "" {
			return fmt.Errorf("--input requires --action")
		}
	}
	return postWaitpointDecision(token, approved, comment, action, data)
}
func decideWaitpoint(token string, approved bool, comment string) error {
	return postWaitpointDecision(token, approved, comment, "", nil)
}
func postWaitpointDecision(token string, approved bool, comment, action string, data map[string]any) error {
	if err := requireAuth(); err != nil {
		return err
	}
	if err := requireWorkspace(); err != nil {
		return err
	}
	client := newAPIClient()
	ws := client.GetWorkspaceID()
	resp, err := client.Post(
		fmt.Sprintf("/api/v1/workspaces/%s/pipelines/waitpoints/%s/approve", ws, token),
		map[string]interface{}{
			"approved":  approved,
			"comment":   comment,
			"action_id": action,
			"data":      data,
		},
	)
	if err != nil {
		// Transport errors include the request URL. Keep their type and cause,
		// but do not repeat the capability the caller already supplied.
		var connectionErr *cli.ConnectionError
		var requestErr *url.Error
		if token != "" && errors.As(err, &connectionErr) && errors.As(connectionErr.Err, &requestErr) {
			redacted := *requestErr
			redacted.URL = strings.ReplaceAll(redacted.URL, token, "[redacted]")
			return &cli.ConnectionError{Err: &redacted}
		}
		return err
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return err
	}
	if approved {
		fmt.Println("Approved waitpoint.")
	} else {
		fmt.Println("Rejected waitpoint.")
	}
	return nil
}

var routineWaitpointsCallbackURLCmd = &cobra.Command{
	Use:   "callback-url <token>",
	Short: "Mint the public callback URL for a pending waitpoint (shown once)",
	Long: `Mint a URL an external system (CI job, approval service, vendor
webhook) can POST to complete a pending waitpoint without signing in:

  curl -X POST <callback_url> -d '{"approved":true,"payload":{}}'

The URL is shown only now. Crewship stores a digest of it, so it cannot be
listed or printed again; running this command again mints a new URL and the
previous one stops working. Requires MANAGER or above. Waitpoints with a typed
decision form, and held routine actions, have no callback URL.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		resp, err := client.Post(fmt.Sprintf("/api/v1/workspaces/%s/pipelines/waitpoints/%s/callback-token",
			ws, url.PathEscape(args[0])), map[string]any{})
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out struct {
			Token       string `json:"token" yaml:"token"`
			CallbackURL string `json:"callback_url" yaml:"callback_url"`
			Rotated     bool   `json:"rotated" yaml:"rotated"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			fmt.Println(out.CallbackURL)
			if out.Rotated {
				fmt.Fprintln(os.Stderr, "The previous callback URL for this waitpoint no longer works.")
			}
			fmt.Fprintln(os.Stderr, "Store it now: it cannot be shown again. Anyone holding it can complete this waitpoint.")
		})
	},
}

func init() {
	for _, cmd := range []*cobra.Command{routineWaitpointsApproveCmd, routineWaitpointsRejectCmd} {
		cmd.Flags().String("action", "", "named decision action ID")
		cmd.Flags().String("input", "", "decision fields as JSON or @file.json")
	}
	routineWaitpointsListCmd.Flags().Bool("json", false, "Deprecated alias for --format json")

	routineWaitpointsApproveCmd.Flags().String("comment", "", "comment stored with the accepted decision")
	routineWaitpointsRejectCmd.Flags().String("comment", "", "rejection reason stored with the decision")

	routineWaitpointsCmd.AddCommand(routineWaitpointsListCmd)
	routineWaitpointsCmd.AddCommand(routineWaitpointsShowCmd)
	routineWaitpointsCmd.AddCommand(routineWaitpointsApproveCmd)
	routineWaitpointsCmd.AddCommand(routineWaitpointsRejectCmd)
	routineWaitpointsCmd.AddCommand(routineWaitpointsCallbackURLCmd)

	pipelineCmd.AddCommand(routineWaitpointsCmd)
}
