package main

// Cross-crew relations + collaboration commands: connect /
// disconnect / connections (the crew→crew permission graph), plus
// standup and peer-convs which surface inter-agent activity. Extracted
// from cmd_crew.go.

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

var crewConnectCmd = &cobra.Command{
	Use:   "connect <crew-slug-A> <crew-slug-B>",
	Short: "Create a connection between two crews (enables cross-crew task dispatch)",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		fromID, err := resolveCrewID(client, args[0])
		if err != nil {
			return err
		}
		toID, err := resolveCrewID(client, args[1])
		if err != nil {
			return err
		}

		direction, _ := cmd.Flags().GetString("direction")
		body := map[string]interface{}{
			"from_crew_id": fromID,
			"to_crew_id":   toID,
			"direction":    direction,
		}

		resp, err := client.Post("/api/v1/crew-connections", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var created struct {
			ID string `json:"id"`
		}
		if err := cli.ReadJSON(resp, &created); err != nil {
			return err
		}

		cli.PrintSuccess(fmt.Sprintf("Crews connected: %s <-> %s (id: %s)", args[0], args[1], created.ID))
		return nil
	},
}

var crewDisconnectCmd = &cobra.Command{
	Use:   "disconnect <connection-id>",
	Short: "Remove a crew connection",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Delete("/api/v1/crew-connections/" + args[0])
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess("Crew connection removed.")
		return nil
	},
}

var crewConnectionsCmd = &cobra.Command{
	Use:   "connections",
	Short: "List all crew connections in the workspace",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Get("/api/v1/crew-connections")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var conns []struct {
			ID           string `json:"id"`
			FromCrewSlug string `json:"from_crew_slug"`
			ToCrewSlug   string `json:"to_crew_slug"`
			Direction    string `json:"direction"`
			Status       string `json:"status"`
			CreatedAt    string `json:"created_at"`
		}
		if err := cli.ReadJSON(resp, &conns); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"ID", "FROM", "TO", "DIRECTION", "STATUS", "CREATED"}
		var rows [][]string
		for _, c := range conns {
			rows = append(rows, []string{c.ID, c.FromCrewSlug, c.ToCrewSlug, c.Direction, c.Status, c.CreatedAt})
		}
		return f.Auto(conns, headers, rows)
	},
}

var crewStandupCmd = &cobra.Command{
	Use:   "standup <slug-or-id>",
	Short: "Show crew standup summary (last 24h activity)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		crewID, err := resolveCrewID(client, args[0])
		if err != nil {
			return err
		}

		path := "/api/v1/crews/" + crewID + "/standup"
		if since, _ := cmd.Flags().GetString("since"); since != "" {
			path += "?since=" + since
		}

		resp, err := client.Get(path)
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
		if f.Format == "json" {
			return f.JSON(result)
		}
		// Standup returns a text summary
		if text, ok := result["standup"].(string); ok {
			fmt.Println(text)
		} else {
			return f.JSON(result)
		}
		return nil
	},
}

var crewPeerConvsCmd = &cobra.Command{
	Use:   "peer-conversations <slug-or-id>",
	Short: "List peer conversations in a crew",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		crewID, err := resolveCrewID(client, args[0])
		if err != nil {
			return err
		}

		limit, _ := cmd.Flags().GetInt("limit")
		path := fmt.Sprintf("/api/v1/crews/%s/peer-conversations?limit=%d", crewID, limit)

		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		type peerConv struct {
			ID        string  `json:"id"`
			FromName  string  `json:"from_name"`
			ToName    string  `json:"to_name"`
			Question  string  `json:"question"`
			Response  *string `json:"response"`
			Status    string  `json:"status"`
			Escalated bool    `json:"escalated"`
			CreatedAt string  `json:"created_at"`
		}
		var items []peerConv
		if err := cli.ReadJSON(resp, &items); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"ID", "FROM", "TO", "QUESTION", "STATUS", "ESCALATED", "CREATED"}
		rows := make([][]string, len(items))
		for i, item := range items {
			q := item.Question
			if len(q) > 60 {
				q = q[:57] + "..."
			}
			esc := ""
			if item.Escalated {
				esc = "YES"
			}
			rows[i] = []string{item.ID[:8], item.FromName, item.ToName, q, item.Status, esc, item.CreatedAt}
		}
		return f.Auto(items, headers, rows)
	},
}
