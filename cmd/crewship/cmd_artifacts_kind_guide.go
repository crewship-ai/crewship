package main

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

func init() { rootCmd.AddCommand(newArtifactsCommand()) }

// newArtifactsCommand groups reads about artifact kinds. Instance commands stay
// under `crewship page artifact` and `crewship page design`.
func newArtifactsCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "artifacts", Short: "Read what agents are told about each artifact kind"}
	cmd.AddCommand(newArtifactsKindGuideCommand())
	return cmd
}

type KindGuideSummary struct {
	Kind         string   `json:"kind" yaml:"kind"`
	Title        string   `json:"title" yaml:"title"`
	Version      string   `json:"version" yaml:"version"`
	RulesVersion string   `json:"rules_version" yaml:"rules_version"`
	SHA256       string   `json:"sha256" yaml:"sha256"`
	Bytes        int      `json:"bytes" yaml:"bytes"`
	Modes        []string `json:"modes" yaml:"modes"`
}

type kindGuideCapabilities struct {
	Supported   []string `json:"supported" yaml:"supported"`
	Unsupported []string `json:"unsupported" yaml:"unsupported"`
}

type kindGuide struct {
	KindGuideSummary `json:",inline" yaml:",inline"`
	Capabilities     kindGuideCapabilities `json:"capabilities" yaml:"capabilities"`
	Content          string                `json:"content" yaml:"content"`
}

func newArtifactsKindGuideCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "kind-guide [kind]",
		Short: "Show the versioned agent guide for an artifact kind, or list the guides",
		Long: `Without a kind, list every artifact kind guide with its version and digest.
With a kind (docs, slides, design, library, dashboard, app; aliases such as
design-system and project are accepted), print that guide: the data shapes and
limits, write order, version check, conflict rule, agent modes and
verification rule agents follow for that kind. Agents receive the same text.`,
		Example: `  crewship artifacts kind-guide
  crewship artifacts kind-guide docs
  crewship artifacts kind-guide design-system --format json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireAuth(); err != nil {
				return err
			}
			client := newAPIClient()
			formatter := resolvedFormatter(cmd)
			formatter.Writer = cmd.OutOrStdout()
			if len(args) == 0 {
				resp, err := client.Get("/api/v1/artifact-kind-guides")
				if err != nil {
					return err
				}
				if err := cli.CheckError(resp); err != nil {
					return err
				}
				var body struct {
					Guides []KindGuideSummary `json:"guides" yaml:"guides"`
				}
				if err := cli.ReadJSON(resp, &body); err != nil {
					return err
				}
				rows := make([][]string, 0, len(body.Guides))
				for _, g := range body.Guides {
					rows = append(rows, []string{g.Kind, g.Title, g.Version, g.RulesVersion, strings.Join(g.Modes, ","), g.SHA256[:min(12, len(g.SHA256))]})
				}
				return formatter.Auto(body, []string{"KIND", "TITLE", "VERSION", "RULES", "MODES", "SHA256"}, rows)
			}
			kind := strings.TrimSpace(args[0])
			if kind == "" {
				return cli.WithExitCode(fmt.Errorf("kind must not be empty"), cli.ExitValidation)
			}
			resp, err := client.Get("/api/v1/artifact-kind-guides/" + url.PathEscape(kind))
			if err != nil {
				return err
			}
			if err := cli.CheckError(resp); err != nil {
				return err
			}
			var g kindGuide
			if err := cli.ReadJSON(resp, &g); err != nil {
				return err
			}
			return formatter.AutoHuman(g, func() {
				fmt.Fprintf(formatter.Writer, "%s guide %s (rules %s, sha256 %s)\n", g.Title, g.Version, g.RulesVersion, g.SHA256)
				fmt.Fprintf(formatter.Writer, "Modes: %s\nSupported: %s\nUnsupported: %s\n\n", strings.Join(g.Modes, ", "), strings.Join(g.Capabilities.Supported, ", "), strings.Join(g.Capabilities.Unsupported, ", "))
				fmt.Fprint(formatter.Writer, g.Content)
			})
		},
	}
}
