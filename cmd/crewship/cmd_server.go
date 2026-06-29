package main

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Manage server profiles (multi-instance targeting)",
	Long: `Server profiles let one CLI config target several Crewship instances
(dev1/dev2/dev3, staging, prod) and switch between them, each with its own
auth token bound to its own host.

Select the active profile per command with --profile, per shell with the
CREWSHIP_PROFILE env var, or persist a default with 'crewship server use
<name>'. Authenticate a profile with 'crewship login --profile <name>'.

Typical setup:

  crewship server add dev1 --server https://crewship-dev1.example
  crewship login  --profile dev1
  crewship server add dev2 --server https://crewship-dev2.example
  crewship login  --profile dev2
  crewship server use dev1          # default target for this machine
  crewship --profile dev2 crew list # one-off against dev2`,
}

var serverListCmd = &cobra.Command{
	Use:   "list",
	Short: "List configured server profiles",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := cli.LoadConfig()
		if err != nil {
			return err
		}
		if len(cfg.Servers) == 0 {
			fmt.Println("No server profiles configured.")
			fmt.Println("Add one with: crewship server add <name> --server <url>")
			if cfg.Server != "" {
				fmt.Printf("\nLegacy single-server target: %s\n", cfg.Server)
			}
			return nil
		}

		active := cli.ActiveProfileName(flagProfile, cfg)
		names := make([]string, 0, len(cfg.Servers))
		for n := range cfg.Servers {
			names = append(names, n)
		}
		sort.Strings(names)

		for _, n := range names {
			p := cfg.Servers[n]
			marker := "  "
			if n == active {
				marker = cli.Green + "* " + cli.Reset
			}
			auth := cli.Dim + "no token" + cli.Reset
			if p.Token != "" {
				auth = "token set"
			}
			ws := p.Workspace
			if ws == "" {
				ws = "-"
			}
			fmt.Printf("%s%s%-12s%s %-44s ws=%-26s %s\n",
				marker, cli.Bold, n, cli.Reset, p.Server, ws, auth)
		}
		if active != "" && cfg.Servers[active] == nil {
			fmt.Printf("\n%s! active profile %q has no definition%s — run 'crewship server add %s --server <url>'\n",
				cli.Yellow, active, cli.Reset, active)
		}
		return nil
	},
}

var serverAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Add or update a server profile",
	Long: `Add or update a server profile's URL (and optional default workspace).

The URL comes from the global --server flag and the workspace from --workspace:

  crewship server add dev1 --server https://crewship-dev1.example
  crewship server add prod --server https://crewship.acme.com --workspace acme-eng

This only records the target; authenticate it separately with
'crewship login --profile <name>'. The first profile added becomes the
default 'current' profile.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := strings.TrimSpace(args[0])
		if name == "" {
			return fmt.Errorf("profile name must not be empty")
		}
		raw := strings.TrimSpace(flagServer)
		if raw == "" {
			return fmt.Errorf("--server <url> is required (e.g. crewship server add %s --server https://host)", name)
		}
		if u, err := url.Parse(raw); err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("invalid --server URL %q (want http(s)://host[:port])", raw)
		}

		cfg, err := cli.LoadConfig()
		if err != nil {
			return err
		}
		if cfg.Servers == nil {
			cfg.Servers = map[string]*cli.ServerProfile{}
		}
		p := cfg.Servers[name]
		if p == nil {
			p = &cli.ServerProfile{}
			cfg.Servers[name] = p
		}
		p.Server = raw
		if ws := strings.TrimSpace(flagWorkspace); ws != "" {
			p.Workspace = ws
		}
		if cfg.Current == "" {
			cfg.Current = name
		}
		if err := cli.SaveConfig(cfg); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Profile %q → %s", name, p.Server))
		if p.Token == "" {
			fmt.Printf("Authenticate it with: crewship login --profile %s\n", name)
		}
		return nil
	},
}

var serverUseCmd = &cobra.Command{
	Use:   "use <name>",
	Short: "Set the default active server profile",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := strings.TrimSpace(args[0])
		cfg, err := cli.LoadConfig()
		if err != nil {
			return err
		}
		if cfg.Servers == nil || cfg.Servers[name] == nil {
			return fmt.Errorf("no such profile %q (see 'crewship server list')", name)
		}
		cfg.Current = name
		if err := cli.SaveConfig(cfg); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Active profile set to %q (%s)", name, cfg.Servers[name].Server))
		return nil
	},
}

var serverRemoveCmd = &cobra.Command{
	Use:     "remove <name>",
	Aliases: []string{"rm"},
	Short:   "Remove a server profile",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := strings.TrimSpace(args[0])
		cfg, err := cli.LoadConfig()
		if err != nil {
			return err
		}
		if cfg.Servers == nil || cfg.Servers[name] == nil {
			return fmt.Errorf("no such profile %q", name)
		}
		delete(cfg.Servers, name)
		if cfg.Current == name {
			cfg.Current = ""
		}
		if err := cli.SaveConfig(cfg); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Removed profile %q", name))
		return nil
	},
}

var serverCurrentCmd = &cobra.Command{
	Use:   "current",
	Short: "Show the active server profile",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := cli.LoadConfig()
		if err != nil {
			return err
		}
		name, p := cfg.ActiveProfile(flagProfile)
		if name == "" {
			fmt.Printf("No profile active (legacy single-server mode).\nServer: %s\n",
				valueOrDefault(cfg.Server, "http://localhost:8080"))
			return nil
		}
		if p == nil {
			fmt.Printf("Active profile %q is selected but not defined (see 'crewship server list').\n", name)
			return nil
		}
		auth := "(none)"
		if p.Token != "" {
			auth = "set"
		}
		fmt.Printf("Active profile: %s\nServer:         %s\nWorkspace:      %s\nToken:          %s\n",
			name, p.Server, valueOrDefault(p.Workspace, "(none)"), auth)
		return nil
	},
}

func init() {
	serverCmd.AddCommand(serverListCmd, serverAddCmd, serverUseCmd, serverRemoveCmd, serverCurrentCmd)
}
