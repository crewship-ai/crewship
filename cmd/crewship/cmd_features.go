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

// baseImageEntry mirrors the UI's BASE_IMAGES catalog in
// components/features/crews/runtime-config.tsx. Keep these two lists in sync.
type baseImageEntry struct {
	Image       string `json:"image"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Recommended bool   `json:"recommended,omitempty"`
}

var baseImagesCatalog = []baseImageEntry{
	{
		Image:       "mcr.microsoft.com/devcontainers/javascript-node:22-bookworm",
		Label:       "Node 22 (Debian)",
		Description: "Node.js 22 + npm + git + curl. Best for Claude Code and most AI workloads.",
		Recommended: true,
	},
	{
		Image:       "mcr.microsoft.com/devcontainers/base:bookworm",
		Label:       "Debian 12 (bookworm)",
		Description: "Minimal Debian with common utilities. Add features/runtimes as needed.",
	},
	{
		Image:       "mcr.microsoft.com/devcontainers/base:ubuntu-24.04",
		Label:       "Ubuntu 24.04",
		Description: "Ubuntu LTS with common utilities.",
	},
	{
		Image:       "mcr.microsoft.com/devcontainers/python:3.12-bookworm",
		Label:       "Python 3.12 (Debian)",
		Description: "Python 3.12 + pip + venv pre-installed on Debian.",
	},
	{
		Image:       "mcr.microsoft.com/devcontainers/go:1.23-bookworm",
		Label:       "Go 1.23 (Debian)",
		Description: "Go 1.23 toolchain on Debian.",
	},
	{
		Image:       "mcr.microsoft.com/devcontainers/rust:bookworm",
		Label:       "Rust (Debian)",
		Description: "Rust stable + cargo on Debian.",
	},
	{
		Image:       "mcr.microsoft.com/devcontainers/java:21-bookworm",
		Label:       "Java 21 (OpenJDK)",
		Description: "OpenJDK 21 + Maven/Gradle on Debian.",
	},
	{
		Image:       "mcr.microsoft.com/devcontainers/universal:2",
		Label:       "Universal (kitchen sink)",
		Description: "Node + Python + Go + Rust + Java + Ruby pre-installed. ~8GB.",
	},
	{
		Image:       "mcr.microsoft.com/devcontainers/base:alpine-3.20",
		Label:       "Alpine 3.20 (experimental)",
		Description: "Tiny (~7MB). WARNING: musl incompatible with Claude Code.",
	},
}

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
