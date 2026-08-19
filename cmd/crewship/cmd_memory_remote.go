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

// memoryProjection mirrors the `projection` object on
// GET /api/v1/memory/versions (internal/memory/projection.go). It answers the
// question the entry list cannot: is this path one the audit trail records at
// all?
//
//	recorded    — a writer projects this path, so an empty list is a FACT:
//	              nothing has been written yet.
//	unrecorded  — no writer projects this path. An empty list is not evidence
//	              of anything; the file on disk may have changed a hundred
//	              times.
//	unavailable — the server has no blob root, so no path of any tier is
//	              recorded. Same reading rule as unrecorded, different cause.
//
// Reason is free-form prose composed per path on the server. It is passed
// through verbatim rather than paraphrased here: the two would drift apart the
// first time either changed, and the server is the side that knows why.
type memoryProjection struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
}

const (
	projectionRecorded    = "recorded"
	projectionUnrecorded  = "unrecorded"
	projectionUnavailable = "unavailable"
)

// readable reports whether an empty entry list means "nothing was written".
// The zero value — a server old enough to predate the field — is readable, by
// the same compatibility rule the memory tab applies (`data.projection ??
// RECORDED`). Reporting an older server as "unavailable" would tell an
// operator their versioning was switched off when it is merely older.
func (p memoryProjection) readable() bool {
	return p.State == "" || p.State == projectionRecorded
}

// notice returns the line(s) to print above the table, or "" when the table
// speaks for itself. This is the whole point of the projection reaching the
// CLI: before it did, all three states below rendered as the identical
// two-line "(no results)", so an operator could not tell "nothing has been
// written" from "we could not look".
func (p memoryProjection) notice(path string, count int) string {
	if p.readable() {
		if count > 0 {
			return "" // the rows are the answer
		}
		if p.State == "" {
			// Older server: it cannot distinguish the two, so neither can we.
			// Say only what is true — the list came back empty.
			return fmt.Sprintf("%sNo versions recorded for %s.%s",
				cli.Dim, path, cli.Reset)
		}
		return fmt.Sprintf("%sNo versions recorded for %s — nothing has been written to it yet.%s\n%s%s%s",
			cli.Dim, path, cli.Reset, cli.Dim, p.Reason, cli.Reset)
	}
	// Unreadable. Lead with the state so it is greppable, and never let the
	// empty list read as a finding.
	verb := "is not recorded"
	if p.State == projectionUnavailable {
		verb = "cannot be collected on this server"
	}
	return fmt.Sprintf("%sThis history %s (%s).%s\n%s%s%s\n%sAn empty list here says nothing about what is on disk.%s",
		cli.Yellow, verb, p.State, cli.Reset,
		cli.Dim, p.Reason, cli.Reset,
		cli.Yellow, cli.Reset)
}

var memoryVersionsListCmd = &cobra.Command{
	Use:   "list <path>",
	Short: "List versions of a memory path newest-first (server API)",
	Long: `List the recorded versions of one memory path, newest first.

Alongside the versions the server reports a PROJECTION state, which the output
leads with whenever it changes how the list should be read:

  recorded      a writer records this path, so an empty list means nothing has
                been written to it yet
  unrecorded    no writer records this path — an empty list says nothing about
                what is actually on disk
  unavailable   this server has no blob root, so no write of any tier is
                recorded anywhere

The distinction is the point: "nothing here" and "we could not look" are
different answers, and before the state was surfaced they printed the same
empty table. --format json carries the state and its reason verbatim.

Examples:
  crewship memory versions list crew:c1/CREW.md
  crewship memory versions list agent:martin/AGENT.md --limit 50
  crewship memory versions list agent:martin/lessons.md --format json`,
	Args: cobra.ExactArgs(1),
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
			Path  string `json:"path"`
			Count int    `json:"count"`
			// Projection must be on this struct and not read ad hoc: f.Auto
			// re-marshals `out` for --format json/yaml/ndjson, so a field
			// missing here is a field missing from the machine output an
			// agent reads. Dropping it was the defect.
			Projection memoryProjection `json:"projection"`
			Entries    []map[string]any `json:"entries"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		f := newFormatter()
		// WRITTEN reads `written_at`. The column used to read `created_at`,
		// which memory.VersionEntry has never had a field for, so it rendered
		// blank on every row ever listed — and the stub in
		// cmd_memory_remote_test.go baked the same wrong key in, which is why
		// nothing caught it. WRITTEN BY is what makes this an audit chain
		// rather than a list of hashes.
		headers := []string{"SHA256", "WRITTEN", "BYTES", "WRITTEN BY"}
		rows := make([][]string, 0, len(out.Entries))
		for _, e := range out.Entries {
			rows = append(rows, []string{
				str(e["sha256"]), str(e["written_at"]), str(e["bytes"]), str(e["written_by"]),
			})
		}
		// Human formats only. json/yaml/ndjson carry the projection in the
		// payload, and quiet exists to be piped — a prose banner on either
		// would be corruption, not context.
		if notice := out.Projection.notice(args[0], out.Count); notice != "" {
			switch f.Format {
			case "json", "yaml", "ndjson", "quiet":
			default:
				fmt.Println(notice)
			}
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
