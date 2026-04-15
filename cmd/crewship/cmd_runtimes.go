package main

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

var runtimesCmd = &cobra.Command{
	Use:   "runtimes",
	Short: "Browse the mise-managed runtime/tool catalog",
}

type runtimeEntry struct {
	Name           string   `json:"name"`
	Tool           string   `json:"tool"`
	Description    string   `json:"description"`
	Category       string   `json:"category"`
	Icon           string   `json:"icon"`
	Versions       []string `json:"versions"`
	DefaultVersion string   `json:"default_version"`
	Backends       []string `json:"backends"`
}

// fetchRuntimeCatalog loads the runtime catalog from the API, optionally
// forwarding a search query to the backend. Category filtering is applied
// client-side because the backend search only matches name/tool/desc/category
// substrings.
func fetchRuntimeCatalog(search string) ([]runtimeEntry, error) {
	client := newAPIClient()
	path := "/api/v1/runtimes/catalog"
	if search != "" {
		path += "?search=" + url.QueryEscape(search)
	}

	resp, err := client.Get(path)
	if err != nil {
		return nil, err
	}
	if err := cli.CheckError(resp); err != nil {
		return nil, err
	}

	var result struct {
		Runtimes []runtimeEntry `json:"runtimes"`
	}
	if err := cli.ReadJSON(resp, &result); err != nil {
		return nil, err
	}
	return result.Runtimes, nil
}

var runtimesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available runtimes/tools from the mise catalog",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}

		search, _ := cmd.Flags().GetString("search")
		category, _ := cmd.Flags().GetString("category")

		runtimes, err := fetchRuntimeCatalog(search)
		if err != nil {
			return err
		}

		// Client-side category filter.
		if category != "" {
			filtered := runtimes[:0]
			for _, r := range runtimes {
				if strings.EqualFold(r.Category, category) {
					filtered = append(filtered, r)
				}
			}
			runtimes = filtered
		}

		f := newFormatter()
		headers := []string{"TOOL", "NAME", "CATEGORY", "VERSIONS", "DEFAULT"}
		var rows [][]string
		for _, r := range runtimes {
			versions := strings.Join(r.Versions, ", ")
			if versions == "" {
				versions = "—"
			}
			def := r.DefaultVersion
			if def == "" {
				def = "—"
			}
			rows = append(rows, []string{r.Tool, r.Name, r.Category, versions, def})
		}
		return f.Auto(runtimes, headers, rows)
	},
}

var runtimesInfoCmd = &cobra.Command{
	Use:   "info <tool>",
	Short: "Show details for a specific runtime/tool",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}

		runtimes, err := fetchRuntimeCatalog("")
		if err != nil {
			return err
		}

		tool := args[0]
		for _, r := range runtimes {
			if strings.EqualFold(r.Tool, tool) {
				f := newFormatter()
				versions := strings.Join(r.Versions, ", ")
				if versions == "" {
					versions = "—"
				}
				def := r.DefaultVersion
				if def == "" {
					def = "—"
				}
				backends := strings.Join(r.Backends, ", ")
				if backends == "" {
					backends = "—"
				}
				desc := r.Description
				if desc == "" {
					desc = "—"
				}
				pairs := [][]string{
					{"Tool", r.Tool},
					{"Name", r.Name},
					{"Category", r.Category},
					{"Description", desc},
					{"Icon", r.Icon},
					{"Versions", versions},
					{"Default", def},
					{"Backends", backends},
				}
				return f.AutoDetail(r, pairs)
			}
		}

		return fmt.Errorf("runtime not found: %s", tool)
	},
}

func init() {
	runtimesListCmd.Flags().String("search", "", "Filter runtimes by name, tool, description, or category")
	runtimesListCmd.Flags().String("category", "", "Filter by category: languages, tools, cloud, databases")

	runtimesCmd.AddCommand(runtimesListCmd)
	runtimesCmd.AddCommand(runtimesInfoCmd)
}
