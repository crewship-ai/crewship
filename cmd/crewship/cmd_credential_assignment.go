package main

import (
	"bufio"
	"fmt"
	"os"
	"time"

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

		// --ttl turns the assignment into a short-lived LEASE (#1373): the
		// grant expires after the given duration and is refused at
		// credential-injection time (e.g. /keeper/execute) once it lapses.
		// Omit it for a standing grant.
		if ttlStr, _ := cmd.Flags().GetString("ttl"); ttlStr != "" {
			d, perr := time.ParseDuration(ttlStr)
			if perr != nil {
				return fmt.Errorf("invalid --ttl %q: %w (use e.g. 30m, 2h, 24h)", ttlStr, perr)
			}
			if d <= 0 {
				return fmt.Errorf("--ttl must be positive (got %s)", ttlStr)
			}
			body["ttl_seconds"] = int64(d.Seconds())
		}

		resp, err := client.Post("/api/v1/agents/"+agentID+"/credentials", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		if _, ok := body["ttl_seconds"]; ok {
			ttlStr, _ := cmd.Flags().GetString("ttl")
			cli.PrintSuccess(fmt.Sprintf("Credential leased to agent %s (expires in %s)", args[1], ttlStr))
		} else {
			cli.PrintSuccess(fmt.Sprintf("Credential assigned to agent %s", args[1]))
		}
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
			ID           string `json:"id" yaml:"id"`
			CredentialID string `json:"credential_id" yaml:"credential_id"`
			GrantSource  string `json:"grant_source" yaml:"grant_source"`
		}
		if err := cli.ReadJSON(listResp, &assignments); err != nil {
			return err
		}
		var assignmentID, inheritedVia string
		for _, a := range assignments {
			if a.CredentialID != credID {
				continue
			}
			if a.ID != "" {
				assignmentID = a.ID
				break
			}
			inheritedVia = a.GrantSource
		}
		if assignmentID == "" && inheritedVia != "" {
			// The agent DOES receive it — through its crew or a binding, not
			// an assignment. "not assigned" was literally true and misled the
			// operator into thinking delivery had stopped.
			return inheritedUnassignError(args[0], args[1], inheritedVia)
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
			Valid  bool   `json:"valid" yaml:"valid"`
			Status int    `json:"status" yaml:"status"`
			Error  string `json:"error" yaml:"error"`
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

// inheritedUnassignError explains how to stop a delivery that no assignment
// made: each grant_source is removed at its own layer.
func inheritedUnassignError(cred, agent, source string) error {
	var how string
	switch source {
	case "crew":
		how = "A crew link delivers the credential to every agent in that crew. To stop delivery, unlink it from the crew " +
			"(crewship credential update " + cred + " --crews ...); if only some agents may hold it, put those agents in a separate crew and link it there"
	case "binding":
		how = "It is delivered by a credential binding (crew, workspace or agent scope). To stop delivery, find it with crewship credential bindings " +
			"and remove it with crewship credential unbind <binding-id>"
	default:
		how = "Remove it at the layer that grants it"
	}
	return fmt.Errorf("credential %s is not assigned to agent %s, but the agent still receives it (grant_source=%s). %s", cred, agent, source, how)
}
