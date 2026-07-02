package main

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// Server-side memory commands. The sibling subcommands in cmd_memory.go
// (search/status/reindex) and cmd_memory_versions.go (log/show/restore)
// operate on the LOCAL filesystem/DB and only work on the server host;
// these hit the running server's API, which is the path a remote
// operator or an agent actually has.

var memoryHybridCmd = &cobra.Command{
	Use:   "hybrid <query>",
	Short: "Hybrid memory search via the server (FTS + episodic recall)",
	Long: `Search workspace memory through the server's hybrid engine — full-text
chunks plus episodic journal recall, merged and ranked.

Unlike 'memory search' (local filesystem FTS), this requires a login
token and works from any machine.

Examples:
  crewship memory hybrid "deploy runbook"
  crewship memory hybrid "API key rotation" --limit 5 --scope crew_shared --crew backend`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		limit, _ := cmd.Flags().GetInt("limit")
		scope, _ := cmd.Flags().GetString("scope")
		client := newAPIClient()
		body := map[string]any{"query": args[0], "limit": limit, "scope": scope}
		if crewRef, _ := cmd.Flags().GetString("crew"); crewRef != "" {
			crewID, err := resolveCrewID(client, crewRef)
			if err != nil {
				return err
			}
			body["crew_id"] = crewID
		}
		resp, err := client.Post("/api/v1/memory/search/hybrid", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out struct {
			Query string           `json:"query"`
			Count int              `json:"count"`
			Hits  []map[string]any `json:"hits"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		f := newFormatter()
		headers := []string{"SOURCE", "SCORE", "SNIPPET"}
		rows := make([][]string, 0, len(out.Hits))
		for _, h := range out.Hits {
			score := ""
			if v, ok := h["score"].(float64); ok {
				score = strconv.FormatFloat(v, 'f', 3, 64)
			}
			rows = append(rows, []string{str(h["source"]), score, str(h["snippet"])})
		}
		return f.Auto(out, headers, rows)
	},
}

var memoryVersionsCmd = &cobra.Command{
	Use:   "versions",
	Short: "Memory version audit chain via the server API",
	Long: `Read and recover memory versions through the running server. The
sibling 'memory log/show/restore' commands read the DB directly and
only work on the server host; these work from anywhere the CLI can
reach the API. The workspace comes from the auth context.`,
}

var memoryVersionsListCmd = &cobra.Command{
	Use:   "list <path>",
	Short: "List versions of a memory path newest-first (server API)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		limit, _ := cmd.Flags().GetInt("limit")
		q := url.Values{}
		q.Set("path", args[0])
		if limit > 0 {
			q.Set("limit", strconv.Itoa(limit))
		}
		client := newAPIClient()
		resp, err := client.Get("/api/v1/memory/versions?" + q.Encode())
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out struct {
			Path    string           `json:"path"`
			Count   int              `json:"count"`
			Entries []map[string]any `json:"entries"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		f := newFormatter()
		headers := []string{"SHA256", "CREATED", "BYTES"}
		rows := make([][]string, 0, len(out.Entries))
		for _, e := range out.Entries {
			rows = append(rows, []string{str(e["sha256"]), str(e["created_at"]), str(e["bytes"])})
		}
		return f.Auto(out, headers, rows)
	},
}

var memoryVersionsShowCmd = &cobra.Command{
	Use:   "show <path> <sha>",
	Short: "Print a memory version's raw content to stdout (server API)",
	Long: `Stream the content-addressed blob for one version. Stdout is the raw
bytes (pipe-friendly); status and errors go to stderr.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		q := url.Values{}
		q.Set("path", args[0])
		client := newAPIClient()
		resp, err := client.Get("/api/v1/memory/versions/" + url.PathEscape(args[1]) + "?" + q.Encode())
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		defer resp.Body.Close()
		_, err = io.Copy(os.Stdout, resp.Body)
		return err
	},
}

var memoryVersionsRestoreCmd = &cobra.Command{
	Use:   "restore <path> <sha> <canonical-path>",
	Short: "Restore a memory version to its canonical file (server API, OWNER/ADMIN)",
	Long: `Restore an older version's content into the canonical memory file.
The server confines <canonical-path> to its configured memory root.

Requires OWNER or ADMIN role. Prompts for confirmation; --yes skips.`,
	Args: cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		tier, _ := cmd.Flags().GetString("tier")
		if tier == "" {
			return fmt.Errorf("--tier is required (agent|crew|workspace|pins|learned)")
		}
		if err := confirmAction(cmd, fmt.Sprintf("Restore %s@%s over %s?", args[0], args[1], args[2])); err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Post("/api/v1/memory/versions/"+url.PathEscape(args[1])+"/restore", map[string]any{
			"path":           args[0],
			"canonical_path": args[2],
			"tier":           tier,
		})
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out map[string]any
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		f := newFormatter()
		pairs := [][]string{
			{"Restored", str(out["restored_sha"])},
			{"Path", str(out["path"])},
			{"Canonical", str(out["canonical_path"])},
		}
		if err := f.AutoDetail(out, pairs); err != nil {
			return err
		}
		if f.Format == "table" || f.Format == "" {
			cli.PrintSuccess("Memory version restored.")
		}
		return nil
	},
}

func init() {
	memoryHybridCmd.Flags().Int("limit", 10, "maximum hits to return")
	memoryHybridCmd.Flags().String("scope", "", "scope filter: '' (all visible) | own | crew_shared")
	memoryHybridCmd.Flags().String("crew", "", "crew slug or id for crew_shared scope")

	memoryVersionsListCmd.Flags().Int("limit", 20, "maximum versions to list")
	memoryVersionsRestoreCmd.Flags().String("tier", "", "memory tier: agent|crew|workspace|pins|learned (required)")
	memoryVersionsRestoreCmd.Flags().Bool("yes", false, "skip the confirmation prompt")

	memoryVersionsCmd.AddCommand(memoryVersionsListCmd)
	memoryVersionsCmd.AddCommand(memoryVersionsShowCmd)
	memoryVersionsCmd.AddCommand(memoryVersionsRestoreCmd)
	memoryCmd.AddCommand(memoryHybridCmd)
	memoryCmd.AddCommand(memoryVersionsCmd)
}
