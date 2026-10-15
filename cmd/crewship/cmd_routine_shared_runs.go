package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// SharedRoutineRun is the same metadata projection in JSON and YAML.
type SharedRoutineRun struct {
	ID        string  `json:"run_id" yaml:"run_id"`
	RoutineID string  `json:"routine_id" yaml:"routine_id"`
	Status    string  `json:"status" yaml:"status"`
	Mode      string  `json:"mode" yaml:"mode"`
	StartedAt string  `json:"started_at" yaml:"started_at"`
	EndedAt   *string `json:"ended_at" yaml:"ended_at"`
}

type sharedRoutineRunResult struct {
	SharedRoutineRun `yaml:",inline"`
	Output           *string           `json:"output" yaml:"output"`
	StepOutputs      map[string]string `json:"step_outputs" yaml:"step_outputs"`
}

func newRoutineSharedRunsCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "shared-runs <routine-id-or-slug> [run-id]", Short: "Read explicitly shared routine history and outputs", Args: cobra.RangeArgs(1, 2), RunE: func(cmd *cobra.Command, args []string) error {
		for _, id := range args {
			if id == "" || id == "." || id == ".." {
				return fmt.Errorf("invalid routine or run identifier")
			}
		}
		limit, _ := cmd.Flags().GetInt("limit")
		offset, _ := cmd.Flags().GetInt("offset")
		if limit < 1 || limit > 500 || offset < 0 || offset > 1000000 {
			return fmt.Errorf("limit must be 1–500 and offset 0–1000000")
		}
		if len(args) == 2 && (cmd.Flags().Changed("limit") || cmd.Flags().Changed("offset")) {
			return fmt.Errorf("pagination is only available when listing runs")
		}
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		workspace := client.GetWorkspaceID()
		if workspace == "" || workspace == "." || workspace == ".." {
			return fmt.Errorf("invalid workspace identifier")
		}
		path := "/api/v1/workspaces/" + url.PathEscape(workspace) + "/restricted-routine-library/" + url.PathEscape(args[0]) + "/runs"
		if len(args) == 2 {
			path += "/" + url.PathEscape(args[1])
		} else {
			path += "?" + url.Values{"limit": {strconv.Itoa(limit)}, "offset": {strconv.Itoa(offset)}}.Encode()
		}
		response, err := client.Get(path)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if err = cli.CheckError(response); err != nil {
			return err
		}
		columns := []string{"RUN", "STATUS", "MODE", "STARTED"}
		row := func(item SharedRoutineRun) []string { return []string{item.ID, item.Status, item.Mode, item.StartedAt} }
		if len(args) == 2 {
			var item sharedRoutineRunResult
			if err = cli.ReadJSON(response, &item); err != nil {
				return err
			}
			if item.ID != args[1] || item.RoutineID == "" || item.StepOutputs == nil {
				return fmt.Errorf("incomplete or mismatched routine result")
			}
			output, err := json.Marshal(item.Output)
			if err != nil {
				return err
			}
			steps, err := json.Marshal(item.StepOutputs)
			if err != nil {
				return err
			}
			return newFormatter().Auto(item, append(columns, "OUTPUT", "STEP OUTPUTS"), [][]string{append(row(item.SharedRoutineRun), string(output), string(steps))})
		}
		items := []SharedRoutineRun{}
		if err = cli.ReadJSON(response, &items); err != nil {
			return err
		}
		rows := [][]string{}
		for _, item := range items {
			rows = append(rows, row(item))
		}
		return newFormatter().Auto(items, columns, rows)
	}}
	cmd.Flags().Int("limit", 100, "Maximum number of visible runs (1–500)")
	cmd.Flags().Int("offset", 0, "Visible run offset (0–1000000)")
	return cmd
}

func init() { pipelineCmd.AddCommand(newRoutineSharedRunsCommand()) }
