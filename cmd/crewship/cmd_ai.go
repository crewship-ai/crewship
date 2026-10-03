package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

//go:embed ai/crewship/SKILL.md
var crewshipAISkill string

// Configuration contains launch arguments only, never stored tokens. Explicit
// target selection survives clients launching us from another working directory.
func aiLaunch(cmd *cobra.Command) (string, []string, error) {
	executable, err := stableAIExecutable()
	if err != nil {
		return "", nil, err
	}
	args := []string{"mcp", "serve"}
	catalog, _ := cmd.Flags().GetString("catalog")
	if catalog != "" && catalog != "auto" {
		if catalog != "server" && catalog != "embedded" {
			return "", nil, apiValidation("catalog must be auto, server, or embedded")
		}
		args = append(args, "--catalog", catalog)
	}
	if configPath := os.Getenv("CREWSHIP_CONFIG"); configPath != "" {
		absolute, err := filepath.Abs(configPath)
		if err != nil {
			return "", nil, err
		}
		args = append(args, "--credential-config", absolute)
	}
	profile, err := aiProfile()
	if err != nil {
		return "", nil, err
	}
	if profile != "" {
		args = append(args, "--profile", profile)
	}
	server := cli.EffectiveServer(flagServer, flagProfile, cliCfg)
	if _, err := validatedAPIServer(server); err != nil {
		return "", nil, err
	}
	// Preserve explicit server selection; otherwise the named profile can evolve.
	if profile == "" || flagServer != "" {
		args = append(args, "--server", server)
	}
	if workspace := cli.ResolveWorkspace(flagWorkspace, cliCfg); workspace != "" {
		args = append(args, "--workspace", workspace)
	}
	write, _ := cmd.Flags().GetBool("allow-write")
	if write {
		args = append(args, "--allow-write")
	}
	tags, _ := cmd.Flags().GetStringSlice("write-tags")
	if len(tags) > 0 {
		args = append(args, "--write-tags", strings.Join(tags, ","))
	}
	operations, _ := cmd.Flags().GetStringSlice("write-operations")
	if len(operations) > 0 {
		if !write {
			return "", nil, apiValidation("--write-operations requires --allow-write")
		}
		args = append(args, "--write-operations", strings.Join(operations, ","))
	}
	approval, _ := cmd.Flags().GetBool("require-approval")
	if approval {
		args = append(args, "--require-approval")
	}
	if len(tags) > 0 && !write {
		return "", nil, apiValidation("--write-tags requires --allow-write")
	}
	if len(tags) > 0 || len(operations) > 0 {
		doc, err := loadAPIDocument()
		if err != nil {
			return "", nil, err
		}
		known, err := doc.operations("", "")
		if err != nil {
			return "", nil, err
		}
		policy := cliMCP{operations: known, allowWrite: write, writeTags: tags, writeOperations: operations}
		if err = policy.validateWriteTags(); err != nil {
			return "", nil, err
		}
	}
	return executable, args, nil
}

// A token supplied by environment must not hide a mistyped target profile.
func aiProfile() (string, error) {
	profile := cli.ActiveProfileName(flagProfile, cliCfg)
	if profile != "" && (cliCfg == nil || cliCfg.Servers[profile] == nil || strings.TrimSpace(cliCfg.Servers[profile].Server) == "") {
		return "", apiValidation("selected profile is not configured")
	}
	return profile, nil
}

func aiClientConfig(client, executable string, args []string) (string, error) {
	entry := map[string]any{"command": executable, "args": args}
	if client == "codex" {
		command, _ := json.Marshal(executable)
		argv, _ := json.Marshal(args)
		return fmt.Sprintf("[mcp_servers.crewship]\ncommand = %s\nargs = %s\n", command, argv), nil
	}
	key := "mcpServers"
	switch client {
	case "generic", "claude", "cursor", "gemini":
	case "vscode":
		key = "servers"
		entry["type"] = "stdio"
	default:
		return "", apiValidation("client must be generic, codex, claude, cursor, vscode, or gemini")
	}
	data, err := json.MarshalIndent(map[string]any{key: map[string]any{"crewship": entry}}, "", "  ")
	return string(data) + "\n", err
}

// Atomic no-clobber installation. A matching file is already installed; a
// different file (including a symlink) is never replaced by the installer.
func installAISkill(directory string) error {
	if strings.TrimSpace(directory) == "" {
		return apiValidation("directory must not be empty")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	target := filepath.Join(directory, "SKILL.md")
	if info, err := os.Lstat(target); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing non-regular SKILL.md")
		}
		existing, err := os.ReadFile(target)
		if err != nil {
			return err
		}
		if string(existing) == crewshipAISkill {
			return nil
		}
		return fmt.Errorf("SKILL.md already exists with different content; choose another directory or remove it explicitly")
	} else if !os.IsNotExist(err) {
		return err
	}
	f, err := os.CreateTemp(directory, ".crewship-skill-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.WriteString(crewshipAISkill); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Link(f.Name(), target)
}

func aiRegistrationArgs(client, executable string, args []string) ([]string, error) {
	var out []string
	switch client {
	case "codex":
		out = []string{"mcp", "add", "crewship", "--", executable}
	case "claude":
		out = []string{"mcp", "add", "--transport", "stdio", "--scope", "user", "crewship", "--", executable}
	default:
		return nil, apiValidation("automatic connection supports codex or claude; use 'crewship ai config <client>' for other clients")
	}
	return append(out, args...), nil
}

func newAICommand() *cobra.Command {
	root := &cobra.Command{Use: "ai", Short: "Connect AI clients and read or install the guide bundled in this binary", Long: `Crewship includes an MCP stdio server and an Agent Skill in the binary.
Use connect for installed Codex/Claude Code clients, config for other clients,
or skill to export SKILL.md to a skill directory chosen by you.
Client support for MCP stdio or Agent Skills is required; a model name alone
does not guarantee support. No extra runtime, server download, or provider key
is needed for the adapter. Crewship API calls still require Crewship login.`}
	skill := &cobra.Command{Use: "skill", Aliases: []string{"guide"}, Short: "Print the embedded Agent Skill or install it into a chosen skill directory", Args: apiArgs(cobra.NoArgs), RunE: func(cmd *cobra.Command, _ []string) error {
		directory, _ := cmd.Flags().GetString("directory")
		if cmd.Flags().Changed("directory") {
			if err := installAISkill(directory); err != nil {
				return err
			}
			_, err := fmt.Fprintln(cmd.ErrOrStderr(), filepath.Join(directory, "SKILL.md"))
			return err
		}
		_, err := fmt.Fprint(cmd.OutOrStdout(), crewshipAISkill)
		return err
	}}
	skill.Flags().String("directory", "", "Install SKILL.md in this skill directory without overwriting differing content")
	config := &cobra.Command{Use: "config [client]", Short: "Print an MCP configuration snippet without credentials or modifying client files", Args: apiArgs(cobra.MaximumNArgs(1)), ValidArgs: []string{"generic", "codex", "claude", "cursor", "vscode", "gemini"}, RunE: func(cmd *cobra.Command, args []string) error {
		client := "generic"
		if len(args) > 0 {
			client = args[0]
		}
		executable, argv, err := aiLaunch(cmd)
		if err != nil {
			return err
		}
		data, err := aiClientConfig(client, executable, argv)
		if err != nil {
			return err
		}
		_, err = fmt.Fprint(cmd.OutOrStdout(), data)
		return err
	}}
	config.Flags().String("catalog", "auto", "Catalog source: auto with offline fallback, required server, or embedded")
	config.Flags().StringSlice("write-operations", nil, "Further restrict writes to exact operation IDs")
	config.Flags().StringSlice("write-tags", nil, "Limit writes to exact catalog tags; admin must be explicitly listed")
	config.Flags().Bool("require-approval", false, "Require MCP client human approval for writes")
	config.Flags().Bool("allow-write", false, "Include opt-in mutation support in the generated MCP configuration")
	connect := &cobra.Command{Use: "connect <codex|claude>", Short: "Register this binary with an installed AI client's native MCP command", Args: apiArgs(cobra.ExactArgs(1)), ValidArgs: []string{"codex", "claude"}, Long: `Register the crewship MCP server using the installed client's own configuration
command. Claude Code uses user scope; Codex uses its normal MCP configuration.
Only unchanged entries created by ai connect may be replaced. Credentials are
not copied. Restart/reload
the client after registration. --dry-run prints the executable and argument array
without running the client or changing files.`, RunE: func(cmd *cobra.Command, args []string) error {
		executable, argv, err := aiLaunch(cmd)
		if err != nil {
			return err
		}
		registration, err := aiRegistrationArgs(args[0], executable, argv)
		if err != nil {
			return err
		}
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		if dryRun {
			return apiStructuredOutput(cmd, map[string]any{"command": args[0], "args": registration, "dry_run": true})
		}
		return connectAIClient(cmd, args[0], executable, argv, registration)
	}}
	connect.Flags().String("catalog", "auto", "Catalog source: auto with offline fallback, required server, or embedded")
	connect.Flags().StringSlice("write-operations", nil, "Further restrict writes to exact operation IDs")
	connect.Flags().StringSlice("write-tags", nil, "Limit writes to exact catalog tags; admin must be explicitly listed")
	connect.Flags().Bool("require-approval", false, "Require MCP client human approval for writes")
	connect.Flags().Bool("allow-write", false, "Enable explicitly confirmed mutations in the registered MCP server")
	connect.Flags().Bool("dry-run", false, "Print the client registration command without executing it")
	connect.Flags().Bool("install-skill", false, "Install the bundled skill in the selected client’s user skill directory without overwriting custom content")
	root.AddCommand(skill, config, connect, newAIStatusCommand(false), newAIStatusCommand(true), newAIDisconnectCommand())
	return root
}

func init() { rootCmd.AddCommand(newAICommand()) }
