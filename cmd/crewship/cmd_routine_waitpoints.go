package main

// Routine waitpoint subcommands. Waitpoints are HITL pause primitives
// — a routine that includes a `wait` step of kind `approval` parks
// here until a human (or another agent) approves/rejects via API.
// CLI exposes the same pause + decision flow.

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

type waitpointRow struct {
	Token          string `json:"token"`
	PipelineRunID  string `json:"pipeline_run_id"`
	StepID         string `json:"step_id"`
	Kind           string `json:"kind"`
	Prompt         string `json:"prompt"`
	InvokingCrewID string `json:"invoking_crew_id,omitempty"`
	TimeoutAt      string `json:"timeout_at"`
	CreatedAt      string `json:"created_at"`
}

var routineWaitpointsCmd = &cobra.Command{
	Use:   "waitpoints",
	Short: "Inspect + decide on pending HITL approval waitpoints",
	Long: `Waitpoints are pause primitives created when a routine's wait step
of kind=approval fires. Each waitpoint blocks the run goroutine until
a decision arrives (approve / reject) or the timeout elapses. List
shows all pending waitpoints in the workspace; approve/reject wakes
the parked goroutine with the comment as the wait step's output.

Examples:
  crewship routine waitpoints list
  crewship routine waitpoints show <token>
  crewship routine waitpoints approve <token> --comment "LGTM"
  crewship routine waitpoints reject <token>  --comment "needs revision"
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
		jsonOut, _ := cmd.Flags().GetBool("json")
		if jsonOut {
			b, _ := json.MarshalIndent(rows, "", "  ")
			fmt.Println(string(b))
			return nil
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
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				shortID(r.Token), shortID(r.PipelineRunID), r.StepID, r.Kind,
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
			if r.Token == args[0] {
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
				fmt.Println("\nPrompt:")
				fmt.Println(r.Prompt)
				return nil
			}
		}
		return fmt.Errorf("waitpoint %s not found (already decided, expired, or wrong token)", args[0])
	},
}

var routineWaitpointsApproveCmd = &cobra.Command{
	Use:   "approve <token>",
	Short: "Approve a pending waitpoint (run resumes with comment as wait output)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		comment, _ := cmd.Flags().GetString("comment")
		return decideWaitpoint(args[0], true, comment)
	},
}

var routineWaitpointsRejectCmd = &cobra.Command{
	Use:   "reject <token>",
	Short: "Reject a pending waitpoint (run resumes with rejection signal)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		comment, _ := cmd.Flags().GetString("comment")
		return decideWaitpoint(args[0], false, comment)
	},
}

func decideWaitpoint(token string, approved bool, comment string) error {
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
			"approved": approved,
			"comment":  comment,
		},
	)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return err
	}
	if approved {
		fmt.Printf("Approved waitpoint %s.\n", shortID(token))
	} else {
		fmt.Printf("Rejected waitpoint %s.\n", shortID(token))
	}
	return nil
}

func init() {
	routineWaitpointsListCmd.Flags().Bool("json", false, "output as JSON for scripting")

	routineWaitpointsApproveCmd.Flags().String("comment", "", "decision comment forwarded to the parked run as the wait step's output")
	routineWaitpointsRejectCmd.Flags().String("comment", "", "rejection reason forwarded to the parked run")

	routineWaitpointsCmd.AddCommand(routineWaitpointsListCmd)
	routineWaitpointsCmd.AddCommand(routineWaitpointsShowCmd)
	routineWaitpointsCmd.AddCommand(routineWaitpointsApproveCmd)
	routineWaitpointsCmd.AddCommand(routineWaitpointsRejectCmd)

	pipelineCmd.AddCommand(routineWaitpointsCmd)
}
