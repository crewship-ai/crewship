//go:build !clionly

package main

import (
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

type installationPathRow struct {
	Resource string `json:"resource" yaml:"resource"`
	Path     string `json:"path" yaml:"path"`
	Source   string `json:"source" yaml:"source"`
	Enabled  *bool  `json:"enabled,omitempty" yaml:"enabled,omitempty"`
}

type installationPathsReport struct {
	Paths              []installationPathRow `json:"paths" yaml:"paths"`
	StartupBlocked     bool                  `json:"startup_blocked" yaml:"startup_blocked"`
	LegacyConflict     string                `json:"legacy_conflict,omitempty" yaml:"legacy_conflict,omitempty"`
	ConfigurationError string                `json:"configuration_error,omitempty" yaml:"configuration_error,omitempty"`
}

var pathsCmd = newPathsCommand()

func newPathsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "paths",
		Short: "Inspect local installation paths and configuration sources without creating files",
		Args:  cobra.NoArgs,
		// This is a local server diagnostic: bypass root's client-profile lookup.
		PersistentPreRun: func(cmd *cobra.Command, args []string) {},
		RunE:             runPaths,
	}
	cmd.Flags().String("data-dir", "", "Absolute installation data directory (same as start)")
	cmd.Flags().String("config", "", "Path to server configuration file (YAML)")
	cmd.Flags().String("db", "", "Database URL override (same as start)")
	return cmd
}

func runPaths(cmd *cobra.Command, args []string) error {
	outputFormat := cli.ResolveFormat(flagFormat, nil)
	switch outputFormat {
	case "table", "json", "yaml", "ndjson", "quiet":
	default:
		return fmt.Errorf("paths supports --format table|json|yaml|ndjson|quiet (got %q)", outputFormat)
	}
	rootFlag, _ := cmd.Flags().GetString("data-dir")
	configFile, _ := cmd.Flags().GetString("config")
	databaseFlag, _ := cmd.Flags().GetString("db")
	root, err := database.ResolveDataDirRoot(rootFlag)
	if err != nil {
		return err
	}
	cfg, err := config.Load(configFile)
	if err != nil {
		return fmt.Errorf("load server configuration: %w", err)
	}
	resolved, err := config.ResolvePaths(cfg, root, databaseFlag)
	if err != nil {
		return err
	}
	report := installationPathsReport{Paths: []installationPathRow{{Resource: "installation.root", Path: root, Source: installationRootSource(rootFlag)}}}
	keys := make([]string, 0, len(resolved.Paths))
	for key := range resolved.Paths {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		path := resolved.Paths[key]
		if key == "database" {
			path = redactPathsDatabase(path)
		}
		report.Paths = append(report.Paths, installationPathRow{Resource: key, Path: path, Source: resolved.Sources[key]})
	}
	pageSource, err := pageProjectsPathSource(configFile)
	if err != nil {
		return err
	}
	pagesEnabled := cfg.Storage.PageProjectsPath != ""
	report.Paths = append(report.Paths, installationPathRow{Resource: "storage.page_projects_path", Path: cfg.Storage.PageProjectsPath, Source: pageSource, Enabled: &pagesEnabled})
	if err := cfg.Validate(); err != nil {
		report.ConfigurationError = err.Error()
		report.StartupBlocked = true
	}
	if err := resolved.CheckLegacyData(); err != nil {
		report.LegacyConflict = err.Error()
		report.StartupBlocked = true
	}
	// Resolve format without opening the client configuration or selecting a
	// server profile. The inherited --format/-f flag remains authoritative.
	formatter := &cli.Formatter{Format: outputFormat, Writer: cmd.OutOrStdout()}
	if formatter.Format == "quiet" {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), root)
		return err
	}
	return formatter.AutoHuman(report, func() {
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "RESOURCE\tPATH\tSOURCE")
		for _, row := range report.Paths {
			path := row.Path
			if row.Enabled != nil && !*row.Enabled {
				path = "(disabled)"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\n", row.Resource, path, row.Source)
		}
		_ = w.Flush()
		if report.ConfigurationError != "" {
			fmt.Fprintln(cmd.OutOrStdout(), "Startup blocked:", report.ConfigurationError)
		}
		if report.LegacyConflict != "" {
			fmt.Fprintln(cmd.OutOrStdout(), "Startup blocked:", report.LegacyConflict)
		}
	})
}

func installationRootSource(flagRoot string) string {
	if strings.TrimSpace(flagRoot) != "" {
		return "flag"
	}
	if strings.TrimSpace(os.Getenv("CREWSHIP_HOME")) != "" {
		return "env:CREWSHIP_HOME"
	}
	if strings.TrimSpace(os.Getenv("CREWSHIP_DATA_DIR")) != "" {
		return "env:CREWSHIP_DATA_DIR"
	}
	return "default"
}

func pageProjectsPathSource(configFile string) (string, error) {
	if os.Getenv("CREWSHIP_PAGE_PROJECTS_PATH") != "" {
		return "env", nil
	}
	if configFile != "" {
		data, err := os.ReadFile(configFile)
		if err != nil {
			return "", err
		}
		var values map[string]any
		if err := yaml.Unmarshal(data, &values); err != nil {
			return "", err
		}
		if storage, ok := values["storage"].(map[string]any); ok {
			if value, exists := storage["page_projects_path"]; exists && value != nil {
				return "yaml", nil
			}
		}
	}
	return "default", nil
}

func redactPathsDatabase(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil && strings.Contains(raw, "://") {
		// A malformed URL can still contain credentials. Do not echo it just
		// because the parser could not identify its userinfo safely.
		return "(invalid database URL; redacted)"
	}
	if err == nil && parsed.User != nil {
		return parsed.Redacted()
	}
	return raw
}
