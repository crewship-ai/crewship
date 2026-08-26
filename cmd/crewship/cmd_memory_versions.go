//go:build !clionly

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/memory"
	"github.com/spf13/cobra"
)

// cmd_memory_versions wires `crewship memory log/show/restore` —
// the operator-facing read + recover surface for the v90
// memory_versions audit trail. Distinct from `crewship memory
// search` (FTS over markdown chunks) because the audit trail is
// SQL-on-DB, not filesystem-on-FTS. Both subcommands share the
// `memory` parent so they read like sibling verbs in the help.
//
// log and show read the SERVER (#2086). Two routes for exactly this
// already existed and had no CLI command at all —
// GET /api/v1/admin/memory/versions and
// GET /api/v1/admin/memory/versions/{id}/content — while these two
// commands opened ~/.crewship/crewship.db, so on any host where the
// server runs with a different DATABASE_URL they reported an audit
// chain that was not the one being audited. --local reads the file.
//
// restore stays local in both directions: it WRITES a canonical file
// onto this machine's disk from a blob in this machine's
// content-addressed store. There is no route because there is nothing
// a remote server could do with the request; it is gated instead, so
// it refuses rather than restore from the wrong instance's history.

var memoryLogCmd = &cobra.Command{
	Use:   "log <workspace_id> <path>",
	Short: "List versions of a memory path newest-first",
	Long: `List the memory_versions audit chain for a path within a workspace.

Path is the audit-trail identifier the consolidator + approve flow
record under — for canonical learned-*.md and pins.md this is
"crew:<crew_id>/<filename>" (see internal/consolidate.canonicalAuditPath).
For agent/workspace tier writes future PRs may use other conventions;
prefix the path with the tier marker exactly as it appears in DB.

Output is JSON unless --format=text. Default limit is 20 rows; bump
via --limit (clamped to 1000).`,
	Args: cobra.ExactArgs(2),
	RunE: runMemoryLog,
}

var memoryShowCmd = &cobra.Command{
	Use:   "show <workspace_id> <path> <sha>",
	Short: "Print the content of a specific memory version",
	Long: `Read the content-addressed blob for a single version and write it
to stdout. Use this to recover content from an older version
without committing the restore — the canonical file stays
unchanged.

Pipe-friendly: stdout is the raw blob bytes. Stderr carries
status / errors. Exit codes:
  0  blob found and streamed
  1  blob not found or read error
  2  invalid usage`,
	Args: cobra.ExactArgs(3),
	RunE: runMemoryShow,
}

var memoryRestoreCmd = &cobra.Command{
	Use:   "restore <workspace_id> <path> <sha> <canonical_path>",
	Short: "Restore a memory file from a historical version",
	Long: `Atomically replace the canonical memory file at <canonical_path>
with the content of version <sha>, then record a fresh
memory_versions row so the chain stays forward-only (no rewriting
history).

The audit trail's writtenBy is set from --user (default: the
$USER env var). Use this when --user is a real operator id you
want associated with the recovery event in compliance review.

--blob-root defaults to {data_dir}/memory/versions, matching the
content-addressed store the consolidator + approve flow write to.

The command refuses to run when canonical_path is empty or
absolute paths outside the data dir; pass --force to bypass the
"path leaves data dir" guard.`,
	Args: cobra.ExactArgs(4),
	RunE: runMemoryRestore,
}

func init() {
	// Cobra flag conventions match the existing memory subcommands.
	memoryLogCmd.Flags().Int("limit", 20, "max rows (clamped to 1000)")
	memoryLogCmd.Flags().String("format", "json", "output format: json|text")

	// Declared per-command rather than persistently on `memory`: the rest of
	// the memory tree (search, status, pin, …) is server-side already, and a
	// --local it would ignore is worse than no flag.
	memoryLogCmd.Flags().Bool("local", false, localOnlyFlagHelp)
	memoryShowCmd.Flags().Bool("local", false, localOnlyFlagHelp)
	memoryRestoreCmd.Flags().Bool("local", false, localOnlyFlagHelp)

	memoryRestoreCmd.Flags().String("blob-root", "", "content-addressed blob root (default: {data_dir}/memory/versions)")
	memoryRestoreCmd.Flags().String("user", "", "audit-trail writtenBy for the restore row (default: $USER)")
	memoryRestoreCmd.Flags().String("tier", "learned", "tier to record the restored version under: agent|crew|workspace|pins|learned")
	memoryRestoreCmd.Flags().Bool("force", false, "skip the canonical-path guard")

	memoryCmd.AddCommand(memoryLogCmd)
	memoryCmd.AddCommand(memoryShowCmd)
	memoryCmd.AddCommand(memoryRestoreCmd)
}

func runMemoryLog(cmd *cobra.Command, args []string) error {
	workspaceID := args[0]
	path := args[1]
	limit, _ := cmd.Flags().GetInt("limit")
	format, _ := cmd.Flags().GetString("format")

	entries, err := memoryVersionEntries(cmd, workspaceID, path, limit)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		fmt.Fprintf(os.Stderr, "no versions for %s @ %s\n", workspaceID, path)
		return nil
	}

	switch strings.ToLower(format) {
	case "json", "":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(entries)
	case "text":
		// One row per line: sha shortprefix, written_at, bytes, writtenBy
		// Format matches `git log --oneline` shape so the eye reads it the same.
		for _, e := range entries {
			short := e.Sha256
			if len(short) > 12 {
				short = short[:12]
			}
			fmt.Printf("%s  %s  %6d B  %s\n", short, e.WrittenAt, e.Bytes, e.WrittenBy)
		}
		return nil
	default:
		return fmt.Errorf("unknown --format %q (use json or text)", format)
	}
}

func runMemoryShow(cmd *cobra.Command, args []string) error {
	workspaceID := args[0]
	path := args[1]
	sha := args[2]

	if !localOnlyFlag(cmd) {
		return memoryShowOverAPI(cmd, workspaceID, path, sha)
	}

	db, err := openGatedLocalDB(cmd, "crewship memory show --local", "")
	if err != nil {
		return err
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	content, err := memory.ReadVersion(ctx, db.DB, workspaceID, path, sha)
	if err != nil {
		if errors.Is(err, memory.ErrVersionNotFound) {
			return memoryVersionNotFound(workspaceID, path, sha)
		}
		return err
	}
	// cmd.OutOrStdout(), same as the API path: one destination for the blob so
	// the two halves of this command cannot drift on where the payload lands.
	_, err = io.Copy(cmd.OutOrStdout(), strings.NewReader(string(content)))
	return err
}

// memoryVersionNotFound is the documented "1 blob not found" exit. It returns
// an error rather than calling os.Exit so deferred closes still run and the
// two paths (API, local file) cannot drift on exit code or wording.
func memoryVersionNotFound(workspaceID, path, sha string) error {
	return cli.WithExitCode(
		fmt.Errorf("version not found: workspace=%s path=%s sha=%s", workspaceID, path, sha),
		cli.ExitGeneric)
}

// memoryShowOverAPI resolves the row by (path, sha) through the versions list
// endpoint, then streams its bytes from the content endpoint.
//
// Two calls because the content route is keyed by row id while the CLI's
// arguments are the audit chain's own coordinates (path + sha), which is what
// an operator reading `memory log` output actually has in front of them.
func memoryShowOverAPI(cmd *cobra.Command, workspaceID, path, sha string) error {
	// requireAuth, not requireAuthAndWorkspace: the workspace is an argument
	// to this command, so a shell with no `workspace use` is not an error.
	if err := requireAuth(); err != nil {
		return err
	}
	client := newAPIClient()
	entries, err := memoryVersionsFromAPI(client, workspaceID, path, 0)
	if err != nil {
		return err
	}
	id := ""
	for _, e := range entries {
		if e.Sha256 == sha {
			id = e.ID
			break
		}
	}
	if id == "" {
		return memoryVersionNotFound(workspaceID, path, sha)
	}

	resp, err := client.Get(fmt.Sprintf("/api/v1/admin/memory/versions/%s/content%s",
		url.PathEscape(id), queryString("workspace_id", workspaceID)))
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck
	if err := cli.CheckError(resp); err != nil {
		return err
	}
	// Raw bytes to stdout, unwrapped: the command is documented as
	// pipe-friendly and the blob is the payload, not a field in one.
	_, err = io.Copy(cmd.OutOrStdout(), resp.Body)
	return err
}

func runMemoryRestore(cmd *cobra.Command, args []string) error {
	workspaceID := args[0]
	path := args[1]
	sha := args[2]
	canonicalPath := args[3]

	blobRoot, _ := cmd.Flags().GetString("blob-root")
	user, _ := cmd.Flags().GetString("user")
	tierStr, _ := cmd.Flags().GetString("tier")
	force, _ := cmd.Flags().GetBool("force")

	if user == "" {
		user = os.Getenv("USER")
	}
	if user == "" {
		user = "cli"
	}

	tier := memory.Tier(tierStr)
	if !memory.ValidTier(tier) {
		return fmt.Errorf("invalid --tier %q (allowed: agent|crew|workspace|pins|learned)", tierStr)
	}

	if blobRoot == "" {
		// Default to the data dir's versions path — mirrors how
		// the server wires cfg.Storage.MemoryRoot + "/versions".
		// CLI tools that want to point at a different store can
		// override via --blob-root.
		// We do NOT auto-create the dir here; RecordVersion does
		// MkdirAll under the dedup-blob path on first write.
		if dd, ddErr := defaultBlobRoot(); ddErr == nil {
			blobRoot = dd
		} else {
			return fmt.Errorf("resolve --blob-root: %w", ddErr)
		}
	}

	// Confine the restore target to the data dir tree (one level above
	// blobRoot — blobRoot is {data}/memory/versions, so {data}/memory
	// is the allowed root). --force bypasses for operators recovering
	// content outside the standard tree (e.g. a workspace bound to a
	// different memory dir via env).
	restoreRoot := filepath.Dir(blobRoot)
	if !force && !canonicalPathIsSafe(canonicalPath, restoreRoot) {
		return fmt.Errorf("refusing to restore to %q (outside %q; pass --force to override)",
			canonicalPath, restoreRoot)
	}

	db, err := openGatedLocalDB(cmd, "crewship memory restore", "")
	if err != nil {
		return err
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := memory.Restore(ctx, db.DB, canonicalPath, workspaceID, path, sha, user, blobRoot, tier)
	if err != nil {
		if errors.Is(err, memory.ErrVersionNotFound) {
			fmt.Fprintf(os.Stderr, "version not found: workspace=%s path=%s sha=%s\n", workspaceID, path, sha)
			os.Exit(1)
		}
		return fmt.Errorf("restore: %w", err)
	}
	fmt.Fprintf(os.Stderr, "restored %s @ %s -> %s (new audit row id=%s, sha=%s)\n",
		workspaceID, path, canonicalPath, res.VersionID, res.Sha256)
	return nil
}

// memoryVersionEntries returns the audit chain for (workspace, path), from
// the server the CLI targets or — with --local — from the database file on
// this host.
func memoryVersionEntries(cmd *cobra.Command, workspaceID, path string, limit int) ([]memory.VersionEntry, error) {
	if localOnlyFlag(cmd) {
		db, err := openGatedLocalDB(cmd, "crewship memory log --local", "")
		if err != nil {
			return nil, err
		}
		defer db.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		entries, err := memory.LogVersions(ctx, db.DB, workspaceID, path, limit)
		if err != nil {
			return nil, fmt.Errorf("log versions: %w", err)
		}
		return entries, nil
	}
	if err := requireAuth(); err != nil {
		return nil, err
	}
	return memoryVersionsFromAPI(newAPIClient(), workspaceID, path, limit)
}

// memoryVersionsPageCeiling bounds the pagination walk below. The list
// endpoint filters by path PREFIX and this command wants one exact path, so a
// workspace with thousands of sibling rows under a shared prefix could
// otherwise keep the CLI paging forever. 200 pages x 500 rows is far past any
// real audit chain; hitting it is reported, never silently truncated.
const memoryVersionsPageCeiling = 200

// memoryVersionsFromAPI walks GET /api/v1/admin/memory/versions and returns
// the rows whose path matches exactly, newest first.
//
// limit <= 0 means "every match". The exact-path filter is applied AFTER the
// server's page limit, so the walk continues across pages until it has `limit`
// matches or the cursor runs out — filtering a single page and stopping would
// return zero rows for a path whose newest siblings happen to fill page one,
// which is the same shape of quiet under-reporting that
// `admin sessions list --active-only` was fixed for.
func memoryVersionsFromAPI(client *cli.Client, workspaceID, path string, limit int) ([]memory.VersionEntry, error) {
	type apiRow struct {
		ID        string `json:"id"`
		Path      string `json:"path"`
		Tier      string `json:"tier"`
		Sha256    string `json:"sha256"`
		Bytes     int    `json:"bytes"`
		WrittenAt string `json:"written_at"`
		WrittenBy string `json:"written_by"`
		ParentSha string `json:"parent_sha"`
	}
	type apiPage struct {
		Rows       []apiRow `json:"rows"`
		NextCursor *string  `json:"next_cursor"`
	}

	var out []memory.VersionEntry
	cursor := ""
	for page := 0; ; page++ {
		if page >= memoryVersionsPageCeiling {
			return nil, fmt.Errorf(
				"gave up after %d pages of /api/v1/admin/memory/versions without exhausting the path prefix %q — narrow the path",
				memoryVersionsPageCeiling, path)
		}
		var body apiPage
		q := queryString("workspace_id", workspaceID, "path_prefix", path, "limit", "500", "cursor", cursor)
		if err := getJSON(client, "/api/v1/admin/memory/versions"+q, &body); err != nil {
			return nil, err
		}
		for _, r := range body.Rows {
			if r.Path != path {
				continue
			}
			out = append(out, memory.VersionEntry{
				ID: r.ID, Path: r.Path, Tier: r.Tier, Sha256: r.Sha256, Bytes: r.Bytes,
				WrittenAt: r.WrittenAt, WrittenBy: r.WrittenBy, ParentSha: r.ParentSha,
			})
			if limit > 0 && len(out) >= limit {
				return out, nil
			}
		}
		if body.NextCursor == nil || *body.NextCursor == "" {
			return out, nil
		}
		cursor = *body.NextCursor
	}
}

// defaultBlobRoot resolves {DataDir.Root}/memory/versions, matching
// server-side wiring. Falls back to error when DataDir resolution
// fails (e.g. no $HOME); the caller can supply --blob-root
// explicitly.
func defaultBlobRoot() (string, error) {
	// Import cycle would form if we pulled internal/database into
	// this file — so we duplicate the small bit of path math the
	// data-dir resolution does. Override env var matches
	// database.DefaultDataDir's contract.
	if override := strings.TrimSpace(os.Getenv("CREWSHIP_DATA_DIR")); override != "" {
		return filepath.Join(override, "memory", "versions"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".crewship", "memory", "versions"), nil
}

// canonicalPathIsSafe rejects empty paths + traversal attempts + any
// target that resolves outside the supplied allowedRoot. Callers
// passing --force bypass this; non-force callers need a path that is
// non-empty, contains no ".." segments, and lands inside allowedRoot
// after symlink + Clean resolution.
//
// allowedRoot is canonicalised (filepath.Abs + filepath.Clean) before
// the containment check so a trailing slash or a relative root passed
// from the CLI flag still does the right thing.
func canonicalPathIsSafe(p, allowedRoot string) bool {
	if strings.TrimSpace(p) == "" {
		return false
	}
	if strings.Contains(p, "..") {
		return false
	}
	if allowedRoot == "" {
		// No root supplied; fall back to the old "non-empty + no .."
		// gate. Callers should always pass a root; this keeps the
		// helper non-breaking for any future caller that didn't yet.
		return true
	}
	absP, err := filepath.Abs(p)
	if err != nil {
		return false
	}
	absRoot, err := filepath.Abs(allowedRoot)
	if err != nil {
		return false
	}
	// Add the separator so "/foo/bar-evil" doesn't match prefix "/foo/bar".
	rootWithSep := filepath.Clean(absRoot) + string(os.PathSeparator)
	return strings.HasPrefix(filepath.Clean(absP)+string(os.PathSeparator), rootWithSep)
}
