package main

import (
	"fmt"
	"net/http"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

type sharedCrewCLI struct {
	ID         string `json:"id" yaml:"id"`
	Name       string `json:"name" yaml:"name"`
	Slug       string `json:"slug" yaml:"slug"`
	Restricted bool   `json:"restricted" yaml:"restricted"`
	AgentCount int    `json:"agent_count" yaml:"agent_count"`
	Agents     []struct {
		ID     string `json:"id" yaml:"id"`
		Name   string `json:"name" yaml:"name"`
		Slug   string `json:"slug" yaml:"slug"`
		Role   string `json:"agent_role" yaml:"agent_role"`
		Status string `json:"status" yaml:"status"`
	} `json:"agents,omitempty" yaml:"agents,omitempty"`
}

func printSharedCrewList(resp *http.Response) error {
	var items []sharedCrewCLI
	if err := cli.ReadJSON(resp, &items); err != nil {
		return err
	}
	rows := [][]string{}
	for _, item := range items {
		rows = append(rows, []string{item.Slug, item.Name, fmt.Sprint(item.AgentCount)})
	}
	f := newFormatter()
	defer printListFooter(f, readListMeta(resp), len(items))
	return f.Auto(items, []string{"SLUG", "NAME", "AGENTS"}, rows)
}
func printSharedCrewDetail(resp *http.Response) error {
	var item sharedCrewCLI
	if err := cli.ReadJSON(resp, &item); err != nil {
		return err
	}
	rows := [][]string{{"ID", item.ID}, {"Name", item.Name}, {"Slug", item.Slug}, {"Agents", fmt.Sprint(item.AgentCount)}}
	for _, agent := range item.Agents {
		rows = append(rows, []string{"Agent", agent.Slug + " (" + agent.Status + ")"})
	}
	return newFormatter().Auto(item, []string{"FIELD", "VALUE"}, rows)
}

var crewAccessCmd = &cobra.Command{
	Use:   "access <slug-or-id>",
	Short: "Explain your current Guest crew share",
	Long:  "Explain View and conditional Use from your current Guest crew share. This does not grant management, delegation, credentials or permission to run an agent without its own preflight.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		id, err := resolveCrewID(client, args[0])
		if err != nil {
			return err
		}
		resp, err := client.Get("/api/v1/crews/" + id + "/access/me")
		if err != nil {
			return err
		}
		if err = cli.CheckError(resp); err != nil {
			return err
		}
		var result struct {
			CrewID  string `json:"crew_id" yaml:"crew_id"`
			Actions map[string]struct {
				State  string `json:"state" yaml:"state"`
				Reason string `json:"reason" yaml:"reason"`
			} `json:"actions" yaml:"actions"`
		}
		if err = cli.ReadJSON(resp, &result); err != nil {
			return err
		}
		rows := [][]string{}
		for _, action := range []string{"view", "use", "manage"} {
			decision := result.Actions[action]
			rows = append(rows, []string{action, decision.State, decision.Reason})
		}
		return newFormatter().Auto(result, []string{"ACTION", "STATE", "REASON"}, rows)
	},
}
