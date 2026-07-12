package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// Additional routine subcommands beyond the original 7 (list / get /
// save / run / dry-run / delete / runs). These cover the version
// management, bundle export/import, and live-run cancel surfaces that
// previously lived only in the API + UI.

// ---- versions ----

type pipelineVersionRow struct {
	Version        int    `json:"version"`
	IsHead         bool   `json:"is_head"`
	ParentVersion  *int   `json:"parent_version,omitempty"`
	DefinitionHash string `json:"definition_hash"`
	AuthorType     string `json:"author_type"`
	AuthorID       string `json:"author_id"`
	ChangeSummary  string `json:"change_summary,omitempty"`
	CreatedAt      string `json:"created_at"`
}

var routineVersionsCmd = &cobra.Command{
	Use:   "versions <slug>",
	Short: "List all versions of a routine",
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
		resp, err := client.Get(fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/versions", ws, args[0]))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var rows []pipelineVersionRow
		if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		if len(rows) == 0 {
			fmt.Println("No version history yet.")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "VERSION\tHEAD\tPARENT\tHASH\tAUTHOR\tCREATED\tSUMMARY")
		// HEAD comes from the server's is_head (pipelines.head_version) —
		// after a rollback it sits on an OLDER row, so guessing rows[0]
		// (max version) shows a stale HEAD (#996). Servers predating the
		// field mark nothing; fall back to the old max-version heuristic —
		// but ONLY when the page isn't full. On a full page (server default
		// LIMIT 100), a new server's head may simply live beyond the page;
		// printing no marker beats confidently marking the wrong row.
		serverMarksHead := false
		for _, v := range rows {
			if v.IsHead {
				serverMarksHead = true
				break
			}
		}
		const versionsPageCap = 100
		guessHead := !serverMarksHead && len(rows) < versionsPageCap
		for _, v := range rows {
			isHead := ""
			if v.IsHead || (guessHead && v.Version == rows[0].Version) {
				isHead = "*"
			}
			parent := "—"
			if v.ParentVersion != nil {
				parent = fmt.Sprintf("v%d", *v.ParentVersion)
			}
			summary := v.ChangeSummary
			if len(summary) > 50 {
				summary = summary[:47] + "..."
			}
			fmt.Fprintf(w, "v%d\t%s\t%s\t%s\t%s/%s\t%s\t%s\n",
				v.Version, isHead, parent, truncIDForCLI(v.DefinitionHash, 12),
				v.AuthorType, truncIDForCLI(v.AuthorID, 12), v.CreatedAt, summary)
		}
		return w.Flush()
	},
}

// ---- versions show ----

// routineVersionsShowCmd fetches one specific version including its
// DSL definition. Useful for diffing two versions of a routine
// (the standard `versions <slug>` lists revisions, but doesn't dump
// the definition; this one does).
var routineVersionsShowCmd = &cobra.Command{
	Use:   "show <slug>",
	Short: "Show full definition of a specific routine version",
	Long: `Fetches GET /pipelines/{slug}/versions/{n}. The response includes
definition_hash, parent_version, change_summary, and the full DSL JSON
for the requested version. Pipe to jq for selective extraction.

Examples:
  crewship routine versions show my-routine --version 3
  crewship routine versions show my-routine --version 3 | jq '.definition'
`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		version, _ := cmd.Flags().GetInt("version")
		if version <= 0 {
			return fmt.Errorf("--version <n> is required (positive integer)")
		}
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		resp, err := client.Get(fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/versions/%d", ws, args[0], version))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		// Pretty-print the JSON so it's readable on a terminal but
		// still valid for piping. Indent at 2 spaces — matches the
		// rest of the CLI's JSON output style.
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("read response: %w", err)
		}
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, raw, "", "  "); err != nil {
			// Not valid JSON? Surface the raw bytes so the user can
			// see what came back.
			fmt.Println(string(raw))
			return nil
		}
		fmt.Println(pretty.String())
		return nil
	},
}

// ---- active runs ----

// routineActiveCmd lists workspace-wide in-flight pipeline runs. Calls
// GET /pipelines/runs/active (single-replica scope per the handler
// note). Empty list when nothing is running.
var routineActiveCmd = &cobra.Command{
	Use:   "active",
	Short: "List in-flight routine runs across the workspace",
	RunE: func(cmd *cobra.Command, _ []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		resp, err := client.Get(fmt.Sprintf("/api/v1/workspaces/%s/pipelines/runs/active", ws))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var rows []struct {
			RunID           string `json:"run_id"`
			PipelineSlug    string `json:"pipeline_slug"`
			ConcurrencyKey  string `json:"concurrency_key"`
			StartedAt       string `json:"started_at"`
			CancelRequested bool   `json:"cancel_requested"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		if len(rows) == 0 {
			fmt.Println("No active runs.")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "RUN_ID\tSLUG\tSTARTED\tCANCEL_REQ\tCONCURRENCY_KEY")
		for _, r := range rows {
			cancelMark := ""
			if r.CancelRequested {
				cancelMark = "yes"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
				truncIDForCLI(r.RunID, 24), r.PipelineSlug, r.StartedAt, cancelMark, r.ConcurrencyKey)
		}
		return w.Flush()
	},
}

// ---- rollback ----

var routineRollbackCmd = &cobra.Command{
	Use:   "rollback <slug>",
	Short: "Roll a routine back to a previous version",
	Long: `Repoints the routine's HEAD at the target version and makes its
definition live. No new version row is created (versions are deduped
by content hash); history is preserved, so you can re-roll forward by
another rollback. 'crewship routine versions' marks the new HEAD.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		target, _ := cmd.Flags().GetInt("to")
		if target <= 0 {
			return fmt.Errorf("--to <version> is required (positive integer)")
		}
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		body := mustJSON(map[string]any{"target_version": target})
		resp, err := client.Post(
			fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/rollback", ws, args[0]),
			bytes.NewReader(body),
		)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		fmt.Printf("Rolled back %s to v%d.\n", args[0], target)
		return nil
	},
}

// ---- export ----

var routineExportCmd = &cobra.Command{
	Use:   "export <slug>",
	Short: "Export a routine bundle as JSON to stdout",
	Long: `Writes the full routine bundle (definition + version history) to
stdout. Pipe to a file: 'crewship routine export my-routine > bundle.json'.
The output is the same shape that 'crewship routine import' consumes,
suitable for transferring routines between workspaces.

A routine's ` + "`type: script`" + ` step files are inlined into the bundle
(base64) from the routine's author crew, so a portable routine travels with
its deterministic backbone — recipe + scripts + agent judgment. The crew
manifest ` + "`files:`" + ` block remains the source of truth; inlining is a
portability convenience. Pass --no-scripts to export the recipe alone.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		includeHistory, _ := cmd.Flags().GetBool("include-history")
		noScripts, _ := cmd.Flags().GetBool("no-scripts")
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		path := fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/export", ws, args[0])
		if includeHistory {
			path += "?include_history=1"
		}
		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		if !noScripts {
			raw, err = augmentBundleWithScripts(cmd.Context(), client, ws, args[0], raw)
			if err != nil {
				return err
			}
		}
		_, err = os.Stdout.Write(raw)
		return err
	},
}

// augmentBundleWithScripts inlines the routine's script-step files into the
// export bundle. Resolves the routine's author crew (that's where the scripts
// live), downloads each, and adds a top-level `scripts` array. A routine with
// no script steps returns the bundle unchanged.
func augmentBundleWithScripts(ctx context.Context, client *cli.Client, ws, slug string, raw []byte) ([]byte, error) {
	var bundle map[string]any
	if err := json.Unmarshal(raw, &bundle); err != nil {
		return nil, fmt.Errorf("decode export bundle: %w", err)
	}
	pipeMap, _ := bundle["pipeline"].(map[string]any)
	if pipeMap == nil {
		return raw, nil
	}
	defRaw, err := json.Marshal(pipeMap["definition"])
	if err != nil {
		return nil, fmt.Errorf("re-encode definition: %w", err)
	}
	paths, err := collectScriptPaths(defRaw)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return raw, nil
	}
	// Resolve the author crew — scripts are a crew asset delivered there.
	var p struct {
		AuthorCrewID string `json:"author_crew_id"`
	}
	if err := getJSON(client, fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s", ws, slug), &p); err != nil {
		return nil, fmt.Errorf("resolve author crew for script inlining: %w", err)
	}
	entries, err := inlineScripts(newClientCrewFileIO(ctx, client), p.AuthorCrewID, defRaw)
	if err != nil {
		return nil, err
	}
	bundle["scripts"] = entries
	out, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("re-encode bundle: %w", err)
	}
	return out, nil
}

// ---- import ----

var routineImportCmd = &cobra.Command{
	Use:   "import [bundle.json]",
	Short: "Import a routine bundle from a file or stdin",
	Long: `Reads a routine bundle JSON (the output of 'crewship routine export')
from the given file argument or from stdin if no argument. Existing
routines with the same slug are updated; new routines are created.

--crew names the author crew that will OWN the imported routine (required —
it resolves the routine's agent slugs). Any scripts inlined in the bundle are
materialized into that crew's shared dir via the same /files/save path
'crewship apply' uses. A script whose dest already exists with DIFFERENT
content fails loudly — pass --force to overwrite (a crew script is shared
across routines). --no-scripts imports the recipe without materializing.

Examples:
  crewship routine import bundle.json --crew acct
  cat bundle.json | crewship routine import --crew acct
  crewship routine export src-routine | crewship routine import --crew acct
`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		var raw []byte
		var err error
		if len(args) == 1 {
			raw, err = os.ReadFile(args[0])
			if err != nil {
				return fmt.Errorf("read bundle file: %w", err)
			}
		} else {
			raw, err = io.ReadAll(os.Stdin)
			if err != nil {
				return fmt.Errorf("read stdin: %w", err)
			}
			if len(raw) == 0 {
				return fmt.Errorf("empty bundle (no file argument and stdin is empty)")
			}
		}

		// Decode the bundle first so a keyboard typo is a local error, not a
		// 400 — and so an invalid bundle fails before we demand a crew.
		var bundle map[string]any
		if err := json.Unmarshal(raw, &bundle); err != nil {
			return fmt.Errorf("bundle is not valid JSON: %w", err)
		}

		client := newAPIClient()
		ws := client.GetWorkspaceID()

		crewSlug, _ := cmd.Flags().GetString("crew")
		if crewSlug == "" {
			return fmt.Errorf("--crew is required (the author crew that will own the imported routine)")
		}
		crewID, err := resolveCrewID(client, crewSlug)
		if err != nil {
			return err
		}
		force, _ := cmd.Flags().GetBool("force")
		noScripts, _ := cmd.Flags().GetBool("no-scripts")

		if !noScripts {
			scripts, err := decodeBundleScripts(bundle)
			if err != nil {
				return err
			}
			if err := materializeScripts(newClientCrewFileIO(cmd.Context(), client), crewID, scripts, force); err != nil {
				return err
			}
			if n := len(scripts); n > 0 {
				fmt.Fprintf(os.Stderr, "%s[materialized %d script(s) into crew %s]%s\n", cli.Dim, n, crewSlug, cli.Reset)
			}
		}
		bundle["author_crew_id"] = crewID
		body, err := json.Marshal(bundle)
		if err != nil {
			return fmt.Errorf("re-encode bundle: %w", err)
		}
		resp, err := client.Post(
			fmt.Sprintf("/api/v1/workspaces/%s/pipelines/import", ws),
			bytes.NewReader(body),
		)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		respBody, _ := io.ReadAll(resp.Body)
		var out struct {
			Slug string `json:"slug"`
			ID   string `json:"id"`
		}
		_ = json.Unmarshal(respBody, &out)
		if out.Slug != "" {
			fmt.Printf("Imported routine %q (id=%s).\n", out.Slug, out.ID)
		} else {
			fmt.Println(string(respBody))
		}
		return nil
	},
}

// ---- cancel ----

var routineCancelCmd = &cobra.Command{
	Use:   "cancel <run_id>",
	Short: "Cancel an in-flight routine run",
	Long: `Signals the run's goroutine to stop at the next safe point. The run
is marked failed with a cancellation reason. Already-completed and
already-failed runs return 409.`,
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
		resp, err := client.Post(
			fmt.Sprintf("/api/v1/workspaces/%s/pipelines/runs/%s/cancel", ws, args[0]),
			http.NoBody,
		)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		fmt.Printf("Cancellation signaled for run %s.\n", args[0])
		return nil
	},
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("cmd_routine_extra: marshal: %v", err))
	}
	return b
}

func truncIDForCLI(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.TrimRight(s[:n], "_-") + "…"
}

func init() {
	routineRollbackCmd.Flags().Int("to", 0, "target version to roll back to (REQUIRED)")
	_ = routineRollbackCmd.MarkFlagRequired("to")
	routineExportCmd.Flags().Bool("include-history", false, "include all versions in the bundle (otherwise just HEAD)")
	routineExportCmd.Flags().Bool("no-scripts", false, "export the recipe alone; do not inline `type: script` files")
	routineImportCmd.Flags().String("crew", "", "author crew slug/id that will own the imported routine (REQUIRED)")
	routineImportCmd.Flags().Bool("force", false, "overwrite an existing crew script whose content differs")
	routineImportCmd.Flags().Bool("no-scripts", false, "import the recipe without materializing inlined scripts")

	// `versions` is both a list (when invoked alone) AND a parent of
	// `versions show`. Cobra handles the dual role fine — args route
	// to the parent's RunE, subcommands to their own.
	routineVersionsShowCmd.Flags().Int("version", 0, "target version number (REQUIRED)")
	_ = routineVersionsShowCmd.MarkFlagRequired("version")
	routineVersionsCmd.AddCommand(routineVersionsShowCmd)

	pipelineCmd.AddCommand(routineVersionsCmd)
	pipelineCmd.AddCommand(routineRollbackCmd)
	pipelineCmd.AddCommand(routineExportCmd)
	pipelineCmd.AddCommand(routineImportCmd)
	pipelineCmd.AddCommand(routineCancelCmd)
	pipelineCmd.AddCommand(routineActiveCmd)
}
