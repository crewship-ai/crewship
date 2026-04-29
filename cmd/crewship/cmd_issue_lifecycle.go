package main

// Issue CRUD writes — issueCreateCmd / issueUpdateCmd /
// issueDeleteCmd. Extracted from cmd_issue.go for readability;
// init() in the main file still wires them onto issueCmd.

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

var issueCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new issue",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		flags := cmd.Flags()
		crewSlug, _ := flags.GetString("crew")
		title, _ := flags.GetString("title")
		if crewSlug == "" {
			return fmt.Errorf("--crew is required")
		}
		if title == "" {
			return fmt.Errorf("--title is required")
		}

		client := newAPIClient()
		crewID, err := resolveCrewID(client, crewSlug)
		if err != nil {
			return err
		}

		body := map[string]interface{}{
			"title": title,
		}

		if v, _ := flags.GetString("description"); v != "" {
			body["description"] = v
		}
		if v, _ := flags.GetString("priority"); v != "" {
			body["priority"] = v
		}
		if v, _ := flags.GetString("assignee"); v != "" {
			atype, _ := flags.GetString("assignee-type")
			if atype == "" {
				atype = "agent"
			}
			switch atype {
			case "agent":
				agentID, err := resolveAgentID(client, v)
				if err != nil {
					return fmt.Errorf("cannot resolve assignee %q: %w", v, err)
				}
				body["assignee_id"] = agentID
			default:
				return fmt.Errorf("--assignee-type %q is not supported (only 'agent')", atype)
			}
			body["assignee_type"] = atype
		}
		if v, _ := flags.GetString("labels"); v != "" {
			body["labels"] = strings.Split(v, ",")
		}
		if v, _ := flags.GetString("due-date"); v != "" {
			body["due_date"] = v
		}

		resp, err := client.Post("/api/v1/crews/"+crewID+"/issues", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var created issueItem
		if err := cli.ReadJSON(resp, &created); err != nil {
			return err
		}

		identifier := derefStr(created.Identifier, created.ID)
		cli.PrintSuccess(fmt.Sprintf("Created issue %s: %s", identifier, created.Title))
		return nil
	},
}

var issueUpdateCmd = &cobra.Command{
	Use:   "update <identifier>",
	Short: "Update an issue",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()

		// Fetch issue to get crew_id
		issue, err := fetchIssue(client, args[0])
		if err != nil {
			return err
		}

		body := map[string]interface{}{}
		flags := cmd.Flags()

		if flags.Changed("title") {
			v, _ := flags.GetString("title")
			body["title"] = v
		}
		if flags.Changed("description") {
			v, _ := flags.GetString("description")
			body["description"] = v
		}
		if flags.Changed("status") {
			v, _ := flags.GetString("status")
			body["status"] = v
		}
		if flags.Changed("priority") {
			v, _ := flags.GetString("priority")
			body["priority"] = v
		}
		if flags.Changed("assignee") {
			v, _ := flags.GetString("assignee")
			if v == "" {
				body["assignee_id"] = nil
				body["assignee_type"] = nil
			} else {
				atype, _ := flags.GetString("assignee-type")
				if atype == "" {
					atype = "agent"
				}
				switch atype {
				case "agent":
					agentID, err := resolveAgentID(client, v)
					if err != nil {
						return fmt.Errorf("cannot resolve assignee %q: %w", v, err)
					}
					body["assignee_id"] = agentID
				default:
					return fmt.Errorf("--assignee-type %q is not supported (only 'agent')", atype)
				}
				body["assignee_type"] = atype
			}
		} else if flags.Changed("assignee-type") {
			v, _ := flags.GetString("assignee-type")
			if strings.TrimSpace(v) == "" {
				body["assignee_type"] = nil
			} else if v != "agent" {
				return fmt.Errorf("--assignee-type %q is not supported (only 'agent')", v)
			} else {
				body["assignee_type"] = v
			}
		}
		if flags.Changed("due-date") {
			v, _ := flags.GetString("due-date")
			body["due_date"] = v
		}

		if len(body) == 0 {
			return fmt.Errorf("no fields to update")
		}

		identifier := derefStr(issue.Identifier, issue.ID)
		resp, err := client.Patch(
			fmt.Sprintf("/api/v1/crews/%s/issues/%s", issue.CrewID, url.PathEscape(identifier)),
			body,
		)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess(fmt.Sprintf("Issue %s updated.", args[0]))
		return nil
	},
}

var issueDeleteCmd = &cobra.Command{
	Use:   "delete <identifier>",
	Short: "Delete an issue",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		if err := confirmAction(cmd, fmt.Sprintf("Delete issue %q?", args[0])); err != nil {
			return err
		}

		client := newAPIClient()
		issue, err := fetchIssue(client, args[0])
		if err != nil {
			return err
		}

		identifier := derefStr(issue.Identifier, issue.ID)
		resp, err := client.Delete(
			fmt.Sprintf("/api/v1/crews/%s/issues/%s", issue.CrewID, url.PathEscape(identifier)),
		)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess(fmt.Sprintf("Issue %s deleted.", args[0]))
		return nil
	},
}
