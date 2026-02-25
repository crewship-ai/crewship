package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
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

var credCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a credential",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		flags := cmd.Flags()
		name, _ := flags.GetString("name")
		credType, _ := flags.GetString("type")
		provider, _ := flags.GetString("provider")
		value, _ := flags.GetString("value")
		valueStdin, _ := flags.GetBool("value-stdin")
		envVarName, _ := flags.GetString("env-var-name")

		if name == "" {
			return fmt.Errorf("--name is required")
		}
		if credType == "" {
			return fmt.Errorf("--type is required (SECRET, API_KEY, or AI_CLI_TOKEN)")
		}

		if valueStdin {
			scanner := bufio.NewScanner(os.Stdin)
			if scanner.Scan() {
				value = strings.TrimSpace(scanner.Text())
			}
		}

		if value == "" {
			return fmt.Errorf("--value or --value-stdin is required")
		}

		secLevel, _ := flags.GetInt("security-level")

		body := map[string]interface{}{
			"name":  name,
			"type":  credType,
			"value": value,
		}
		if provider != "" {
			body["provider"] = provider
		}
		if envVarName != "" {
			body["env_var_name"] = envVarName
		}
		if secLevel >= 1 && secLevel <= 3 {
			body["security_level"] = secLevel
		}

		client := newAPIClient()

		valid, errMsg := testCredentialValue(client, provider, credType, value)
		if valid {
			cli.PrintSuccess("Key validated successfully")
		} else if errMsg != "" {
			if !confirmInvalidKey(errMsg) {
				return fmt.Errorf("aborted")
			}
		}

		resp, err := client.Post("/api/v1/credentials", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var created struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := cli.ReadJSON(resp, &created); err != nil {
			return err
		}

		cli.PrintSuccess(fmt.Sprintf("Credential created: %s (%s)", created.Name, created.ID))
		return nil
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

var credUpdateCmd = &cobra.Command{
	Use:   "update <name-or-id>",
	Short: "Update a credential",
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

		body := map[string]interface{}{}
		flags := cmd.Flags()

		if flags.Changed("value") {
			v, _ := flags.GetString("value")
			body["value"] = v
		}
		if flags.Changed("name") {
			v, _ := flags.GetString("name")
			body["name"] = v
		}
		if flags.Changed("value-stdin") {
			scanner := bufio.NewScanner(os.Stdin)
			if scanner.Scan() {
				body["value"] = strings.TrimSpace(scanner.Text())
			}
		}
		if flags.Changed("security-level") {
			v, _ := flags.GetInt("security-level")
			if v < 0 || v > 3 {
				return fmt.Errorf("--security-level must be between 0 and 3")
			}
			body["security_level"] = v
		}

		if len(body) == 0 {
			return fmt.Errorf("no fields to update")
		}

		if val, ok := body["value"]; ok {
			if valStr, ok := val.(string); ok && valStr != "" {
				resp, err := client.Get("/api/v1/credentials/" + credID)
				if err == nil {
					var cred struct {
						Type     string `json:"type"`
						Provider string `json:"provider"`
					}
					if cli.ReadJSON(resp, &cred) == nil {
						valid, errMsg := testCredentialValue(client, cred.Provider, cred.Type, valStr)
						if valid {
							cli.PrintSuccess("Key validated successfully")
						} else if errMsg != "" {
							if !confirmInvalidKey(errMsg) {
								return fmt.Errorf("aborted")
							}
						}
					}
				}
			}
		}

		resp, err := client.Patch("/api/v1/credentials/"+credID, body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess("Credential updated.")
		return nil
	},
}

var credDeleteCmd = &cobra.Command{
	Use:   "delete <name-or-id>",
	Short: "Delete a credential",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		if err := confirmAction(cmd, fmt.Sprintf("Delete credential %q?", args[0])); err != nil {
			return err
		}

		client := newAPIClient()
		credID, err := resolveCredentialID(client, args[0])
		if err != nil {
			return err
		}
		resp, err := client.Delete("/api/v1/credentials/" + credID)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess("Credential deleted.")
		return nil
	},
}

var credAssignCmd = &cobra.Command{
	Use:   "assign <credential-id> <agent-slug>",
	Short: "Assign a credential to an agent",
	Args:  cobra.ExactArgs(2),
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
		agentID, err := resolveAgentID(client, args[1])
		if err != nil {
			return err
		}

		envVarName, _ := cmd.Flags().GetString("env-var-name")
		if envVarName == "" {
			return fmt.Errorf("--env-var-name is required (e.g. ANTHROPIC_API_KEY)")
		}
		priority, _ := cmd.Flags().GetInt("priority")

		body := map[string]interface{}{
			"credential_id": credID,
			"env_var_name":  envVarName,
		}
		if priority > 0 {
			body["priority"] = priority
		}

		resp, err := client.Post("/api/v1/agents/"+agentID+"/credentials", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess(fmt.Sprintf("Credential assigned to agent %s", args[1]))
		return nil
	},
}

var credUnassignCmd = &cobra.Command{
	Use:   "unassign <credential-id> <agent-slug>",
	Short: "Remove a credential from an agent",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		agentID, err := resolveAgentID(client, args[1])
		if err != nil {
			return err
		}

		// Look up the assignment ID from agent's credential list
		listResp, err := client.Get("/api/v1/agents/" + agentID + "/credentials")
		if err != nil {
			return err
		}
		if err := cli.CheckError(listResp); err != nil {
			return err
		}
		var assignments []struct {
			ID           string `json:"id"`
			CredentialID string `json:"credential_id"`
		}
		if err := cli.ReadJSON(listResp, &assignments); err != nil {
			return err
		}
		var assignmentID string
		for _, a := range assignments {
			if a.CredentialID == args[0] {
				assignmentID = a.ID
				break
			}
		}
		if assignmentID == "" {
			return fmt.Errorf("credential %s not assigned to agent %s", args[0], args[1])
		}

		resp, err := client.Delete("/api/v1/agents/" + agentID + "/credentials/" + assignmentID)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess(fmt.Sprintf("Credential removed from agent %s", args[1]))
		return nil
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
func confirmInvalidKey(errMsg string) bool {
	cli.PrintWarning(fmt.Sprintf("Key validation failed: %s", errMsg))
	fmt.Print("Save anyway? [y/N] ")
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		answer := strings.TrimSpace(strings.ToLower(scanner.Text()))
		return answer == "y" || answer == "yes"
	}
	return false
}

func init() {
	credCreateCmd.Flags().String("name", "", "Credential name (required)")
	credCreateCmd.Flags().String("type", "", "Type: SECRET|API_KEY|AI_CLI_TOKEN (required)")
	credCreateCmd.Flags().String("provider", "", "Provider: ANTHROPIC|OPENAI|GOOGLE|GITHUB|NONE")
	credCreateCmd.Flags().String("value", "", "Credential value (visible in process list, prefer --value-stdin)")
	credCreateCmd.Flags().Bool("value-stdin", false, "Read value from stdin (secure)")
	credCreateCmd.Flags().String("env-var-name", "", "Environment variable name")
	credCreateCmd.Flags().Int("security-level", 0, "Keeper security level: 1 (low), 2 (medium), 3 (sensitive)")

	credUpdateCmd.Flags().String("name", "", "Credential name")
	credUpdateCmd.Flags().String("value", "", "New value")
	credUpdateCmd.Flags().Bool("value-stdin", false, "Read value from stdin")
	credUpdateCmd.Flags().Int("security-level", 0, "Keeper security level: 1 (low), 2 (medium), 3 (sensitive)")

	credAssignCmd.Flags().String("env-var-name", "", "Environment variable name override")
	credAssignCmd.Flags().Int("priority", 0, "Priority (1-10)")

	credDeleteCmd.Flags().BoolP("yes", "y", false, "Skip confirmation")

	credentialCmd.AddCommand(credListCmd)
	credentialCmd.AddCommand(credCreateCmd)
	credentialCmd.AddCommand(credGetCmd)
	credentialCmd.AddCommand(credUpdateCmd)
	credentialCmd.AddCommand(credDeleteCmd)
	credentialCmd.AddCommand(credAssignCmd)
	credentialCmd.AddCommand(credUnassignCmd)
}
