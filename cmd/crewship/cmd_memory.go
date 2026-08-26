package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

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
	Source string              `json:"source"`
	Result memory.SearchResult `json:"result"`
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
		scopes := []memoryScopeStatus{}
		for _, mp := range paths {
			row := memoryScopeStatus{Scope: mp.scope, Path: mp.path}
			eng, err := memory.New(mp.path, memory.DefaultConfig())
			if err != nil {
				row.Error = "not initialized: " + err.Error()
				scopes = append(scopes, row)
				continue
			}

			status, err := eng.Status(ctx)
			eng.Close()
			if err != nil {
				row.Error = err.Error()
				scopes = append(scopes, row)
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

		return resolvedFormatter(cmd).AutoHuman(scopes, func() {
			for _, s := range scopes {
				if s.Error != "" {
					fmt.Printf("[%s] %s — %s\n", s.Scope, s.Path, s.Error)
					continue
				}
				fmt.Printf("[%s] %s\n", s.Scope, s.Path)
				fmt.Printf("  Files:   %d\n", s.TotalFiles)
				fmt.Printf("  Chunks:  %d\n", s.TotalChunks)
				fmt.Printf("  Size:    %d KB\n", s.TotalSizeKB)
				fmt.Printf("  Indexed: %s\n", s.IndexedAt)
				fmt.Printf("  Ready:   %v\n\n", s.SearchReady)
			}
		})
	},
}

// memoryScopeStatus is one scope's index status in `memory status`.
type memoryScopeStatus struct {
	Scope       string `json:"scope"`
	Path        string `json:"path"`
	Initialized bool   `json:"initialized"`
	TotalFiles  int    `json:"total_files"`
	TotalChunks int    `json:"total_chunks"`
	TotalSizeKB int64  `json:"total_size_kb"`
	IndexedAt   string `json:"indexed_at,omitempty"`
	SearchReady bool   `json:"search_ready"`
	Error       string `json:"error,omitempty"`
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
			eng, err := memory.New(mp.path, memory.DefaultConfig())
			if err != nil {
				fmt.Printf("[%s] %s — cannot open: %v\n", mp.scope, mp.path, err)
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
