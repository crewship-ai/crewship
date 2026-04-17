package main

import (
	"fmt"
	"net/url"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// checkpointCmd groups Cartographer checkpoint operations. Checkpoints
// are named cursors into the mission journal — they mark "a known-good
// state" so later divergence can be inspected or a new mission forked
// from the marked point.
//
// `restore` is advisory: the server surfaces the cursor and any journal
// entries that diverged from it, but does not mutate mission state. Use
// `fork` when you need a concrete new mission branched from a checkpoint.
var checkpointCmd = &cobra.Command{
	Use:   "checkpoint",
	Short: "Manage Cartographer mission checkpoints",
	Long: `Create, inspect, restore (advisory), fork, and delete mission
checkpoints. A checkpoint pins a journal cursor to a human-readable
label so you can return to it later.

Examples:
  crewship checkpoint list --mission MIS-42
  crewship checkpoint create --mission MIS-42 --label "green build"
  crewship checkpoint restore chk_abc            # advisory: shows diverged entries
  crewship checkpoint fork chk_abc --label "experiment-1"
  crewship checkpoint delete chk_abc --yes`,
}

// checkpointRow mirrors the rendered columns. Extra backend fields are
// ignored by json.Decoder and can be added here when new columns are
// desired in the table view.
type checkpointRow struct {
	ID            string `json:"id"`
	MissionID     string `json:"mission_id"`
	Label         string `json:"label"`
	JournalCursor string `json:"journal_cursor"`
	CreatedBy     string `json:"created_by"`
	CreatedAt     string `json:"created_at"`
	ForkOf        string `json:"fork_of,omitempty"`
}

var checkpointListCmd = &cobra.Command{
	Use:   "list",
	Short: "List checkpoints for a mission",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		missionID, _ := cmd.Flags().GetString("mission")
		if missionID == "" {
			return fmt.Errorf("--mission is required")
		}

		client := newAPIClient()
		resp, err := client.Get("/api/v1/missions/" + url.PathEscape(missionID) + "/checkpoints")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var body struct {
			Checkpoints []checkpointRow `json:"checkpoints"`
			Count       int             `json:"count"`
			MissionID   string          `json:"mission_id"`
		}
		if err := cli.ReadJSON(resp, &body); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"ID", "LABEL", "CURSOR", "CREATED", "CREATED_BY"}
		rows := make([][]string, 0, len(body.Checkpoints))
		for _, c := range body.Checkpoints {
			rows = append(rows, []string{
				truncateString(c.ID, 24),
				truncateString(c.Label, 28),
				truncateString(c.JournalCursor, 20),
				c.CreatedAt,
				truncateString(c.CreatedBy, 16),
			})
		}
		return f.Auto(body.Checkpoints, headers, rows)
	},
}

var checkpointCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a checkpoint at the current journal cursor",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		missionID, _ := cmd.Flags().GetString("mission")
		if missionID == "" {
			return fmt.Errorf("--mission is required")
		}
		label, _ := cmd.Flags().GetString("label")
		client := newAPIClient()

		body := map[string]string{}
		if label != "" {
			body["label"] = label
		}
		resp, err := client.Post("/api/v1/missions/"+url.PathEscape(missionID)+"/checkpoints", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var cp checkpointRow
		if err := cli.ReadJSON(resp, &cp); err != nil {
			return err
		}
		f := newFormatter()
		if f.Format == "json" {
			return f.JSON(cp)
		}
		if f.Format == "yaml" {
			return f.YAML(cp)
		}
		cli.PrintSuccess(fmt.Sprintf("Checkpoint created: %s (label=%q cursor=%s)", cp.ID, cp.Label, cp.JournalCursor))
		return nil
	},
}

var checkpointRestoreCmd = &cobra.Command{
	Use:   "restore <id>",
	Short: "Inspect divergence between a checkpoint and current mission state (advisory)",
	Long: `restore is advisory: the server returns the checkpoint cursor and
any journal entries that diverged since the checkpoint was made, but no
mission state is mutated. Use 'checkpoint fork' to spawn a new mission
anchored at this point.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Post("/api/v1/checkpoints/"+url.PathEscape(args[0])+"/restore", nil)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var body struct {
			Checkpoint      checkpointRow `json:"checkpoint"`
			JournalCursor   string        `json:"journal_cursor"`
			WarnDivergence  []string      `json:"warn_divergence"`
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
		fmt.Printf("%sCheckpoint:%s %s\n", cli.Bold, cli.Reset, body.Checkpoint.ID)
		fmt.Printf("%sCursor:%s     %s\n", cli.Bold, cli.Reset, body.JournalCursor)
		if len(body.WarnDivergence) > 0 {
			fmt.Printf("%sDiverged entries (%d):%s\n", cli.Yellow, len(body.WarnDivergence), cli.Reset)
			for _, w := range body.WarnDivergence {
				fmt.Printf("  - %s\n", w)
			}
		} else {
			fmt.Printf("%sNo divergence since checkpoint.%s\n", cli.Green, cli.Reset)
		}
		return nil
	},
}

var checkpointForkCmd = &cobra.Command{
	Use:   "fork <id>",
	Short: "Fork a new mission from a checkpoint",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		label, _ := cmd.Flags().GetString("label")
		client := newAPIClient()
		body := map[string]string{}
		if label != "" {
			body["label"] = label
		}
		resp, err := client.Post("/api/v1/checkpoints/"+url.PathEscape(args[0])+"/fork", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out struct {
			NewMissionID    string `json:"new_mission_id"`
			NewCheckpointID string `json:"new_checkpoint_id"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		f := newFormatter()
		if f.Format == "json" {
			return f.JSON(out)
		}
		if f.Format == "yaml" {
			return f.YAML(out)
		}
		cli.PrintSuccess(fmt.Sprintf("Forked mission %s from checkpoint %s (new checkpoint %s).",
			out.NewMissionID, args[0], out.NewCheckpointID))
		return nil
	},
}

var checkpointDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a checkpoint",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		if err := confirmAction(cmd, fmt.Sprintf("Delete checkpoint %s?", args[0])); err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Delete("/api/v1/checkpoints/" + url.PathEscape(args[0]))
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()
		cli.PrintSuccess(fmt.Sprintf("Checkpoint %s deleted.", args[0]))
		return nil
	},
}

func init() {
	checkpointListCmd.Flags().String("mission", "", "Mission ID (required)")

	checkpointCreateCmd.Flags().String("mission", "", "Mission ID (required)")
	checkpointCreateCmd.Flags().String("label", "", "Optional human-readable label")

	checkpointForkCmd.Flags().String("label", "", "Optional label for the forked mission")

	checkpointDeleteCmd.Flags().Bool("yes", false, "Skip confirmation prompt")

	checkpointCmd.AddCommand(checkpointListCmd)
	checkpointCmd.AddCommand(checkpointCreateCmd)
	checkpointCmd.AddCommand(checkpointRestoreCmd)
	checkpointCmd.AddCommand(checkpointForkCmd)
	checkpointCmd.AddCommand(checkpointDeleteCmd)
}
