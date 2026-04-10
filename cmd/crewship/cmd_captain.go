package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

var captainCmd = &cobra.Command{
	Use:   "captain",
	Short: "Chat with Captain (workspace AI assistant)",
}

var captainChatCmd = &cobra.Command{
	Use:   "chat <message>",
	Short: "Send a message to Captain and stream the response",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		message := strings.Join(args, " ")
		client := newAPIClient()
		wsID := client.GetWorkspaceID()

		body := map[string]string{"message": message}
		data, _ := json.Marshal(body)

		serverURL := cli.ResolveServer(flagServer, cliCfg)
		token := ""
		if cliCfg != nil {
			token = cliCfg.Token
		}

		u := serverURL + "/api/v1/captain/chat?workspace_id=" + wsID
		req, err := http.NewRequest("POST", u, strings.NewReader(string(data)))
		if err != nil {
			return fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}

		httpClient := &http.Client{Timeout: 120 * time.Second}
		resp, err := httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("request failed: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			var errBody map[string]interface{}
			json.NewDecoder(resp.Body).Decode(&errBody)
			if detail, ok := errBody["detail"].(string); ok {
				return fmt.Errorf("captain error: %s", detail)
			}
			return fmt.Errorf("captain error: HTTP %d", resp.StatusCode)
		}

		fmt.Printf("%sCaptain:%s ", cli.Bold, cli.Reset)

		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			raw := strings.TrimPrefix(line, "data: ")

			var ev map[string]interface{}
			if err := json.Unmarshal([]byte(raw), &ev); err != nil {
				continue
			}

			switch ev["type"] {
			case "text":
				fmt.Print(ev["content"])
			case "error":
				fmt.Printf("\n%sError: %v%s\n", cli.Red, ev["content"], cli.Reset)
			case "done":
				fmt.Println()
			}
		}

		return scanner.Err()
	},
}

var captainContextCmd = &cobra.Command{
	Use:   "context",
	Short: "Show Captain's workspace context (phase, counts)",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Get("/api/v1/captain/context")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var ctx struct {
			Phase              int    `json:"onboarding_phase"`
			Crews              int    `json:"crews"`
			Agents             int    `json:"agents"`
			ActiveMissions     int    `json:"active_missions"`
			PendingEscalations int    `json:"pending_escalations"`
			PendingProposals   int    `json:"pending_proposals"`
			WorkspaceID        string `json:"workspace_id"`
		}
		if err := cli.ReadJSON(resp, &ctx); err != nil {
			return err
		}

		phases := map[int]string{
			1: "EMPTY",
			2: "SETUP",
			3: "CREDENTIALS_NEEDED",
			4: "OPERATIONAL",
		}
		phaseName := phases[ctx.Phase]
		if phaseName == "" {
			phaseName = fmt.Sprintf("UNKNOWN(%d)", ctx.Phase)
		}

		fmt.Printf("%sWorkspace Phase:%s %s\n", cli.Bold, cli.Reset, phaseName)
		fmt.Printf("%sCrews:%s           %d\n", cli.Bold, cli.Reset, ctx.Crews)
		fmt.Printf("%sAgents:%s          %d\n", cli.Bold, cli.Reset, ctx.Agents)
		fmt.Printf("%sActive missions:%s %d\n", cli.Bold, cli.Reset, ctx.ActiveMissions)
		fmt.Printf("%sPending escalations:%s %d\n", cli.Bold, cli.Reset, ctx.PendingEscalations)
		fmt.Printf("%sPending proposals:%s   %d\n", cli.Bold, cli.Reset, ctx.PendingProposals)

		return nil
	},
}

var captainHistoryCmd = &cobra.Command{
	Use:   "history",
	Short: "Show Captain chat history",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Get("/api/v1/captain/history")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var history struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := cli.ReadJSON(resp, &history); err != nil {
			return err
		}

		if len(history.Messages) == 0 {
			fmt.Println("No conversation history.")
			return nil
		}

		for _, m := range history.Messages {
			switch m.Role {
			case "user":
				fmt.Printf("%sYou:%s     %s\n\n", cli.Bold, cli.Reset, m.Content)
			case "assistant":
				content := m.Content
				if len(content) > 300 {
					content = content[:297] + "..."
				}
				fmt.Printf("%sCaptain:%s %s\n\n", cli.Bold, cli.Reset, content)
			}
		}

		return nil
	},
}

var captainHistoryClearCmd = &cobra.Command{
	Use:   "clear",
	Short: "Clear Captain chat history",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		if err := confirmAction(cmd, "Clear Captain chat history?"); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Delete("/api/v1/captain/history")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess("Captain history cleared.")
		return nil
	},
}

func init() {
	captainHistoryClearCmd.Flags().BoolP("yes", "y", false, "Skip confirmation")

	captainHistoryCmd.AddCommand(captainHistoryClearCmd)

	captainCmd.AddCommand(captainChatCmd)
	captainCmd.AddCommand(captainContextCmd)
	captainCmd.AddCommand(captainHistoryCmd)
}
