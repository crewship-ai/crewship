package main

import (
	"fmt"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

var systemCmd = &cobra.Command{
	Use:   "system",
	Short: "Show system information (runtime, license, keeper)",
}

var systemInfoCmd = &cobra.Command{
	Use:   "info",
	Short: "Show runtime and license information",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}

		client := newAPIClient()
		client.WorkspaceID = ""

		// Runtime info
		runtimeResp, err := client.Get("/api/v1/system/runtime")
		if err != nil {
			return fmt.Errorf("runtime info: %w", err)
		}
		if err := cli.CheckError(runtimeResp); err != nil {
			return err
		}

		var runtime struct {
			Available bool   `json:"available"`
			Runtime   string `json:"runtime"`
			Version   string `json:"version"`
			Socket    string `json:"socket"`
		}
		if err := cli.ReadJSON(runtimeResp, &runtime); err != nil {
			return err
		}

		fmt.Printf("%sContainer Runtime%s\n", cli.Bold, cli.Reset)
		fmt.Printf("  Available:  %v\n", runtime.Available)
		fmt.Printf("  Runtime:    %s\n", runtime.Runtime)
		fmt.Printf("  Version:    %s\n", runtime.Version)
		if runtime.Socket != "" {
			fmt.Printf("  Socket:     %s\n", runtime.Socket)
		}

		// License info
		licenseResp, err := client.Get("/api/v1/system/license")
		if err != nil {
			return fmt.Errorf("license info: %w", err)
		}
		if licenseResp.StatusCode == 200 {
			var license struct {
				Edition     string `json:"edition"`
				LicenseID   string `json:"license_id"`
				LicenseeOrg string `json:"licensee_org"`
				MaxAgents   int    `json:"max_agents_per_crew"`
				MaxCrews    int    `json:"max_crews"`
				MaxMembers  int    `json:"max_members"`
			}
			if cli.ReadJSON(licenseResp, &license) == nil {
				fmt.Printf("\n%sLicense%s\n", cli.Bold, cli.Reset)
				fmt.Printf("  Edition:          %s\n", license.Edition)
				fmt.Printf("  Max crews:        %d\n", license.MaxCrews)
				fmt.Printf("  Max agents/crew:  %d\n", license.MaxAgents)
				fmt.Printf("  Max members:      %d\n", license.MaxMembers)
				if license.LicenseeOrg != "" {
					fmt.Printf("  Licensee:         %s\n", license.LicenseeOrg)
				}
			}
		} else {
			licenseResp.Body.Close()
		}

		return nil
	},
}

var systemKeeperCmd = &cobra.Command{
	Use:   "keeper",
	Short: "Show Keeper security system status",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}

		client := newAPIClient()
		client.WorkspaceID = ""

		resp, err := client.Get("/api/v1/system/keeper")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var keeper struct {
			Enabled      bool   `json:"enabled"`
			OllamaURL    string `json:"ollama_url"`
			Model        string `json:"model"`
			OllamaOnline bool   `json:"ollama_online"`
			SecretCount  int    `json:"secret_count"`
		}
		if err := cli.ReadJSON(resp, &keeper); err != nil {
			return err
		}

		status := cli.Red + "disabled" + cli.Reset
		if keeper.Enabled {
			status = cli.Green + "enabled" + cli.Reset
		}
		ollamaStatus := cli.Red + "offline" + cli.Reset
		if keeper.OllamaOnline {
			ollamaStatus = cli.Green + "online" + cli.Reset
		}

		fmt.Printf("%sKeeper Security%s\n", cli.Bold, cli.Reset)
		fmt.Printf("  Status:       %s\n", status)
		fmt.Printf("  Ollama URL:   %s\n", keeper.OllamaURL)
		fmt.Printf("  Model:        %s\n", keeper.Model)
		fmt.Printf("  Ollama:       %s\n", ollamaStatus)
		fmt.Printf("  Secret creds: %d\n", keeper.SecretCount)

		return nil
	},
}

var systemStatsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show admin stats (workspaces, users, agents, running)",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Get("/api/v1/admin/stats")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var stats struct {
			Workspaces int `json:"workspaces"`
			Users      int `json:"users"`
			Agents     int `json:"agents"`
			Running    int `json:"running"`
		}
		if err := cli.ReadJSON(resp, &stats); err != nil {
			return err
		}

		f := newFormatter()
		if f.Format == "json" {
			return f.JSON(stats)
		}
		fmt.Printf("%sAdmin Stats%s\n", cli.Bold, cli.Reset)
		fmt.Printf("  Workspaces: %d\n", stats.Workspaces)
		fmt.Printf("  Users:      %d\n", stats.Users)
		fmt.Printf("  Agents:     %d\n", stats.Agents)
		fmt.Printf("  Running:    %d\n", stats.Running)
		return nil
	},
}

var systemOnboardingCmd = &cobra.Command{
	Use:   "onboarding",
	Short: "Show onboarding status for the current user",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}

		client := newAPIClient()
		client.WorkspaceID = ""

		resp, err := client.Get("/api/v1/onboarding/status")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var result map[string]interface{}
		if err := cli.ReadJSON(resp, &result); err != nil {
			return err
		}

		f := newFormatter()
		return f.JSON(result)
	},
}

func init() {
	systemCmd.AddCommand(systemInfoCmd)
	systemCmd.AddCommand(systemKeeperCmd)
	systemCmd.AddCommand(systemStatsCmd)
	systemCmd.AddCommand(systemOnboardingCmd)
}
