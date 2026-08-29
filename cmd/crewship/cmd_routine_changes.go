package main

// `routine changes <run_id>` — GET .../pipeline-runs/{runId}/changes.
//
// Sibling of tree/metadata/signal/logs: resolves the run's crew and reports
// that crew container's uncommitted git diff (the Activity dock's Changes
// tab). A run with no resolvable crew, or a crew container that isn't a git
// repo (or isn't reachable), degrades to is_repo:false rather than erroring
// — see ProxyHandler.RunGitDiff (internal/api/proxy.go) and
// handleContainerGitDiff / parseGitDiff (internal/server/routes_container.go),
// which is where the {is_repo, files, diff, truncated} shape below comes
// from — it is not an OpenAPI guess.

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// runChangedFile is one file in a `routine changes` diff summary. Mirrors
// gitChangedFile (internal/server/routes_container.go).
type runChangedFile struct {
	Path      string `json:"path" yaml:"path"`
	Status    string `json:"status" yaml:"status"`
	Additions int    `json:"additions" yaml:"additions"`
	Deletions int    `json:"deletions" yaml:"deletions"`
}

// runChangesResult mirrors parseGitDiff's response map
// (internal/server/routes_container.go): either {is_repo:false[,error]} or
// {is_repo:true, files, diff, truncated}.
type runChangesResult struct {
	IsRepo    bool             `json:"is_repo" yaml:"is_repo"`
	Error     string           `json:"error,omitempty" yaml:"error,omitempty"`
	Files     []runChangedFile `json:"files,omitempty" yaml:"files,omitempty"`
	Diff      string           `json:"diff,omitempty" yaml:"diff,omitempty"`
	Truncated bool             `json:"truncated,omitempty" yaml:"truncated,omitempty"`
}

var routineChangesCmd = &cobra.Command{
	Use:   "changes <run_id>",
	Short: "Show the git diff a run's crew container produced",
	Long: `Resolves the run to its crew (invoking_crew_id, falling back to the
routine's author crew) and reports that crew container's uncommitted git
changes: a file list with add/delete counts, plus the unified diff.

A run with no resolvable crew, or whose crew container isn't a live git
repo, answers is_repo:false rather than an error — that is the normal
case for a workspace-level routine with no crew attached, not a failure.

Examples:
  crewship routine changes run_abc123
  crewship routine changes run_abc123 --format json | jq '.files[].path'
`,
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
		resp, err := client.Get(fmt.Sprintf("/api/v1/workspaces/%s/pipeline-runs/%s/changes", ws, args[0]))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var result runChangesResult
		if err := cli.ReadJSON(resp, &result); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		if result.Files == nil {
			result.Files = []runChangedFile{}
		}
		return resolvedFormatter(cmd).AutoHuman(result, func() {
			if !result.IsRepo {
				msg := "No git changes: run's crew container is not a git repo (or has no resolvable crew)."
				if result.Error != "" {
					msg = fmt.Sprintf("No git changes: %s", result.Error)
				}
				fmt.Println(msg)
				return
			}
			if len(result.Files) == 0 {
				fmt.Println("No uncommitted changes.")
				return
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "STATUS\tPATH\t+\t-")
			for _, f := range result.Files {
				fmt.Fprintf(w, "%s\t%s\t%d\t%d\n", f.Status, f.Path, f.Additions, f.Deletions)
			}
			_ = w.Flush()
			if result.Diff != "" {
				fmt.Println()
				fmt.Println(result.Diff)
				if result.Truncated {
					fmt.Println("\n(diff truncated)")
				}
			}
		})
	},
}

func init() {
	pipelineCmd.AddCommand(routineChangesCmd)
}
