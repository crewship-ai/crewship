package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// Routine-definition tags (v123) — tag a routine for cross-crew
// discovery (`routine list --tag billing`). Distinct from run tags.

// routineTagResult is the machine-readable answer of `routine tag`.
// Tags is the server's sorted tag set after the add; it is omitted when a
// removal followed, because the DELETE answers only {"ok":true} and the
// list returned by the earlier PUT would then be stale.
type routineTagResult struct {
	Slug    string   `json:"slug" yaml:"slug"`
	Added   []string `json:"added,omitempty" yaml:"added,omitempty"`
	Removed string   `json:"removed,omitempty" yaml:"removed,omitempty"`
	Tags    []string `json:"tags,omitempty" yaml:"tags,omitempty"`
}

var routineTagCmd = &cobra.Command{
	Use:   "tag <slug>",
	Short: "Add or remove discovery tags on a routine definition",
	Long: `Add or remove discovery tags on a routine definition. Definition tags
power cross-crew discovery (` + "`routine list --tag <tag>`" + `) and are distinct
from run tags. Removing a tag only unassigns the label; the routine and
its runs are untouched. Requires MANAGER or above.

Examples:
  crewship routine tag invoice-parser --add billing --add finance
  crewship routine tag invoice-parser --remove finance
  crewship routine tag invoice-parser --add billing -f json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		add, _ := cmd.Flags().GetStringSlice("add")
		remove, _ := cmd.Flags().GetString("remove")
		if len(add) == 0 && remove == "" {
			return fmt.Errorf("pass --add <tag> (repeatable) and/or --remove <tag>")
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		f := resolvedFormatter(cmd)
		result := routineTagResult{Slug: args[0]}
		if len(add) > 0 {
			resp, err := client.Do("PUT",
				fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/tags", ws, args[0]),
				map[string]any{"tags": add})
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if err := cli.CheckError(resp); err != nil {
				return err
			}
			var body struct {
				Tags []string `json:"tags"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
				return fmt.Errorf("decode response: %w", err)
			}
			result.Added = add
			result.Tags = body.Tags
			if f.RoutesToHuman() {
				fmt.Printf("Tagged %s: +%v\n", args[0], add)
			}
		}
		if remove != "" {
			resp, err := client.Do("DELETE",
				fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/tags/%s", ws, args[0], remove),
				http.NoBody)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if err := cli.CheckError(resp); err != nil {
				return err
			}
			result.Removed = remove
			result.Tags = nil
			if f.RoutesToHuman() {
				fmt.Printf("Untagged %s: -%s\n", args[0], remove)
			}
		}
		// Human lines were printed as each call landed; machine formats get
		// one object describing the whole invocation.
		return f.AutoHuman(result, func() {})
	},
}

func init() {
	routineTagCmd.Flags().StringSlice("add", nil, "tag(s) to add (repeatable)")
	routineTagCmd.Flags().String("remove", "", "tag to remove")
	pipelineCmd.AddCommand(routineTagCmd)
}
