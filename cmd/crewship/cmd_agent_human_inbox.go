package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

func newAgentHumanInboxCmd() *cobra.Command {
	return &cobra.Command{Use: "human <agent-slug-or-id>", Short: "Read authorized human messages, task sources and actual agent read receipts", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		id, err := resolveAgentID(c, args[0])
		if err != nil {
			return err
		}
		return chatRoomRequest(http.MethodGet, "/api/v1/agents/"+url.PathEscape(id)+"/human-inbox", nil)
	}}
}

func newAgentInboxSendCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "send <agent-slug-or-id>", Short: "Send a canonical channel message to one joined agent", Args: cobra.ExactArgs(1)}
	cmd.Flags().String("conversation", "", "Existing workspace channel ID (agent must already be joined)")
	cmd.Flags().String("message", "", "Human message text")
	cmd.Flags().String("client-id", "", "Stable send ID; reuse on retries")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		c, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		id, err := resolveAgentID(c, args[0])
		if err != nil {
			return err
		}
		room, _ := cmd.Flags().GetString("conversation")
		text, _ := cmd.Flags().GetString("message")
		key, _ := cmd.Flags().GetString("client-id")
		if room == "" || strings.TrimSpace(text) == "" || key == "" {
			return fmt.Errorf("--conversation, --message and --client-id are required")
		}
		return chatRoomRequest(http.MethodPost, chatRoomPath(room)+"/messages", map[string]any{"content": text, "client_id": key, "mentioned_agent_ids": []string{id}})
	}
	return cmd
}

func newAgentInboxTaskCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "task <agent-slug-or-id>", Short: "Create and start a canonical issue assigned to this agent", Args: cobra.ExactArgs(1)}
	cmd.Flags().String("crew", "", "Crew slug or ID")
	cmd.Flags().String("title", "", "Issue title")
	cmd.Flags().String("instructions", "", "Issue instructions")
	cmd.Flags().String("client-id", "", "Stable create ID; reuse on retries")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		c, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		agent, err := resolveAgentID(c, args[0])
		if err != nil {
			return err
		}
		crewRef, _ := cmd.Flags().GetString("crew")
		title, _ := cmd.Flags().GetString("title")
		text, _ := cmd.Flags().GetString("instructions")
		key, _ := cmd.Flags().GetString("client-id")
		if crewRef == "" || strings.TrimSpace(title) == "" || strings.TrimSpace(text) == "" || key == "" {
			return fmt.Errorf("--crew, --title, --instructions and --client-id are required")
		}
		crew, err := resolveCrewID(c, crewRef)
		if err != nil {
			return err
		}
		// The ordinary public issue routes retain admission, plan, review and human
		// acceptance gates. This wrapper never inserts a task/run or marks DONE.
		resp, err := c.WithHeader("Idempotency-Key", key).Post("/api/v1/crews/"+url.PathEscape(crew)+"/issues", map[string]any{"title": title, "description": text, "delegate_agent_id": agent})
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err = cli.CheckError(resp); err != nil {
			return err
		}
		var issue struct {
			ID         string `json:"id" yaml:"id"`
			Identifier string `json:"identifier" yaml:"identifier"`
		}
		if err = cli.ReadJSON(resp, &issue); err != nil {
			return err
		}
		if issue.Identifier == "" {
			return fmt.Errorf("created issue %s has no identifier", issue.ID)
		}
		path := "/api/v1/crews/" + url.PathEscape(crew) + "/issues/" + url.PathEscape(issue.Identifier)
		start, err := c.WithHeader("Idempotency-Key", key+":start").Post(path+"/start", map[string]any{})
		ambiguous := err != nil
		if err == nil {
			defer start.Body.Close()
			ambiguous = start.StatusCode == http.StatusBadRequest
			err = cli.CheckError(start)
		}
		if err != nil {
			original := err
			// Only ambiguous delivery or the canonical already-started 400
			// may recover through an authorized read. Never mask auth/conflict.
			if ambiguous {
				current, readErr := c.Get(path)
				if readErr == nil {
					defer current.Body.Close()
					if cli.CheckError(current) == nil {
						var observed struct {
							Status   string `json:"status" yaml:"status"`
							Delegate string `json:"delegate_agent_id" yaml:"delegate_agent_id"`
						}
						if cli.ReadJSON(current, &observed) == nil && observed.Delegate == agent && inboxExistingTaskState(observed.Status) {
							return resolvedFormatter(cmd).JSON(map[string]any{"issue_id": issue.ID, "identifier": issue.Identifier, "agent_id": agent, "state": "observed_existing", "issue_status": observed.Status, "read_at": nil})
						}
					}
				}
			}
			return fmt.Errorf("issue %s exists; start failed: %w", issue.Identifier, original)
		}
		return resolvedFormatter(cmd).JSON(map[string]any{"issue_id": issue.ID, "identifier": issue.Identifier, "agent_id": agent, "state": "accepted", "read_at": nil})
	}
	return cmd
}

func init() {
	agentInboxCmd.AddCommand(newAgentHumanInboxCmd(), newAgentInboxSendCmd(), newAgentInboxTaskCmd(), newAgentInboxReadCmd())
}

func inboxExistingTaskState(status string) bool {
	switch status {
	case "IN_PROGRESS", "REVIEW", "DONE", "CANCELLED":
		return true
	}
	return false
}

func newAgentInboxReadCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "read", Short: "Read and acknowledge the current authenticated agent run's human inbox source", Args: cobra.NoArgs}
	cmd.Flags().String("sidecar", "http://127.0.0.1:9119", "Local sidecar HTTP origin (loopback only)")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		endpoint, _ := cmd.Flags().GetString("sidecar")
		if err := validateAgentSidecarOrigin(endpoint); err != nil {
			return err
		}
		token := os.Getenv("CREWSHIP_AGENT_TOKEN")
		if token == "" {
			return fmt.Errorf("CREWSHIP_AGENT_TOKEN is required inside the current agent run")
		}
		const payload = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"inbox_read","arguments":{}}}`
		req, err := http.NewRequestWithContext(cmd.Context(), http.MethodPost, endpoint+"/mcp/routines", strings.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		var call struct {
			Result struct {
				Content []struct{ Text string }
				IsError bool
			}
			Error *struct{ Message string }
		}
		if err := cli.ReadJSON(resp, &call); err != nil {
			return err
		}
		if call.Error != nil {
			return fmt.Errorf("inbox read: %s", call.Error.Message)
		}
		if len(call.Result.Content) != 1 {
			return fmt.Errorf("inbox read returned an invalid result")
		}
		var result map[string]any
		if err := json.Unmarshal([]byte(call.Result.Content[0].Text), &result); err != nil {
			return err
		}
		if call.Result.IsError {
			return fmt.Errorf("inbox read: %v", result["error"])
		}
		return resolvedFormatter(cmd).Machine(result)
	}
	return cmd
}
