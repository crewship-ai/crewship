package main

import (
	"fmt"
	"os"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

var logsCmd = &cobra.Command{
	Use:   "logs <agent-slug>",
	Short: "View agent logs",
	Long: `View logs for an agent. Use --follow for live streaming.

Examples:
  crewship logs viktor
  crewship logs viktor --follow
  crewship logs viktor --lines 50`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		agentSlug := args[0]

		// Resolve agent to get ID and crew_id
		resp, err := client.Get("/api/v1/agents")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var agents []struct {
			ID     string  `json:"id"`
			Slug   string  `json:"slug"`
			CrewID *string `json:"crew_id"`
		}
		if err := cli.ReadJSON(resp, &agents); err != nil {
			return err
		}

		var agentID, crewID string
		for _, a := range agents {
			if a.Slug == agentSlug || a.ID == agentSlug {
				agentID = a.ID
				if a.CrewID != nil {
					crewID = *a.CrewID
				}
				break
			}
		}
		if agentID == "" {
			return fmt.Errorf("agent not found: %s", agentSlug)
		}
		if crewID == "" {
			return fmt.Errorf("agent has no crew (logs require a crew)")
		}

		lines, _ := cmd.Flags().GetInt("lines")
		follow, _ := cmd.Flags().GetBool("follow")

		// Fetch logs via proxy endpoint
		path := fmt.Sprintf("/api/v1/agents/%s/logs?crew_id=%s&limit=%d", agentID, crewID, lines)
		logResp, err := client.Get(path)
		if err != nil {
			return err
		}
		if err := cli.CheckError(logResp); err != nil {
			return err
		}

		var logEntries []struct {
			Timestamp string `json:"ts"`
			Level     string `json:"level"`
			Agent     string `json:"agent"`
			Event     string `json:"event"`
			Content   string `json:"content"`
		}
		if err := cli.ReadJSON(logResp, &logEntries); err != nil {
			return err
		}

		for _, l := range logEntries {
			ts := l.Timestamp
			if t, err := time.Parse(time.RFC3339Nano, l.Timestamp); err == nil {
				ts = t.Format("2006-01-02 15:04:05")
			}
			eventColor := ""
			switch l.Event {
			case "output":
				eventColor = cli.White
			case "error":
				eventColor = cli.Red
			default:
				eventColor = cli.Gray
			}
			fmt.Printf("%s%s%s %s[%s]%s %s\n", cli.Dim, ts, cli.Reset, eventColor, l.Event, cli.Reset, truncate(l.Content, 200))
		}

		if follow {
			return logsFollow(client, agentID, agentSlug)
		}

		return nil
	},
}

func logsFollow(client *cli.Client, agentID, agentSlug string) error {
	wsToken, err := cli.WSTokenFromServer(client)
	if err != nil {
		return fmt.Errorf("get WS token for follow: %w", err)
	}

	server := cli.ResolveServer(flagServer, cliCfg)
	ws, err := cli.NewWSClient(server, wsToken)
	if err != nil {
		return err
	}
	defer ws.Close()

	channel := "agent:" + agentID
	if err := ws.Subscribe(channel); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	fmt.Fprintf(os.Stderr, "%s[following logs for %s, Ctrl+C to stop]%s\n", cli.Dim, agentSlug, cli.Reset)

	for {
		msg, err := ws.ReadMessage()
		if err != nil {
			return nil
		}

		event, _ := cli.ParseChatEvent(msg)
		if event == nil {
			continue
		}

		ts := time.Now().Format("2006-01-02 15:04:05")
		fmt.Printf("%s%s%s [%s] %s\n", cli.Dim, ts, cli.Reset, event.Type, truncate(event.Content, 200))
	}
}

func init() {
	logsCmd.Flags().IntP("lines", "n", 100, "Number of log lines")
	logsCmd.Flags().BoolP("follow", "F", false, "Stream logs in real-time")
}
