package main

import (
	"fmt"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage CLI configuration",
}

var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Display current configuration",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := cli.LoadConfig()
		if err != nil {
			return err
		}

		path, _ := cli.DefaultConfigPath()

		fmt.Printf("%sConfig file:%s %s\n", cli.Bold, cli.Reset, path)
		fmt.Printf("%sServer:%s     %s\n", cli.Dim, cli.Reset, valueOrDefault(cfg.Server, "(default: http://localhost:8080)"))
		fmt.Printf("%sWorkspace:%s  %s\n", cli.Dim, cli.Reset, valueOrDefault(cfg.Workspace, "(not set)"))
		fmt.Printf("%sFormat:%s     %s\n", cli.Dim, cli.Reset, valueOrDefault(cfg.Format, "(default: table)"))
		fmt.Printf("%sDefAgent:%s   %s\n", cli.Dim, cli.Reset, valueOrDefault(cfg.DefaultAgent, "(not set, used by `crewship ask`)"))
		fmt.Printf("%sMarkdown:%s   %s\n", cli.Dim, cli.Reset, valueOrDefault(cfg.Markdown, "(default: auto)"))
		if cfg.Token != "" {
			fmt.Printf("%sToken:%s      %s...%s\n", cli.Dim, cli.Reset, cfg.Token[:20], cfg.Token[len(cfg.Token)-4:])
		} else {
			fmt.Printf("%sToken:%s      (not set)\n", cli.Dim, cli.Reset)
		}
		return nil
	},
}

var configSetCmd = &cobra.Command{
	Use:   "set <key> <value>",
	Short: "Set a configuration value",
	Long: `Set a configuration value. Available keys:
  server         - Server URL
  workspace      - Default workspace slug or ID
  format         - Default output format (table|json|yaml|quiet)
  default-agent  - Agent slug used by 'crewship ask' when no --agent flag
  markdown       - Markdown rendering: auto|on|off (default auto = TTY only)`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := cli.LoadConfig()
		if err != nil {
			cfg = &cli.CLIConfig{}
		}

		key, value := args[0], args[1]
		switch key {
		case "server":
			cfg.Server = value
		case "workspace":
			cfg.Workspace = value
		case "format":
			switch value {
			case "table", "json", "yaml", "quiet":
			default:
				return fmt.Errorf("invalid format %q, must be one of: table, json, yaml, quiet", value)
			}
			cfg.Format = value
		case "default-agent", "default_agent":
			cfg.DefaultAgent = value
		case "markdown":
			switch value {
			case "auto", "on", "off":
			default:
				return fmt.Errorf("invalid markdown %q, must be one of: auto, on, off", value)
			}
			cfg.Markdown = value
		default:
			return fmt.Errorf("unknown config key %q (available: server, workspace, format, default-agent, markdown)", key)
		}

		if err := cli.SaveConfig(cfg); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Config %s set to: %s", key, value))
		return nil
	},
}

func valueOrDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func init() {
	configCmd.AddCommand(configShowCmd)
	configCmd.AddCommand(configSetCmd)
}
