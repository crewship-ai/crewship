package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/crewship-ai/crewship/internal/memory"
	"github.com/spf13/cobra"
)

var memoryCmd = &cobra.Command{
	Use:   "memory",
	Short: "Inspect and search agent/crew/workspace memory (local filesystem)",
	Long: `Directly access memory FTS5 indexes on the local filesystem.
Useful for development and debugging — does not require a running server.

Scopes:
  agent      Per-agent memory at <base>/.memory/
  crew       Crew shared memory at <base>/shared/.memory/
  workspace  Workspace memory at ~/.crewship/memory/<workspace>/`,
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

		type scopedResult struct {
			Source string              `json:"source"`
			Result memory.SearchResult `json:"result"`
		}

		var allResults []scopedResult

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

		if len(allResults) == 0 {
			fmt.Println("No results found.")
			return nil
		}

		// JSON output for tooling, table for humans.
		format, _ := cmd.Flags().GetString("format")
		if format == "json" {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(allResults)
		}

		for i, sr := range allResults {
			fmt.Printf("[%d] [%s] %s (score: %.4f)\n", i+1, sr.Source, sr.Result.File, sr.Result.Score)
			fmt.Printf("    %s\n\n", sr.Result.Snippet)
		}
		return nil
	},
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

		for _, mp := range paths {
			eng, err := memory.New(mp.path, memory.DefaultConfig())
			if err != nil {
				fmt.Printf("[%s] %s — not initialized: %v\n", mp.scope, mp.path, err)
				continue
			}

			status, err := eng.Status(ctx)
			eng.Close()
			if err != nil {
				fmt.Printf("[%s] %s — error: %v\n", mp.scope, mp.path, err)
				continue
			}

			fmt.Printf("[%s] %s\n", mp.scope, mp.path)
			fmt.Printf("  Files:   %d\n", status.TotalFiles)
			fmt.Printf("  Chunks:  %d\n", status.TotalChunks)
			fmt.Printf("  Size:    %d KB\n", status.TotalSizeKB)
			fmt.Printf("  Indexed: %s\n", status.IndexedAt.Format(time.RFC3339))
			fmt.Printf("  Ready:   %v\n\n", status.SearchReady)
		}
		return nil
	},
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

		for _, mp := range paths {
			eng, err := memory.New(mp.path, memory.DefaultConfig())
			if err != nil {
				fmt.Printf("[%s] %s — cannot open: %v\n", mp.scope, mp.path, err)
				continue
			}

			start := time.Now()
			if err := eng.Reindex(); err != nil {
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
	memorySearchCmd.Flags().StringP("format", "F", "table", "Output format: table, json")

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
