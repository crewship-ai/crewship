package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var credentialCmd = &cobra.Command{
	Use:     "credential",
	Aliases: []string{"cred"},
	Short:   "Manage credentials",
}

var credListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all credentials in the workspace",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Get("/api/v1/credentials")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var creds []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Type       string `json:"type"`
			Provider   string `json:"provider"`
			Status     string `json:"status"`
			AgentCount int    `json:"_count_agent_credentials"`
		}
		if err := cli.ReadJSON(resp, &creds); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"ID", "NAME", "TYPE", "PROVIDER", "STATUS", "AGENTS"}
		var rows [][]string
		for _, c := range creds {
			rows = append(rows, []string{c.ID, c.Name, c.Type, c.Provider, c.Status, fmt.Sprintf("%d", c.AgentCount)})
		}
		return f.Auto(creds, headers, rows)
	},
}

var credGetCmd = &cobra.Command{
	Use:   "get <name-or-id>",
	Short: "Show credential details (value is never displayed)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		credID, err := resolveCredentialID(client, args[0])
		if err != nil {
			return err
		}
		resp, err := client.Get("/api/v1/credentials/" + credID)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var cred struct {
			ID        string  `json:"id"`
			Name      string  `json:"name"`
			Type      string  `json:"type"`
			Provider  string  `json:"provider"`
			Status    string  `json:"status"`
			Scope     string  `json:"scope"`
			CreatedAt string  `json:"created_at"`
			CrewID    *string `json:"crew_id"`
		}
		if err := cli.ReadJSON(resp, &cred); err != nil {
			return err
		}

		f := newFormatter()
		pairs := [][]string{
			{"ID", cred.ID},
			{"Name", cred.Name},
			{"Type", cred.Type},
			{"Provider", cred.Provider},
			{"Status", cred.Status},
			{"Scope", cred.Scope},
			{"Created", cred.CreatedAt},
		}
		return f.AutoDetail(cred, pairs)
	},
}

func resolveCredentialID(client *cli.Client, nameOrID string) (string, error) {
	if looksLikeCUID(nameOrID) {
		return nameOrID, nil
	}

	resp, err := client.Get("/api/v1/credentials")
	if err != nil {
		return "", fmt.Errorf("resolve credential: %w", err)
	}
	if err := cli.CheckError(resp); err != nil {
		return "", err
	}

	var creds []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := cli.ReadJSON(resp, &creds); err != nil {
		return "", err
	}

	for _, c := range creds {
		if c.Name == nameOrID {
			return c.ID, nil
		}
	}
	return "", fmt.Errorf("credential %q not found", nameOrID)
}

// testCredentialValue validates a credential value against the provider API.
// Returns (valid, errorMessage). Skips test for SECRET type, NONE provider,
// and OAuth tokens (sk-ant-oat*) which cannot be validated via API.
func testCredentialValue(client *cli.Client, provider, credType, value string) (bool, string) {
	if credType == "SECRET" || provider == "" || provider == "NONE" {
		return true, ""
	}
	if strings.HasPrefix(value, "sk-ant-oat") {
		return true, ""
	}

	body := map[string]interface{}{
		"provider": provider,
		"type":     credType,
		"value":    value,
	}
	resp, err := client.Post("/api/v1/credentials/test", body)
	if err != nil {
		return false, "test request failed: " + err.Error()
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return false, "test request failed: " + err.Error()
	}

	var result struct {
		Valid bool   `json:"valid"`
		Error string `json:"error"`
	}
	if err := cli.ReadJSON(resp, &result); err != nil {
		return false, "failed to read test result"
	}
	return result.Valid, result.Error
}

// confirmInvalidKey prompts the user to confirm saving an invalid credential.
// Uses huh for interactive TTY sessions; falls back to plain stdin read when
// either stdin or stdout is not a TTY. We gate on BOTH: a redirected stdout
// (`crewship credential create ... > out.txt`) would otherwise cause huh to
// write ANSI escape sequences into the target file.
func confirmInvalidKey(errMsg string) bool {
	cli.PrintWarning(fmt.Sprintf("Key validation failed: %s", errMsg))

	// Non-TTY fallback (kept for safety even though caller already checks)
	stdinTTY := term.IsTerminal(int(os.Stdin.Fd()))
	stdoutTTY := term.IsTerminal(int(os.Stdout.Fd()))
	if !stdinTTY || !stdoutTTY {
		fmt.Print("Save anyway? [y/N] ")
		scanner := bufio.NewScanner(os.Stdin)
		if scanner.Scan() {
			answer := strings.TrimSpace(strings.ToLower(scanner.Text()))
			return answer == "y" || answer == "yes"
		}
		return false
	}

	var confirmed bool
	err := huh.NewConfirm().
		Title("Save anyway?").
		Description("The credential value failed provider validation — it may not work in production.").
		Affirmative("Save anyway").
		Negative("Cancel").
		Value(&confirmed).
		Run()
	if err != nil {
		return false
	}
	return confirmed
}

func init() {
	credCreateCmd.Flags().String("name", "", "Credential name (required)")
	credCreateCmd.Flags().String("type", "", "Type: SECRET|API_KEY|AI_CLI_TOKEN|CLI_TOKEN (required)")
	credCreateCmd.Flags().String("provider", "", "Provider: ANTHROPIC|OPENAI|GOOGLE|GITHUB|GITLAB|VERCEL|AWS|CUSTOM_CLI|NONE")
	credCreateCmd.Flags().String("value", "", "Credential value (visible in process list, prefer --value-stdin)")
	credCreateCmd.Flags().Bool("value-stdin", false, "Read value from stdin (secure)")
	credCreateCmd.Flags().String("env-var-name", "", "Environment variable name")
	credCreateCmd.Flags().Int("security-level", 0, "Keeper security level: 0 (none), 1 (low), 2 (medium), 3 (sensitive)")

	credUpdateCmd.Flags().String("name", "", "Credential name")
	credUpdateCmd.Flags().String("value", "", "New value")
	credUpdateCmd.Flags().Bool("value-stdin", false, "Read value from stdin")
	credUpdateCmd.Flags().Int("security-level", 0, "Keeper security level: 0 (none), 1 (low), 2 (medium), 3 (sensitive)")

	credAssignCmd.Flags().String("env-var-name", "", "Environment variable name override")
	credAssignCmd.Flags().Int("priority", 0, "Priority (1-10)")

	credDeleteCmd.Flags().BoolP("yes", "y", false, "Skip confirmation")

	credTestCmd.Flags().String("provider", "", "Provider: ANTHROPIC|OPENAI|GOOGLE|GITHUB|GITLAB|VERCEL|AWS|CUSTOM_CLI (required)")
	credTestCmd.Flags().String("type", "", "Type: API_KEY|AI_CLI_TOKEN|SECRET|CLI_TOKEN")
	credTestCmd.Flags().String("value", "", "Credential value to test")
	credTestCmd.Flags().Bool("value-stdin", false, "Read value from stdin")

	credentialCmd.AddCommand(credListCmd)
	credentialCmd.AddCommand(credCreateCmd)
	credentialCmd.AddCommand(credGetCmd)
	credentialCmd.AddCommand(credUpdateCmd)
	credentialCmd.AddCommand(credDeleteCmd)
	credentialCmd.AddCommand(credAssignCmd)
	credentialCmd.AddCommand(credUnassignCmd)
	credentialCmd.AddCommand(credTestCmd)
}
