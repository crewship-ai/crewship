package main

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

var featuresCmd = &cobra.Command{
	Use:   "features",
	Short: "Browse devcontainer features catalog",
}

var featuresListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available devcontainer features",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}

		client := newAPIClient()
		path := "/api/v1/features/catalog"
		if search, _ := cmd.Flags().GetString("search"); search != "" {
			path += "?search=" + url.QueryEscape(search)
		}

		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var result struct {
			Features []struct {
				Ref         string `json:"ref"`
				Name        string `json:"name"`
				Description string `json:"description"`
				Category    string `json:"category"`
				SizeHint    string `json:"size_hint"`
			} `json:"features"`
		}
		if err := cli.ReadJSON(resp, &result); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"NAME", "CATEGORY", "SIZE", "REF"}
		var rows [][]string
		for _, feat := range result.Features {
			rows = append(rows, []string{
				feat.Name, feat.Category, feat.SizeHint, feat.Ref,
			})
		}
		return f.Auto(result.Features, headers, rows)
	},
}

var featuresInfoCmd = &cobra.Command{
	Use:   "info <ref>",
	Short: "Show details for a specific devcontainer feature",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Get("/api/v1/features/catalog")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var result struct {
			Features []struct {
				Ref         string `json:"ref"`
				Name        string `json:"name"`
				Description string `json:"description"`
				Category    string `json:"category"`
				Icon        string `json:"icon"`
				SizeHint    string `json:"size_hint"`
			} `json:"features"`
		}
		if err := cli.ReadJSON(resp, &result); err != nil {
			return err
		}

		ref := args[0]
		for _, feat := range result.Features {
			if feat.Ref == ref {
				f := newFormatter()
				pairs := [][]string{
					{"Name", feat.Name},
					{"Ref", feat.Ref},
					{"Category", feat.Category},
					{"Description", feat.Description},
					{"Icon", feat.Icon},
					{"Size Hint", feat.SizeHint},
				}
				return f.AutoDetail(feat, pairs)
			}
		}

		return fmt.Errorf("feature not found: %s", ref)
	},
}

func init() {
	featuresListCmd.Flags().String("search", "", "Filter features by name, description, or category")

	featuresCmd.AddCommand(featuresListCmd)
	featuresCmd.AddCommand(featuresInfoCmd)
}
