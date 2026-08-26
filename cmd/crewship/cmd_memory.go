package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/memory"
	"github.com/spf13/cobra"
)

var memoryCmd = &cobra.Command{
	Use:   "memory",
	Short: "Inspect and search agent/crew/workspace memory (local filesystem)",
	Long: `Directly access memory FTS5 indexes on the local filesystem.
Useful for development and debugging — does not require a running server.

Scopes (--path meaning depends on scope):
  agent      --path = agent dir or .memory dir (e.g. /crew/agents/lead/.memory)
  crew       --path = crew root dir (resolves to <path>/shared/.memory/)
  workspace  --path = workspace memory dir (e.g. ~/.crewship/memory/<workspace-id>)
  all        --path = crew root dir (searches agent + crew)`,
}

var memorySearchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "FTS5 search across memory",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		query := args[0]
		limit, _ := cmd.Flags().GetInt("limit")
		scope, _ := cmd.Flags().GetString("scope")
		basePath, _ := cmd.Flags().GetString("path")

		if basePath == "" {
			return fmt.Errorf("--path is required (e.g. /path/to/crew/agents/lead/.memory)")
		}

		paths, err := resolveMemoryPaths(basePath, scope)
		if err != nil {
			return err
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		allResults := []scopedResult{}

		for _, mp := range paths {
			eng, err := memory.New(mp.path, memory.DefaultConfig())
			if err != nil {
				if flagVerbose {
					fmt.Fprintf(os.Stderr, "skip %s (%s): %v\n", mp.scope, mp.path, err)
				}
				continue
			}

			results, err := eng.Search(ctx, query, limit)
			eng.Close()
			if err != nil {
				if flagVerbose {
					fmt.Fprintf(os.Stderr, "search %s failed: %v\n", mp.scope, err)
				}
				continue
			}

			for _, r := range results {
				allResults = append(allResults, scopedResult{Source: mp.scope, Result: r})
			}
		}

		// "No results found." used to be printed before the format was
		// consulted at all, so a `--format json` search that matched nothing
		// answered a sentence — and a search matching nothing is the case a
		// caller most needs to handle.
		return resolvedFormatter(cmd).AutoHuman(allResults, func() {
			if len(allResults) == 0 {
				fmt.Println("No results found.")
				return
			}
			for i, sr := range allResults {
				fmt.Printf("[%d] [%s] %s (score: %.4f)\n", i+1, sr.Source, sr.Result.File, sr.Result.Score)
				fmt.Printf("    %s\n\n", sr.Result.Snippet)
			}
		})
	},
}

// scopedResult is one memory search hit, tagged with the scope it came from.
type scopedResult struct {
	Source string              `json:"source" yaml:"source"`
	Result memory.SearchResult `json:"result" yaml:"result"`
}

var memoryStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show memory index status",
	RunE: func(cmd *cobra.Command, args []string) error {
		scope, _ := cmd.Flags().GetString("scope")
		basePath, _ := cmd.Flags().GetString("path")

		if basePath == "" {
			return fmt.Errorf("--path is required")
		}

		paths, err := resolveMemoryPaths(basePath, scope)
		if err != nil {
			return err
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// A scope that will not open is a RESULT, not a failure of the
		// command — `--scope all` legitimately reaches paths that were never
		// initialised. It used to be printed as prose and exit 0, so a caller
		// asking for JSON got a mix of a driver error and, sometimes,
		// nothing else. Each scope now carries its own error field.
		//
		// #2086 adds the other half: the text in that field has to name a
		// cause the operator can act on, and when NO scope could be read the
		// command must not still exit 0 — `crewship memory status … ||
		// handle_it` was dead code in every script that had it. The row shape
		// is unchanged, so `--format json` keeps carrying the error per scope.
		scopes := []memoryScopeStatus{}
		var failed int
		for _, mp := range paths {
			row := memoryScopeStatus{Scope: mp.scope, Path: mp.path}

			// Ask the filesystem before SQLite does: the driver collapses a
			// missing path, a file where a directory belongs, and a directory
			// it cannot enter into one "unable to open database file (14)",
			// which names neither the cause nor the path it tried.
			if err := memoryDirError(mp.path); err != nil {
				row.Error = err.Error()
				scopes = append(scopes, row)
				failed++
				continue
			}

			eng, err := memory.New(mp.path, memory.DefaultConfig())
			if err != nil {
				row.Error = fmt.Sprintf("cannot open the memory index in %s: %v", mp.path, err)
				scopes = append(scopes, row)
				failed++
				continue
			}

			status, err := eng.Status(ctx)
			eng.Close()
			if err != nil {
				row.Error = fmt.Sprintf("cannot read the memory index in %s: %v", mp.path, err)
				scopes = append(scopes, row)
				failed++
				continue
			}

			row.Initialized = true
			row.TotalFiles = status.TotalFiles
			row.TotalChunks = status.TotalChunks
			row.TotalSizeKB = status.TotalSizeKB
			row.IndexedAt = status.IndexedAt.Format(time.RFC3339)
			row.SearchReady = status.SearchReady
			scopes = append(scopes, row)
		}

		if err := resolvedFormatter(cmd).AutoHuman(scopes, func() {
			for _, s := range scopes {
				// A per-scope failure is a diagnostic, so in human output it
				// goes to stderr and stdout stays clean for the scopes that
				// did report. Structured formats keep it in the row, where a
				// caller can read it per scope.
				if s.Error != "" {
					fmt.Fprintf(os.Stderr, "[%s] %s\n", s.Scope, s.Error)
					continue
				}
				fmt.Printf("[%s] %s\n", s.Scope, s.Path)
				fmt.Printf("  Files:   %d\n", s.TotalFiles)
				fmt.Printf("  Chunks:  %d\n", s.TotalChunks)
				fmt.Printf("  Size:    %d KB\n", s.TotalSizeKB)
				fmt.Printf("  Indexed: %s\n", s.IndexedAt)
				fmt.Printf("  Ready:   %v\n\n", s.SearchReady)
			}
		}); err != nil {
			return err
		}

		// Every scope failed: nothing was reported, so the command did not
		// do what it was asked and must not claim success. A partial failure
		// stays exit 0 — the scopes that answered, answered — with the
		// failures already rendered above.
		if failed == len(paths) {
			return cli.WithExitCode(
				fmt.Errorf("no readable memory index for scope %q under %s", scope, basePath),
				cli.ExitNotFound)
		}
		return nil
	},
}

// memoryScopeStatus is one scope's index status in `memory status`.
type memoryScopeStatus struct {
	Scope       string `json:"scope" yaml:"scope"`
	Path        string `json:"path" yaml:"path"`
	Initialized bool   `json:"initialized" yaml:"initialized"`
	TotalFiles  int    `json:"total_files" yaml:"total_files"`
	TotalChunks int    `json:"total_chunks" yaml:"total_chunks"`
	TotalSizeKB int64  `json:"total_size_kb" yaml:"total_size_kb"`
	IndexedAt   string `json:"indexed_at,omitempty" yaml:"indexed_at,omitempty"`
	SearchReady bool   `json:"search_ready" yaml:"search_ready"`
	Error       string `json:"error,omitempty" yaml:"error,omitempty"`
}

var memoryReindexCmd = &cobra.Command{
	Use:   "reindex",
	Short: "Rebuild FTS5 index from markdown files",
	RunE: func(cmd *cobra.Command, args []string) error {
		scope, _ := cmd.Flags().GetString("scope")
		basePath, _ := cmd.Flags().GetString("path")

		if basePath == "" {
			return fmt.Errorf("--path is required")
		}

		paths, err := resolveMemoryPaths(basePath, scope)
		if err != nil {
			return err
		}

		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()

		var succeeded int
		for _, mp := range paths {
			// Same driver-error leak as `status` had, same fix (#2086) —
			// reindex already exits non-zero when every scope fails.
			if err := memoryDirError(mp.path); err != nil {
				fmt.Fprintf(os.Stderr, "[%s] %v\n", mp.scope, err)
				continue
			}

			eng, err := memory.New(mp.path, memory.DefaultConfig())
			if err != nil {
				fmt.Fprintf(os.Stderr, "[%s] cannot open the memory index in %s: %v\n", mp.scope, mp.path, err)
				continue
			}

			start := time.Now()
			if err := eng.ReindexContext(ctx); err != nil {
				eng.Close()
				fmt.Printf("[%s] reindex failed: %v\n", mp.scope, err)
				continue
			}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			status, err := eng.Status(ctx)
			cancel()
			eng.Close()

			elapsed := time.Since(start)
			if err != nil || status == nil {
				fmt.Printf("[%s] reindexed in %s (status unavailable)\n", mp.scope, elapsed.Round(time.Millisecond))
			} else {
				fmt.Printf("[%s] reindexed %d files (%d chunks) in %s\n",
					mp.scope, status.TotalFiles, status.TotalChunks, elapsed.Round(time.Millisecond))
			}
			succeeded++
		}
		if succeeded == 0 {
			return fmt.Errorf("all reindex operations failed")
		}
		return nil
	},
}

func init() {
	// Shared flags for all memory subcommands.
	for _, cmd := range []*cobra.Command{memorySearchCmd, memoryStatusCmd, memoryReindexCmd} {
		cmd.Flags().StringP("scope", "S", "agent", "Memory scope: agent, crew, workspace, all")
		cmd.Flags().StringP("path", "p", "", "Base path to crew directory (e.g. /path/to/crews/{crew-id})")
	}

	memorySearchCmd.Flags().IntP("limit", "l", 10, "Max results per scope")

	// This command owned a LOCAL `--format/-F` flag (table|json). Because it
	// took the NAME "format", it shadowed the root's persistent flag on this
	// command — and a shadowed persistent flag takes its shorthand with it, so
	// `crewship memory search … -f json` did not fall back to human output, it
	// FAILED with `unknown shorthand flag: 'f'` on the one flag the CLI
	// advertises everywhere (#2086). The help was wrong to match: it printed
	// "Output format: table, json" where every other command prints the five.
	//
	// The alias survives under its own name so `-F json` keeps working, and is
	// marked deprecated so it stops spreading. Nothing reads it directly —
	// resolvedFormat folds it into the global resolution below.
	memorySearchCmd.Flags().StringP("output-format", "F", "", "Deprecated alias for --format/-f")
	if err := memorySearchCmd.Flags().MarkDeprecated("output-format", "use --format/-f (which now supports table|json|yaml|ndjson|quiet)"); err != nil {
		panic(err) // programmer error: the flag was just registered
	}

	memoryCmd.AddCommand(memorySearchCmd)
	memoryCmd.AddCommand(memoryStatusCmd)
	memoryCmd.AddCommand(memoryReindexCmd)
}

// memoryPath pairs a scope label with a filesystem path.
type memoryPath struct {
	scope string
	path  string
}

// resolveMemoryPaths converts a base crew path + scope into concrete filesystem paths.
// basePath should be the crew root (e.g. ~/.crewship/crews/{crew-id}/ or /crew/ inside container).
// For workspace scope, it resolves from ~/.crewship/memory/{workspace}/.
func resolveMemoryPaths(basePath, scope string) ([]memoryPath, error) {
	var paths []memoryPath

	switch scope {
	case "agent":
		// Expect --path to point directly at the .memory dir or the agent dir
		p := ensureMemorySubdir(basePath)
		paths = append(paths, memoryPath{scope: "agent", path: p})

	case "crew":
		// Crew shared memory at <basePath>/shared/.memory/
		p := filepath.Join(basePath, "shared", ".memory")
		paths = append(paths, memoryPath{scope: "crew", path: p})

	case "workspace":
		// Workspace memory — basePath should point to workspace memory dir
		paths = append(paths, memoryPath{scope: "workspace", path: basePath})

	case "all":
		// Agent: assume basePath has agents/<slug>/.memory/ — user must be specific
		agentP := ensureMemorySubdir(basePath)
		paths = append(paths, memoryPath{scope: "agent", path: agentP})
		// Crew
		crewP := filepath.Join(basePath, "shared", ".memory")
		if dirExists(crewP) {
			paths = append(paths, memoryPath{scope: "crew", path: crewP})
		}

	default:
		return nil, fmt.Errorf("unknown scope %q — use agent, crew, workspace, or all", scope)
	}

	return paths, nil
}

// ensureMemorySubdir appends .memory if the path doesn't already end with it.
// Always resolves to the .memory subdirectory to prevent creating index.sqlite
// in the wrong location.
func ensureMemorySubdir(p string) string {
	if filepath.Base(p) == ".memory" {
		return p
	}
	return filepath.Join(p, ".memory")
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// memoryDirError reports, in words an operator can act on, why p cannot hold a
// memory index — or nil when it looks usable.
//
// The check exists because SQLite collapses every one of these causes into the
// single opaque SQLITE_CANTOPEN, which the driver renders as "unable to open
// database file (14)": that string is identical for a path that does not
// exist, a file where a directory belongs, and a directory the caller cannot
// enter, and it never names the path it tried (#2086). The path itself is
// derived from --path *and* --scope, so the user cannot reconstruct it from
// the flags they typed.
func memoryDirError(p string) error {
	info, err := os.Stat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%s does not exist — check --path and --scope (`crewship memory --help` lists what --path means per scope), then build an index with `crewship memory reindex`", p)
	case errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("%s cannot be read: permission denied — memory dirs inside an agent container are owned by uid 1001", p)
	case err != nil:
		return fmt.Errorf("%s cannot be read: %v", p, err)
	case !info.IsDir():
		return fmt.Errorf("%s is not a directory — --path names the directory holding the index, not a file inside it", p)
	}
	return nil
}
