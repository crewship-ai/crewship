package main

import (
	"net/url"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/devcontainer"
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
				Ref         string `json:"ref" yaml:"ref"`
				Name        string `json:"name" yaml:"name"`
				Description string `json:"description" yaml:"description"`
				Category    string `json:"category" yaml:"category"`
				SizeHint    string `json:"size_hint" yaml:"size_hint"`
				Publisher   string `json:"publisher" yaml:"publisher"`
				Tier        string `json:"tier" yaml:"tier"`
			} `json:"features" yaml:"features"`
		}
		if err := cli.ReadJSON(resp, &result); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"NAME", "CATEGORY", "SOURCE", "SIZE", "REF"}
		var rows [][]string
		for _, feat := range result.Features {
			source := feat.Publisher
			if feat.Tier != "" {
				source = feat.Publisher + " · " + feat.Tier
			}
			rows = append(rows, []string{
				feat.Name, feat.Category, source, feat.SizeHint, feat.Ref,
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
				Ref         string `json:"ref" yaml:"ref"`
				Name        string `json:"name" yaml:"name"`
				Description string `json:"description" yaml:"description"`
				Category    string `json:"category" yaml:"category"`
				Icon        string `json:"icon" yaml:"icon"`
				SizeHint    string `json:"size_hint" yaml:"size_hint"`
				Publisher   string `json:"publisher" yaml:"publisher"`
				Tier        string `json:"tier" yaml:"tier"`
			} `json:"features" yaml:"features"`
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
					{"Publisher", feat.Publisher},
					{"Tier", feat.Tier},
					{"Description", feat.Description},
					{"Icon", feat.Icon},
					{"Size Hint", feat.SizeHint},
				}
				return f.AutoDetail(feat, pairs)
			}
		}

		return cli.NotFoundf("feature not found: %s", ref)
	},
}

var baseImagesCatalog = devcontainer.BuiltinBaseImages()

var featuresBaseImagesCmd = &cobra.Command{
	Use:   "base-images",
	Short: "List recommended base container images for crews",
	RunE: func(cmd *cobra.Command, args []string) error {
		f := newFormatter()
		headers := []string{"IMAGE", "LABEL", "DESCRIPTION"}
		rows := make([][]string, 0, len(baseImagesCatalog))
		for _, b := range baseImagesCatalog {
			label := b.Label
			if b.Recommended {
				label += " [RECOMMENDED]"
			}
			rows = append(rows, []string{b.Image, label, b.Description})
		}
		return f.Auto(baseImagesCatalog, headers, rows)
	},
}

func init() {
	featuresListCmd.Flags().String("search", "", "Filter features by name, description, or category")

	featuresCmd.AddCommand(featuresListCmd)
	featuresCmd.AddCommand(featuresInfoCmd)
	featuresCmd.AddCommand(featuresBaseImagesCmd)
}
