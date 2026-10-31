//go:build !clionly

package main

// `crewship admin instance page-limit` — the maximum number of pages a
// workspace may hold (GET/PUT /api/v1/admin/instance/page-limit).

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

const pageLimitPath = "/api/v1/admin/instance/page-limit"

type adminPageLimit struct {
	MaxPagesPerWorkspace int `json:"max_pages_per_workspace" yaml:"max_pages_per_workspace"`
	Default              int `json:"default" yaml:"default"`
	Min                  int `json:"min" yaml:"min"`
	Max                  int `json:"max" yaml:"max"`
}

var adminInstancePageLimitCmd = &cobra.Command{
	Use:   "page-limit",
	Short: "Maximum number of pages per workspace",
	Long: `The maximum number of pages (Documents, Designs, artifact projects, pages
created by hand or by an agent) a workspace may hold, for instance
administrators. The default is 100; the allowed range is 10 to 10000.

Archived pages count. When a workspace is at the limit, creating another page
is refused with 422 pages_limit. Lowering the limit never deletes anything: it
only blocks new pages while a workspace is at or above it.

  crewship admin instance page-limit          show
  crewship admin instance page-limit set 250  set`,
	Args: cobra.NoArgs,
	RunE: showAdminPageLimit,
}

func showAdminPageLimit(cmd *cobra.Command, _ []string) error {
	client, err := requireAuthInstance()
	if err != nil {
		return err
	}
	var out adminPageLimit
	if err := getJSON(client, pageLimitPath, &out); err != nil {
		return err
	}
	return printAdminPageLimit(cmd, out)
}

func printAdminPageLimit(cmd *cobra.Command, out adminPageLimit) error {
	return resolvedFormatter(cmd).AutoHuman(out, func() {
		w := cmd.OutOrStdout()
		fmt.Fprintf(w, "Pages per workspace: %d (default %d, allowed %d-%d)\n", out.MaxPagesPerWorkspace, out.Default, out.Min, out.Max)
		fmt.Fprintln(w, "Lowering the limit never deletes pages; it only blocks new ones.")
	})
}

var adminInstancePageLimitGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Show the page limit",
	Args:  cobra.NoArgs,
	RunE:  showAdminPageLimit,
}

var adminInstancePageLimitSetCmd = &cobra.Command{
	Use:   "set <max-pages>",
	Short: "Set the maximum number of pages per workspace",
	Long: `PUT /api/v1/admin/instance/page-limit. A whole number from 10 to 10000.
No page is removed, also when the new limit is below what a workspace holds.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		n, err := strconv.Atoi(args[0])
		if err != nil {
			return cli.WithExitCode(fmt.Errorf("max-pages must be a whole number, got %q", args[0]), cli.ExitValidation)
		}
		client, err := requireAuthInstance()
		if err != nil {
			return err
		}
		var out adminPageLimit
		if err := putJSON(client, pageLimitPath, map[string]any{"max_pages_per_workspace": n}, &out); err != nil {
			return err
		}
		return printAdminPageLimit(cmd, out)
	},
}

func init() {
	adminInstancePageLimitCmd.AddCommand(adminInstancePageLimitGetCmd, adminInstancePageLimitSetCmd)
	adminInstanceCmd.AddCommand(adminInstancePageLimitCmd)
}
