package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// chatCmd shows the full message log for a chat. Useful when you want
// to read what an agent answered without scrolling the live stream — or
// for archival / sharing of past conversations.
//
// Output is a sequence of "role · timestamp" headers followed by message
// bodies, optionally piped through the streaming markdown renderer so
// the on-disk markdown of past responses lands as styled output. JSON
// path returns the raw message list for scripting.
var chatCmd = &cobra.Command{
	Use:   "chat <chat-id>",
	Short: "Show the full message history of a chat session",
	Long: `Print every message in a chat session — user prompts, assistant
responses, tool calls — as a rendered markdown transcript.

Examples:
  crewship chat c_abc123
  crewship chat c_abc123 --no-markdown    # plain text
  crewship chat c_abc123 --format json | jq '.[] | {role, content}'
  crewship chat c_abc123 --since 24h      # filter by time (client-side)`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		chatID := args[0]

		path := "/api/v1/chats/" + url.PathEscape(chatID) + "/messages?limit=500"
		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		if err := getJSON(client, path, &body); err != nil {
			return err
		}

		// Apply optional --since filter client-side. Server has no since
		// param on this endpoint and adding it server-side is out of scope
		// for the CLI — filtering N≤500 entries in Go is cheap.
		if since, _ := cmd.Flags().GetString("since"); since != "" {
			t, err := parseSince(since)
			if err != nil {
				return fmt.Errorf("bad --since: %w", err)
			}
			filtered := body.Messages[:0]
			for _, m := range body.Messages {
				ts, _ := m["created_at"].(string)
				if mt, err := time.Parse(time.RFC3339, ts); err != nil || mt.After(t) {
					filtered = append(filtered, m)
				}
			}
			body.Messages = filtered
		}

		jq, _ := cmd.Flags().GetString("filter")
		if jq != "" {
			return emitJSONFiltered(cmd, body.Messages)
		}
		f := newFormatter()
		switch f.Format {
		case "json":
			return f.JSON(body.Messages)
		case "yaml":
			return f.YAML(body.Messages)
		}

		// Pretty path: use markdown renderer for assistant messages, plain
		// for everything else, with role/timestamp headers.
		md := resolveMarkdownFromCmd(cmd)
		printChatTranscript(body.Messages, md)
		return nil
	},
}

// printChatTranscript renders messages with role-aware styling. Assistant
// messages go through the markdown renderer; user/system stay plain.
func printChatTranscript(messages []map[string]any, md *cli.MarkdownRenderer) {
	if len(messages) == 0 {
		fmt.Printf("%sNo messages.%s\n", cli.Dim, cli.Reset)
		return
	}
	for i, m := range messages {
		role, _ := m["role"].(string)
		content, _ := m["content"].(string)
		ts, _ := m["created_at"].(string)
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			ts = t.Format("2006-01-02 15:04:05")
		}
		if i > 0 {
			fmt.Println()
		}

		color := cli.Cyan
		switch strings.ToLower(role) {
		case "user", "human":
			color = cli.Yellow
		case "assistant", "model":
			color = cli.Green
		case "system":
			color = cli.Dim
		}
		fmt.Printf("%s═══ %s%s%s · %s %s═══%s\n",
			color, cli.Bold, role, cli.Reset+color, ts, color, cli.Reset)

		out := content
		if md != nil && (role == "assistant" || role == "model") {
			out = md.Render(content)
		}
		fmt.Print(out)
		if !strings.HasSuffix(out, "\n") {
			fmt.Println()
		}
	}
}

// chatReactCmd groups the three reaction subcommands. Wraps
// MessageReactionsHandler on the server, scoped by (chat, message, emoji,
// user) — emoji presence is idempotent server-side, so add/remove can be
// called blindly without first checking list.
var chatReactCmd = &cobra.Command{
	Use:   "react",
	Short: "Add, remove or list emoji reactions on a chat message",
}

var chatReactAddCmd = &cobra.Command{
	Use:   "add <chat-id> <message-id> <emoji>",
	Short: "Add an emoji reaction to a message",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		body := map[string]string{"emoji": args[2]}
		path := "/api/v1/chats/" + url.PathEscape(args[0]) +
			"/messages/" + url.PathEscape(args[1]) + "/reactions"
		if err := postJSON(client, path, body, nil); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Added %s to message %s.", args[2], args[1]))
		return nil
	},
}

var chatReactRemoveCmd = &cobra.Command{
	Use:     "remove <chat-id> <message-id> <emoji>",
	Aliases: []string{"rm", "delete"},
	Short:   "Remove your emoji reaction from a message",
	Args:    cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		path := "/api/v1/chats/" + url.PathEscape(args[0]) +
			"/messages/" + url.PathEscape(args[1]) +
			"/reactions/" + url.PathEscape(args[2])
		if err := deleteJSON(client, path); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Removed %s from message %s.", args[2], args[1]))
		return nil
	},
}

var chatReactListCmd = &cobra.Command{
	Use:   "list <chat-id> <message-id>",
	Short: "List emoji reactions on a message (with counts)",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		path := "/api/v1/chats/" + url.PathEscape(args[0]) +
			"/messages/" + url.PathEscape(args[1]) + "/reactions"
		var body struct {
			Reactions []struct {
				Emoji string `json:"emoji"`
				Count int    `json:"count"`
				Mine  bool   `json:"mine"`
			} `json:"reactions"`
		}
		if err := getJSON(client, path, &body); err != nil {
			return err
		}

		f := newFormatter()
		switch f.Format {
		case "json":
			return f.JSON(body.Reactions)
		case "yaml":
			return f.YAML(body.Reactions)
		}

		if len(body.Reactions) == 0 {
			fmt.Printf("%sNo reactions.%s\n", cli.Dim, cli.Reset)
			return nil
		}
		headers := []string{"EMOJI", "COUNT", "MINE"}
		var rows [][]string
		for _, r := range body.Reactions {
			mine := "no"
			if r.Mine {
				mine = "yes"
			}
			rows = append(rows, []string{r.Emoji, fmt.Sprintf("%d", r.Count), mine})
		}
		f.Table(headers, rows)
		return nil
	},
}

// chatSteerCmd delivers a mid-turn steering message into a chat session.
// The server guards against racing a second run into a live turn — today
// the message is QUEUED for the next turn (live injection is a follow-up).
// This is the CLI parity for POST /api/v1/chats/{chatId}/steer.
var chatSteerCmd = &cobra.Command{
	Use:   "steer <chat-id>",
	Short: "Send a mid-turn steering message into a chat (queued for the next turn)",
	Long: `Queue a steering message for a chat session. If the agent is mid-turn,
the message is held and applied on the next turn rather than interrupting the
running one (live mid-turn injection is a planned follow-up). The text is
scanned for prompt-injection before it is accepted.

Examples:
  crewship chat steer c_abc123 --message "focus on the auth bug first"
  crewship chat steer c_abc123 -m "use the staging DB, not prod"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		message, _ := cmd.Flags().GetString("message")
		if strings.TrimSpace(message) == "" {
			return fmt.Errorf("--message is required")
		}
		chatID := args[0]

		path := "/api/v1/chats/" + url.PathEscape(chatID) + "/steer"
		var res struct {
			Queued   bool `json:"queued"`
			InFlight bool `json:"in_flight"`
		}
		if err := postJSON(client, path, map[string]string{"message": message}, &res); err != nil {
			return err
		}

		f := newFormatter()
		switch f.Format {
		case "json":
			return f.JSON(res)
		case "yaml":
			return f.YAML(res)
		}
		if res.InFlight {
			cli.PrintSuccess("Steering message queued — a run is in flight; it will apply on the next turn.")
		} else {
			cli.PrintSuccess("Steering message queued for the next turn.")
		}
		return nil
	},
}

// chatAttachCmd uploads a file as an attachment scoped to a (agent, chat)
// pair. The server route lives under /agents/{agentId}/chats/{chatId}, so
// the CLI resolves the agent ID from the chat (which the local server can
// answer via the messages-prefetch trick — see lookupChatAgentID below).
var chatAttachCmd = &cobra.Command{
	Use:   "attach <chat-id> <file-path>",
	Short: "Upload a file as an attachment to a chat session",
	Long: `Attach a file to a chat session. The file lands under
/output/<agent-slug>/attachments/<chat-id>/ inside the agent container — the
agent can read it like any other file in its workspace.

The agent ID is auto-resolved from the chat: pass --agent <slug-or-id> to
override (useful when the lookup is ambiguous or you've pre-fetched it).

Examples:
  crewship chat attach c_abc123 ./diagram.png
  crewship chat attach c_abc123 ./logs.tar.gz --agent viktor`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		chatID := args[0]
		localPath := args[1]

		agentOverride, _ := cmd.Flags().GetString("agent")
		var agentID string
		if agentOverride != "" {
			agentID, err = resolveAgentID(client, agentOverride)
			if err != nil {
				return err
			}
		} else {
			agentID, err = lookupChatAgentID(client, chatID)
			if err != nil {
				return fmt.Errorf("resolve agent for chat %s: %w (pass --agent to override)", chatID, err)
			}
		}

		fh, err := os.Open(localPath)
		if err != nil {
			return fmt.Errorf("open %s: %w", localPath, err)
		}
		defer fh.Close()

		// Multipart body assembled in memory: 25 MB server-side cap means
		// the bound is small. Streaming via io.Pipe + goroutine adds
		// complexity for no real benefit at this size.
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, err := mw.CreateFormFile("file", filepath.Base(localPath))
		if err != nil {
			return fmt.Errorf("multipart form: %w", err)
		}
		if _, err := io.Copy(fw, fh); err != nil {
			return fmt.Errorf("multipart copy: %w", err)
		}
		if err := mw.Close(); err != nil {
			return fmt.Errorf("multipart close: %w", err)
		}

		path := "/api/v1/agents/" + url.PathEscape(agentID) +
			"/chats/" + url.PathEscape(chatID) + "/attachments"
		resp, err := postMultipart(cmd.Context(), client, path, mw.FormDataContentType(), &buf)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var result struct {
			Filename  string `json:"filename"`
			Size      int    `json:"size"`
			AgentPath string `json:"agent_path"`
		}
		_ = cli.ReadJSON(resp, &result)
		if result.AgentPath != "" {
			cli.PrintSuccess(fmt.Sprintf("Uploaded %s (%d bytes) → %s",
				result.Filename, result.Size, result.AgentPath))
		} else {
			cli.PrintSuccess(fmt.Sprintf("Uploaded %s to chat %s.",
				filepath.Base(localPath), chatID))
		}
		return nil
	},
}

// chatListCmd lists recent chats for an agent. Same data as the
// SessionsSidebar in the web UI — exposed here so CLI users can pipe
// chat IDs into other commands (e.g. `crewship chat <id>` to print a
// transcript).
var chatListCmd = &cobra.Command{
	Use:   "list <agent-slug-or-id>",
	Short: "List recent chats for an agent (most recent first)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		agentID, err := resolveAgentID(client, args[0])
		if err != nil {
			return err
		}

		var chats []struct {
			ID           string  `json:"id"`
			Title        *string `json:"title"`
			Status       string  `json:"status"`
			MessageCount int     `json:"message_count"`
			StartedAt    string  `json:"started_at"`
			CreatedAt    string  `json:"created_at"`
			EndedAt      *string `json:"ended_at"`
			Origin       *string `json:"origin"`
		}
		if err := getJSON(client, "/api/v1/agents/"+agentID+"/chats", &chats); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"ID", "TITLE", "STATUS", "MSGS", "STARTED", "ORIGIN"}
		var rows [][]string
		for _, c := range chats {
			title := "-"
			if c.Title != nil && *c.Title != "" {
				title = truncateString(*c.Title, 36)
			}
			origin := "-"
			if c.Origin != nil && *c.Origin != "" {
				origin = *c.Origin
			}
			started := c.StartedAt
			if t, err := time.Parse(time.RFC3339, started); err == nil {
				started = t.Format("2006-01-02 15:04")
			}
			rows = append(rows, []string{
				c.ID, title, c.Status,
				fmt.Sprintf("%d", c.MessageCount),
				started, origin,
			})
		}
		return f.Auto(chats, headers, rows)
	},
}

// lookupChatAgentID finds which agent owns a chat by walking the agents
// list and querying each agent's chat list until the chat ID is found.
// Worst case scales with #agents — fine for the typical workspace size
// (single-digit to low-tens). Falls back to a clear error so the user
// knows to pass --agent.
func lookupChatAgentID(client *cli.Client, chatID string) (string, error) {
	resp, err := client.Get("/api/v1/agents")
	if err != nil {
		return "", err
	}
	if err := cli.CheckError(resp); err != nil {
		return "", err
	}
	var agents []struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	}
	if err := cli.ReadJSON(resp, &agents); err != nil {
		return "", err
	}
	for _, a := range agents {
		var chats []struct {
			ID string `json:"id"`
		}
		if err := getJSON(client, "/api/v1/agents/"+a.ID+"/chats", &chats); err != nil {
			// Skip agents we can't read rather than aborting — a single
			// permission-denied shouldn't blow up the whole lookup.
			continue
		}
		for _, c := range chats {
			if c.ID == chatID {
				return a.ID, nil
			}
		}
	}
	return "", fmt.Errorf("chat %s not found in any agent's recent sessions", chatID)
}

// postMultipart issues a multipart/form-data POST without going through
// the JSON-encoding client.Post path. Picks up auth + workspace_id the
// same way cli.Client.Do does.
func postMultipart(ctx context.Context, client *cli.Client, path, contentType string, body io.Reader) (*http.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	u, err := url.Parse(client.BaseURL + path)
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}
	wsID := client.GetWorkspaceID()
	if wsID != "" {
		q := u.Query()
		if q.Get("workspace_id") == "" {
			q.Set("workspace_id", wsID)
			u.RawQuery = q.Encode()
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if client.Token != "" {
		req.Header.Set("Authorization", "Bearer "+client.Token)
	}
	req.Header.Set("Content-Type", contentType)
	return client.HTTPClient.Do(req)
}

// chatParticipantsCmd groups the group-chat membership subcommands. CLI
// parity for the /api/v1/chats/{chatId}/participants endpoints. Adding the
// first participant promotes the chat to a group, after which the agent
// responds only when @mentioned.
var chatParticipantsCmd = &cobra.Command{
	Use:     "participants",
	Aliases: []string{"members"},
	Short:   "Manage who is in a multi-user group chat",
}

var chatParticipantsAddCmd = &cobra.Command{
	Use:   "add <chat-id> <user-id>",
	Short: "Add a user to a chat (promotes it to a group chat)",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		role, _ := cmd.Flags().GetString("role")
		body := map[string]string{"user_id": args[1]}
		if role != "" {
			body["role"] = role
		}
		path := "/api/v1/chats/" + url.PathEscape(args[0]) + "/participants"
		if err := postJSON(client, path, body, nil); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Added %s to chat %s (now a group chat).", args[1], args[0]))
		return nil
	},
}

var chatParticipantsRemoveCmd = &cobra.Command{
	Use:     "remove <chat-id> <user-id>",
	Aliases: []string{"rm", "delete"},
	Short:   "Remove a user from a group chat",
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		path := "/api/v1/chats/" + url.PathEscape(args[0]) +
			"/participants/" + url.PathEscape(args[1])
		if err := deleteJSON(client, path); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Removed %s from chat %s.", args[1], args[0]))
		return nil
	},
}

var chatParticipantsListCmd = &cobra.Command{
	Use:   "list <chat-id>",
	Short: "List the participants of a group chat",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		path := "/api/v1/chats/" + url.PathEscape(args[0]) + "/participants"
		var body struct {
			Participants []struct {
				UserID   string `json:"user_id"`
				Email    string `json:"email"`
				FullName string `json:"full_name"`
				Role     string `json:"role"`
				JoinedAt string `json:"joined_at"`
			} `json:"participants"`
		}
		if err := getJSON(client, path, &body); err != nil {
			return err
		}
		f := newFormatter()
		switch f.Format {
		case "json":
			return f.JSON(body.Participants)
		case "yaml":
			return f.YAML(body.Participants)
		}
		if len(body.Participants) == 0 {
			fmt.Printf("%sNo participants (private chat).%s\n", cli.Dim, cli.Reset)
			return nil
		}
		headers := []string{"USER ID", "EMAIL", "NAME", "ROLE"}
		var rows [][]string
		for _, p := range body.Participants {
			rows = append(rows, []string{p.UserID, p.Email, p.FullName, p.Role})
		}
		f.Table(headers, rows)
		return nil
	},
}

func init() {
	chatCmd.Flags().String("since", "", "Only show messages newer than this (1h, 24h, RFC3339)")
	chatCmd.Flags().Bool("markdown", false, "Force markdown ANSI styling")
	chatCmd.Flags().Bool("no-markdown", false, "Disable markdown ANSI styling")
	jqExprFlag(chatCmd)

	chatAttachCmd.Flags().String("agent", "", "Override the auto-resolved agent slug or ID")

	chatSteerCmd.Flags().StringP("message", "m", "", "Steering message text (required)")

	chatReactCmd.AddCommand(chatReactAddCmd)
	chatReactCmd.AddCommand(chatReactRemoveCmd)
	chatReactCmd.AddCommand(chatReactListCmd)

	chatParticipantsAddCmd.Flags().String("role", "member", "Participant role: member or owner")
	chatParticipantsCmd.AddCommand(chatParticipantsAddCmd)
	chatParticipantsCmd.AddCommand(chatParticipantsRemoveCmd)
	chatParticipantsCmd.AddCommand(chatParticipantsListCmd)

	chatCmd.AddCommand(chatReactCmd)
	chatCmd.AddCommand(chatParticipantsCmd)
	chatCmd.AddCommand(chatAttachCmd)
	chatCmd.AddCommand(chatListCmd)
	chatCmd.AddCommand(chatSteerCmd)
}
