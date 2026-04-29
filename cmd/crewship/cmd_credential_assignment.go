package main

import (
	"bufio"
	"fmt"
	"os"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

var credAssignCmd = &cobra.Command{
	Use:   "assign <name-or-id> <agent-slug>",
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
	Use:   "unassign <name-or-id> <agent-slug>",
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
		credID, err := resolveCredentialID(client, args[0])
		if err != nil {
			return err
		}
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
			if a.CredentialID == credID {
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

var credTestCmd = &cobra.Command{
	Use:   "test",
	Short: "Test a credential value before saving (validates against the provider API)",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}

		provider, _ := cmd.Flags().GetString("provider")
		credType, _ := cmd.Flags().GetString("type")
		value, _ := cmd.Flags().GetString("value")
		valueStdin, _ := cmd.Flags().GetBool("value-stdin")
		if value == "" && valueStdin {
			scanner := bufio.NewScanner(os.Stdin)
			if scanner.Scan() {
				value = scanner.Text()
			}
		}

		if provider == "" && credType != "SECRET" {
			return fmt.Errorf("--provider is required (e.g. ANTHROPIC, OPENAI, GOOGLE)")
		}
		if value == "" {
			return fmt.Errorf("--value or --value-stdin is required")
		}

		client := newAPIClient()
		client.WorkspaceID = ""
		resp, err := client.Post("/api/v1/credentials/test", map[string]string{
			"provider": provider,
			"type":     credType,
			"value":    value,
		})
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var result struct {
			Valid  bool   `json:"valid"`
			Status int    `json:"status"`
			Error  string `json:"error"`
		}
		if err := cli.ReadJSON(resp, &result); err != nil {
			return err
		}

		if result.Valid {
			cli.PrintSuccess("Credential is valid.")
		} else {
			msg := result.Error
			if msg == "" {
				msg = "validation failed"
			}
			return fmt.Errorf("credential invalid: %s", msg)
		}
		return nil
	},
}
