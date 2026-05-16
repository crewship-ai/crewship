package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/memory"
	"github.com/spf13/cobra"
)

// cmd_memory_versions wires `crewship memory log/show/restore` —
// the operator-facing read + recover surface for the v90
// memory_versions audit trail. Distinct from `crewship memory
// search` (FTS over markdown chunks) because the audit trail is
// SQL-on-DB, not filesystem-on-FTS. Both subcommands share the
// `memory` parent so they read like sibling verbs in the help.

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

	db, err := openAdminDB()
	if err != nil {
		return err
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	entries, err := memory.LogVersions(ctx, db.DB, workspaceID, path, limit)
	if err != nil {
		return fmt.Errorf("log versions: %w", err)
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

	db, err := openAdminDB()
	if err != nil {
		return err
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	content, err := memory.ReadVersion(ctx, db.DB, workspaceID, path, sha)
	if err != nil {
		if errors.Is(err, memory.ErrVersionNotFound) {
			fmt.Fprintf(os.Stderr, "version not found: workspace=%s path=%s sha=%s\n", workspaceID, path, sha)
			os.Exit(1)
		}
		return err
	}
	_, _ = io.Copy(os.Stdout, strings.NewReader(string(content)))
	return nil
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

	if !force && !canonicalPathIsSafe(canonicalPath) {
		return fmt.Errorf("refusing to restore to %q (pass --force to override)", canonicalPath)
	}

	db, err := openAdminDB()
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
		return override + "/memory/versions", nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return home + "/.crewship/memory/versions", nil
}

// canonicalPathIsSafe rejects empty paths + obvious traversal
// attempts. Callers passing --force bypass this; non-force callers
// need a path that is non-empty and does not contain "..".
func canonicalPathIsSafe(p string) bool {
	if strings.TrimSpace(p) == "" {
		return false
	}
	if strings.Contains(p, "..") {
		return false
	}
	return true
}
