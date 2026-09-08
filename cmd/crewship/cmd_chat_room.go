package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

const chatRoomBase = "/api/v1/conversations"

// Keep workspace rooms inside Chat while retaining the existing agent-session
// commands and their IDs. All calls use the authenticated, workspace-aware client.
func newChatRoomCmd() *cobra.Command {
	root := &cobra.Command{Use: "room", Aliases: []string{"rooms"}, Short: "Chat with people, private groups and workspace channels", Long: `Manage the human and mixed rooms shown in Chat. Private groups and DMs
contain humans only; workspace channels can include agents. An agent runs only
when send includes --mention-agent with its joined agent ID.

Use 'chat create <agent>' for a separate agent session. Room commands use the
same --server, --workspace and authentication as the rest of Crewship. JSON
output preserves the API envelopes, including pagination cursors.`}
	add := func(use, short string, n int, run func(*cobra.Command, []string) error) *cobra.Command {
		c := &cobra.Command{Use: use, Short: short, Args: cobra.ExactArgs(n), RunE: run}
		root.AddCommand(c)
		return c
	}
	continuation := add("continue <private-room-id>", "Continue in a new group or explicit workspace channel; private history is never copied", 1, func(c *cobra.Command, a []string) error {
		title, _ := c.Flags().GetString("title")
		members, _ := c.Flags().GetStringArray("member")
		kind, _ := c.Flags().GetString("kind")
		agent, _ := c.Flags().GetString("agent")
		client, _ := c.Flags().GetString("client-id")
		title = strings.TrimSpace(title)
		if title == "" || !utf8.ValidString(title) || utf8.RuneCountInString(title) > 120 {
			return fmt.Errorf("--title must contain 1..120 characters")
		}
		if strings.TrimSpace(client) == "" || len(client) > 128 || !utf8.ValidString(client) {
			return fmt.Errorf("--client-id is required and must be at most 128 UTF-8 bytes; reuse for retries")
		}
		if kind != "group" && kind != "channel" {
			return fmt.Errorf("--kind must be group or channel")
		}
		if len(members) > 498 || (kind == "group" && (len(members) == 0 || agent != "")) || (kind == "channel" && agent == "") {
			return fmt.Errorf("group needs additional --member humans and no agent; channel needs --agent and is visible to the workspace")
		}
		return chatRoomRequest(http.MethodPost, chatRoomPath(a[0])+"/continue", map[string]any{"kind": kind, "title": title, "member_ids": members, "agent_id": agent, "client_id": client})
	})
	continuation.Flags().String("title", "", "New room title (required)")
	continuation.Flags().String("kind", "group", "group (private; source must be a DM) or channel (workspace-visible)")
	continuation.Flags().String("client-id", "", "Stable retry identity (required)")
	continuation.Flags().String("agent", "", "Agent to join a new workspace channel; joining alone does not run it")
	continuation.Flags().StringArray("member", nil, "Additional workspace human ID (repeatable); existing humans are retained")
	activity := add("activity <room-id>", "Show or configure future issue/routine notifications (channel creator only)", 1, func(c *cobra.Command, a []string) error {
		path := chatRoomPath(a[0]) + "/activity"
		if !c.Flags().Changed("issues") && !c.Flags().Changed("routines") {
			return chatRoomRequest(http.MethodGet, path, nil)
		}
		if !c.Flags().Changed("issues") || !c.Flags().Changed("routines") {
			return fmt.Errorf("set both --issues and --routines explicitly; enabling starts with future events")
		}
		issues, _ := c.Flags().GetBool("issues")
		routines, _ := c.Flags().GetBool("routines")
		return chatRoomRequest(http.MethodPut, path, map[string]bool{"issues": issues, "routines": routines})
	})
	activity.Flags().Bool("issues", false, "Publish future issue lifecycle events")
	activity.Flags().Bool("routines", false, "Publish future routine run lifecycle events")
	list := add("list", "List accessible rooms (one page)", 0, func(c *cobra.Command, _ []string) error {
		limit, _ := c.Flags().GetInt("limit")
		offset, _ := c.Flags().GetInt("offset")
		if limit < 1 || limit > 100 || offset < 0 {
			return fmt.Errorf("--limit must be 1..100 and --offset nonnegative")
		}
		return chatRoomRequest(http.MethodGet, chatRoomBase+"?limit="+strconv.Itoa(limit)+"&offset="+strconv.Itoa(offset), nil)
	})
	list.Flags().Int("limit", 100, "Page size (1..100)")
	list.Flags().Int("offset", 0, "Start offset; next_offset is returned in JSON")
	create := add("create", "Create a private human group or workspace channel", 0, func(c *cobra.Command, _ []string) error {
		title, _ := c.Flags().GetString("title")
		kind, _ := c.Flags().GetString("kind")
		members, _ := c.Flags().GetStringArray("member")
		title = strings.TrimSpace(title)
		if title == "" || !utf8.ValidString(title) || utf8.RuneCountInString(title) > 120 {
			return fmt.Errorf("--title must contain 1..120 characters")
		}
		if kind != "group" && kind != "channel" {
			return fmt.Errorf("--kind must be group or channel")
		}
		return chatRoomRequest(http.MethodPost, chatRoomBase, map[string]any{"title": title, "kind": kind, "member_ids": members})
	})
	create.Flags().String("title", "", "Room title (required)")
	create.Flags().String("kind", "group", "group (private humans) or channel (workspace-visible)")
	create.Flags().StringArray("member", nil, "Human user ID to include (repeatable; creator is included automatically)")
	add("direct <user-id>", "Open or reuse your fixed two-person DM", 1, func(_ *cobra.Command, a []string) error {
		return chatRoomRequest(http.MethodPost, chatRoomBase+"/direct", map[string]string{"user_id": a[0]})
	})
	add("get <room-id>", "Show room metadata, unread count and read cursor", 1, func(_ *cobra.Command, a []string) error {
		return chatRoomRequest(http.MethodGet, chatRoomPath(a[0]), nil)
	})
	messages := add("messages <room-id>", "Read latest messages, older history, or catch up after a sequence", 1, func(c *cobra.Command, a []string) error {
		limit, _ := c.Flags().GetInt("limit")
		after, _ := c.Flags().GetInt64("after-sequence")
		before, _ := c.Flags().GetInt64("before-sequence")
		if limit < 1 || limit > 100 {
			return fmt.Errorf("--limit must be 1..100")
		}
		q := url.Values{"limit": {strconv.Itoa(limit)}}
		if c.Flags().Changed("after-sequence") {
			if after < 0 {
				return fmt.Errorf("--after-sequence must be nonnegative")
			}
			q.Set("after_sequence", strconv.FormatInt(after, 10))
		}
		if c.Flags().Changed("before-sequence") {
			if before < 1 {
				return fmt.Errorf("--before-sequence must be positive")
			}
			q.Set("before_sequence", strconv.FormatInt(before, 10))
		}
		if q.Has("after_sequence") && q.Has("before_sequence") {
			return fmt.Errorf("use only one of --after-sequence and --before-sequence")
		}
		return chatRoomRequest(http.MethodGet, chatRoomPath(a[0])+"/messages?"+q.Encode(), nil)
	})
	messages.Flags().Int("limit", 100, "Page size (1..100); JSON includes has_more")
	messages.Flags().Int64("after-sequence", 0, "Catch up after this cursor; 0 starts at the beginning")
	messages.Flags().Int64("before-sequence", 0, "Read messages older than this sequence")
	send := add("send <room-id>", "Send a message with a stable retry ID and optional structured agent mentions", 1, func(c *cobra.Command, a []string) error {
		content, _ := c.Flags().GetString("message")
		id, _ := c.Flags().GetString("client-id")
		mentions, _ := c.Flags().GetStringArray("mention-agent")
		if strings.TrimSpace(content) == "" || !utf8.ValidString(content) || len(content) > 32768 {
			return fmt.Errorf("--message must contain text and be at most 32768 bytes")
		}
		if strings.TrimSpace(id) == "" || len(id) > 128 {
			return fmt.Errorf("--client-id is required (at most 128 bytes); reuse it with the same content when retrying")
		}
		return chatRoomRequest(http.MethodPost, chatRoomPath(a[0])+"/messages", map[string]any{"content": content, "client_id": id, "mentioned_agent_ids": mentions})
	})
	send.Flags().StringP("message", "m", "", "Message text (required)")
	send.Flags().String("client-id", "", "Stable send ID (required); reuse for retries to avoid duplicate messages/jobs")
	send.Flags().StringArray("mention-agent", nil, "Joined agent ID to invoke (repeatable); plain @text does not invoke agents")
	read := add("read <room-id>", "Mark messages through an explicit sequence as read", 1, func(c *cobra.Command, a []string) error {
		sequence, _ := c.Flags().GetInt64("sequence")
		if !c.Flags().Changed("sequence") || sequence < 0 {
			return fmt.Errorf("--sequence is required and must be nonnegative")
		}
		return chatRoomRequest(http.MethodPost, chatRoomPath(a[0])+"/read", map[string]int64{"last_read_sequence": sequence})
	})
	read.Flags().Int64("sequence", 0, "Last message sequence actually read (required)")
	mute := add("mute <room-id>", "Mute room inbox notifications; use --muted=false to unmute", 1, func(c *cobra.Command, a []string) error {
		muted, _ := c.Flags().GetBool("muted")
		return chatRoomRequest(http.MethodPost, chatRoomPath(a[0])+"/mute", map[string]bool{"muted": muted})
	})
	mute.Flags().Bool("muted", true, "Whether notifications are muted")
	add("jobs <room-id>", "Inspect agent mention jobs, assignments and errors", 1, func(_ *cobra.Command, a []string) error {
		return chatRoomRequest(http.MethodGet, chatRoomPath(a[0])+"/agent-jobs", nil)
	})
	for _, membership := range []struct{ name, entity, field string }{{"participants", "user", "user_id"}, {"agents", "agent", "agent_id"}} {
		m := membership
		group := &cobra.Command{Use: m.name, Short: "Manage room " + m.name}
		group.AddCommand(&cobra.Command{Use: "list <room-id>", Short: "List joined " + m.name, Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, a []string) error {
			return chatRoomRequest(http.MethodGet, chatRoomPath(a[0])+"/"+m.name, nil)
		}})
		group.AddCommand(&cobra.Command{Use: "add <room-id> <" + m.entity + "-id>", Short: "Add a " + m.entity + " (subject to room access rules)", Args: cobra.ExactArgs(2), RunE: func(_ *cobra.Command, a []string) error {
			return chatRoomRequest(http.MethodPost, chatRoomPath(a[0])+"/"+m.name, map[string]string{m.field: a[1]})
		}})
		group.AddCommand(&cobra.Command{Use: "remove <room-id> <" + m.entity + "-id>", Aliases: []string{"rm"}, Short: "Remove a " + m.entity, Args: cobra.ExactArgs(2), RunE: func(_ *cobra.Command, a []string) error {
			return chatRoomRequest(http.MethodDelete, chatRoomPath(a[0])+"/"+m.name+"/"+url.PathEscape(a[1]), nil)
		}})
		root.AddCommand(group)
	}
	return root
}

func chatRoomPath(id string) string { return chatRoomBase + "/" + url.PathEscape(id) }

func chatRoomRequest(method, path string, input any) error {
	client, err := requireAuthAndWorkspace()
	if err != nil {
		return err
	}
	response, err := client.Do(method, path, input)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if err := cli.CheckError(response); err != nil {
		return err
	}
	body := map[string]any{}
	if response.StatusCode == http.StatusNoContent {
		body["status"] = "ok"
	} else if err := cli.ReadJSON(response, &body); err != nil {
		return err
	}
	// Detail output is useful for both metadata and nested result envelopes; keep
	// the complete API response in machine formats so no pagination data is lost.
	keys := make([]string, 0, len(body))
	for key := range body {
		if key != "id" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if _, ok := body["id"]; ok {
		keys = append([]string{"id"}, keys...)
	}
	pairs := make([][]string, 0, len(keys))
	for _, key := range keys {
		value := body[key]
		s, ok := value.(string)
		if !ok {
			encoded, _ := json.Marshal(value)
			s = string(encoded)
		}
		pairs = append(pairs, []string{key, s})
	}
	return newFormatter().AutoDetail(body, pairs)
}

func init() { chatCmd.AddCommand(newChatRoomCmd()) }
