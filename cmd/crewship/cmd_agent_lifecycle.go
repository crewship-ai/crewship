package main

// Agent CRUD-style cobra commands: create / update / delete. Extracted
// from cmd_agent.go for readability; init() in the main file still
// wires them onto agentCmd.

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

var agentCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new agent",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		flags := cmd.Flags()
		name, _ := flags.GetString("name")
		if name == "" {
			return fmt.Errorf("--name is required")
		}

		body := map[string]interface{}{"name": name}

		if v, _ := flags.GetString("slug"); v != "" {
			body["slug"] = v
		}
		if v, _ := flags.GetString("role"); v != "" {
			body["agent_role"] = v
		}
		if v, _ := flags.GetString("role-title"); v != "" {
			body["role_title"] = v
		}
		if v, _ := flags.GetString("cli-adapter"); v != "" {
			body["cli_adapter"] = v
		}
		if v, _ := flags.GetString("tool-profile"); v != "" {
			body["tool_profile"] = v
		}
		if v, _ := flags.GetString("lead-mode"); v != "" {
			body["lead_mode"] = v
		}
		if v, _ := flags.GetString("llm-provider"); v != "" {
			body["llm_provider"] = v
		}
		if v, _ := flags.GetString("llm-model"); v != "" {
			body["llm_model"] = v
		}
		if v, _ := flags.GetInt("timeout"); v > 0 {
			body["timeout_seconds"] = v
		}
		if v, _ := flags.GetBool("memory"); v {
			body["memory_enabled"] = true
		}
		if v, _ := flags.GetString("avatar-seed"); v != "" {
			body["avatar_seed"] = v
		}
		if v, _ := flags.GetString("avatar-style"); v != "" {
			body["avatar_style"] = v
		}

		// System prompt: inline or @file
		if v, _ := flags.GetString("system-prompt"); v != "" {
			if strings.HasPrefix(v, "@") {
				data, err := os.ReadFile(v[1:])
				if err != nil {
					return fmt.Errorf("read system prompt file: %w", err)
				}
				body["system_prompt"] = string(data)
			} else {
				body["system_prompt"] = v
			}
		}

		// Resolve crew slug to ID
		if v, _ := flags.GetString("crew"); v != "" {
			client := newAPIClient()
			crewID, err := resolveCrewID(client, v)
			if err != nil {
				return err
			}
			body["crew_id"] = crewID
		}

		client := newAPIClient()
		resp, err := client.Post("/api/v1/agents", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var created struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
			Name string `json:"name"`
		}
		if err := cli.ReadJSON(resp, &created); err != nil {
			return err
		}

		cli.PrintSuccess(fmt.Sprintf("Agent created: %s (%s)", created.Slug, created.ID))
		return nil
	},
}

var agentUpdateCmd = &cobra.Command{
	Use:   "update <slug-or-id>",
	Short: "Update an agent",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		agentID, err := resolveAgentID(client, args[0])
		if err != nil {
			return err
		}

		body := map[string]interface{}{}
		flags := cmd.Flags()

		if flags.Changed("name") {
			v, _ := flags.GetString("name")
			body["name"] = v
		}
		if flags.Changed("role") {
			v, _ := flags.GetString("role")
			body["agent_role"] = v
		}
		if flags.Changed("role-title") {
			v, _ := flags.GetString("role-title")
			body["role_title"] = v
		}
		if flags.Changed("cli-adapter") {
			v, _ := flags.GetString("cli-adapter")
			body["cli_adapter"] = v
		}
		if flags.Changed("tool-profile") {
			v, _ := flags.GetString("tool-profile")
			body["tool_profile"] = v
		}
		if flags.Changed("lead-mode") {
			v, _ := flags.GetString("lead-mode")
			body["lead_mode"] = v
		}
		if flags.Changed("llm-provider") {
			v, _ := flags.GetString("llm-provider")
			body["llm_provider"] = v
		}
		if flags.Changed("llm-model") {
			v, _ := flags.GetString("llm-model")
			body["llm_model"] = v
		}
		if flags.Changed("timeout") {
			v, _ := flags.GetInt("timeout")
			body["timeout_seconds"] = v
		}
		if flags.Changed("memory") {
			v, _ := flags.GetBool("memory")
			body["memory_enabled"] = v
		}
		if flags.Changed("system-prompt") {
			v, _ := flags.GetString("system-prompt")
			if strings.HasPrefix(v, "@") {
				data, err := os.ReadFile(v[1:])
				if err != nil {
					return fmt.Errorf("read system prompt file: %w", err)
				}
				body["system_prompt"] = string(data)
			} else {
				body["system_prompt"] = v
			}
		}
		if flags.Changed("avatar-seed") {
			v, _ := flags.GetString("avatar-seed")
			body["avatar_seed"] = v
		}
		if flags.Changed("avatar-style") {
			v, _ := flags.GetString("avatar-style")
			body["avatar_style"] = v
		}

		if len(body) == 0 {
			return fmt.Errorf("no fields to update")
		}

		resp, err := client.Patch("/api/v1/agents/"+agentID, body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess("Agent updated successfully.")
		return nil
	},
}

var agentDeleteCmd = &cobra.Command{
	Use:   "delete <slug-or-id>",
	Short: "Delete an agent",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		if err := confirmAction(cmd, fmt.Sprintf("Delete agent %q?", args[0])); err != nil {
			return err
		}

		client := newAPIClient()
		agentID, err := resolveAgentID(client, args[0])
		if err != nil {
			return err
		}

		resp, err := client.Delete("/api/v1/agents/" + agentID)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess("Agent deleted.")
		return nil
	},
}
