package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/conversation"
)

// GET /api/v1/chats/{chatId}/messages chat source messages
// GET /api/v1/conversations/{conversationId}/messages chat source messages
func newChatSourceCommand() *cobra.Command {
	root := &cobra.Command{Use: "source", Short: "Read versioned references from an existing authorized chat source"}
	messages := &cobra.Command{Use: "messages <agent_session|workspace_room> <source-id>", Args: cobra.ExactArgs(2), Short: "Read source identities; agent positions are snapshot ordinals, not event cursors", RunE: func(cmd *cobra.Command, args []string) error {
		kind := conversation.SourceKind(args[0])
		id := args[1]
		limit, _ := cmd.Flags().GetInt("limit")
		after, _ := cmd.Flags().GetInt64("after-sequence")
		before, _ := cmd.Flags().GetInt64("before-sequence")
		if kind != conversation.SourceAgentSession && kind != conversation.SourceWorkspaceRoom {
			return fmt.Errorf("source kind must be agent_session or workspace_room")
		}
		if id == "" || limit < 1 || limit > 100 || after < 0 || before < 0 || (cmd.Flags().Changed("after-sequence") && cmd.Flags().Changed("before-sequence")) || (cmd.Flags().Changed("before-sequence") && before == 0) {
			return fmt.Errorf("invalid source or native room pagination")
		}
		if kind == conversation.SourceAgentSession && (cmd.Flags().Changed("limit") || cmd.Flags().Changed("after-sequence") || cmd.Flags().Changed("before-sequence")) {
			return fmt.Errorf("agent sessions use a bounded snapshot without pagination")
		}
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		q := url.Values{"projection": {conversation.SourceProjection}}
		path := "/api/v1/chats/" + url.PathEscape(id) + "/messages"
		if kind == conversation.SourceWorkspaceRoom {
			path = "/api/v1/conversations/" + url.PathEscape(id) + "/messages"
			q.Set("limit", strconv.Itoa(limit))
			if cmd.Flags().Changed("after-sequence") {
				q.Set("after_sequence", strconv.FormatInt(after, 10))
			}
			if cmd.Flags().Changed("before-sequence") {
				q.Set("before_sequence", strconv.FormatInt(before, 10))
			}
		}
		resp, err := client.Do(http.MethodGet, path+"?"+q.Encode(), nil)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out conversation.SourceMessagesResponse
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		if out.ContractVersion != conversation.SourceContractVersion || out.ConversationRef.SourceKind != kind || out.ConversationRef.SourceID != id || out.ConversationRef.WorkspaceID != client.GetWorkspaceID() || out.Messages == nil {
			return fmt.Errorf("server returned an incompatible source projection")
		}
		return newFormatter().AutoDetail(out, [][]string{{"source_kind", string(kind)}, {"source_id", id}, {"messages", strconv.Itoa(len(out.Messages))}})
	}}
	messages.Flags().Int("limit", 100, "Native room page size (1..100; rooms only)")
	messages.Flags().Int64("after-sequence", 0, "Read after a native room sequence")
	messages.Flags().Int64("before-sequence", 0, "Read before a native room sequence")
	root.AddCommand(messages)
	return root
}
