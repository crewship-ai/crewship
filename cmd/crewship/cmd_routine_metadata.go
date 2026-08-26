package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// Run metadata mutators (set/increment/append) + parent/child run tree.
// Mirror PATCH /pipeline-runs/{id}/metadata and GET .../{id}/tree.

var routineMetadataCmd = &cobra.Command{
	Use:   "metadata <run_id>",
	Short: "Mutate a run's metadata scratchpad (set / increment / append)",
	Long: `Applies set/increment/append ops to a run's metadata, readable from
later steps as {{ run.metadata.x }}. Each flag takes a JSON object.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		ops := map[string]any{}
		for flag, key := range map[string]string{"set": "set", "increment": "increment", "append": "append"} {
			raw, _ := cmd.Flags().GetString(flag)
			if raw == "" {
				continue
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(raw), &m); err != nil {
				return fmt.Errorf("parse --%s JSON: %w", flag, err)
			}
			ops[key] = m
		}
		if len(ops) == 0 {
			return fmt.Errorf("pass --set, --increment, and/or --append")
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		resp, err := client.Do("PATCH",
			fmt.Sprintf("/api/v1/workspaces/%s/pipeline-runs/%s/metadata", ws, args[0]), ops)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var body struct {
			Metadata map[string]any `json:"metadata"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		// The human form wraps the metadata in a "Updated metadata for X:"
		// header; the machine form is the run id plus the resulting document,
		// because a caller applying ops needs to read the result back.
		return resolvedFormatter(cmd).AutoHuman(struct {
			RunID    string         `json:"run_id"`
			Metadata map[string]any `json:"metadata"`
		}{args[0], body.Metadata}, func() {
			b, _ := json.MarshalIndent(body.Metadata, "", "  ")
			fmt.Printf("Updated metadata for %s:\n%s\n", args[0], string(b))
		})
	},
}

var routineTreeCmd = &cobra.Command{
	Use:   "tree <run_id>",
	Short: "Show a run and its child runs (call_pipeline / deferred / replay)",
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
		resp, err := client.Get(fmt.Sprintf("/api/v1/workspaces/%s/pipeline-runs/%s/tree", ws, args[0]))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var body struct {
			Nodes []runTreeNode `json:"nodes"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		if body.Nodes == nil {
			body.Nodes = []runTreeNode{}
		}
		// The human table substitutes "(root)" for an empty parent_id; the
		// machine rows keep the empty string, because "" is how a consumer
		// already tests for a root and "(root)" is a string it would have to
		// know to special-case.
		return resolvedFormatter(cmd).AutoHuman(body.Nodes, func() {
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "RUN ID\tPARENT\tROUTINE\tSTATUS\tVIA\tCOST")
			for _, n := range body.Nodes {
				parent := n.ParentID
				if parent == "" {
					parent = "(root)"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t$%.4f\n", n.ID, parent, n.PipelineSlug, n.Status, n.TriggeredVia, n.CostUSD)
			}
			_ = w.Flush()
		})
	},
}

// runTreeNode is one run in a `routine tree` result.
type runTreeNode struct {
	ID           string  `json:"id"`
	ParentID     string  `json:"parent_id"`
	PipelineSlug string  `json:"pipeline_slug"`
	Status       string  `json:"status"`
	TriggeredVia string  `json:"triggered_via"`
	CostUSD      float64 `json:"cost_usd"`
}

var routineSignalCmd = &cobra.Command{
	Use:   "signal <run_id>",
	Short: "Deliver a payload to a running run's wait:event step (input-stream injection)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		event, _ := cmd.Flags().GetString("event")
		payload, _ := cmd.Flags().GetString("payload")
		if event == "" {
			return fmt.Errorf("--event <type> required")
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		resp, err := client.Do("POST",
			fmt.Sprintf("/api/v1/workspaces/%s/pipeline-runs/%s/signal", ws, args[0]),
			map[string]any{"event_type": event, "payload": payload})
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		fmt.Printf("Signal %q delivered to run %s.\n", event, args[0])
		return nil
	},
}

func init() {
	routineSignalCmd.Flags().String("event", "", "event_type the wait:event step is waiting on (REQUIRED)")
	routineSignalCmd.Flags().String("payload", "", "string payload that becomes the wait step's output")
	pipelineCmd.AddCommand(routineSignalCmd)
	routineMetadataCmd.Flags().String("set", "", "JSON object of keys to set (e.g. '{\"stage\":\"done\"}')")
	routineMetadataCmd.Flags().String("increment", "", "JSON object of numeric keys to add to (e.g. '{\"count\":1}')")
	routineMetadataCmd.Flags().String("append", "", "JSON object of array keys to push onto (e.g. '{\"errors\":\"oops\"}')")
	pipelineCmd.AddCommand(routineMetadataCmd)
	pipelineCmd.AddCommand(routineTreeCmd)
}
