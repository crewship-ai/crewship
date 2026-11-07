package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/mooring"
	"github.com/spf13/cobra"
)

// These commands use the caller's ordinary human credential. They neither
// admit a human editor session nor construct an internal agent principal.
func newCraftAgentTaskCommand() *cobra.Command {
	task := &cobra.Command{Use: "task", Short: "Manage an explicitly consented native agent task"}
	start := &cobra.Command{Use: "start <page>", Args: cobra.ExactArgs(1), Short: "Consent to one whole-document task from your existing chat message", RunE: craftAgentStart}
	start.Flags().String("chat-id", "", "Chat containing your original instruction")
	start.Flags().String("message-id", "", "Your original human message in that chat")
	start.Flags().Bool("whole-document", false, "Explicitly consent to whole-document native edits for this task")
	_ = start.MarkFlagRequired("chat-id")
	_ = start.MarkFlagRequired("message-id")
	task.AddCommand(start)
	task.AddCommand(&cobra.Command{Use: "instruction <agent> <text>", Args: cobra.ExactArgs(2), Short: "Record a task instruction in a new private chat, without an agent turn", RunE: craftAgentInstruction})
	task.AddCommand(&cobra.Command{Use: "status <page> <work-id>", Args: cobra.ExactArgs(2), Short: "Read your task's actual host outcomes under current Page rights", RunE: craftAgentStatus})
	task.AddCommand(&cobra.Command{Use: "stop <page>", Args: cobra.ExactArgs(1), Short: "Revoke your current native agent's Page mandate and request its stop", RunE: craftAgentStop})
	return task
}
func craftAgentIdentifier(value string) bool {
	if !mooring.ValidWireIdentifier(value) || strings.TrimSpace(value) == "" || value == "." || value == ".." {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func craftAgentClient(cmd *cobra.Command) (*cli.Client, error) {
	c, err := pageClient()
	if err != nil {
		return nil, err
	}
	c = c.WithContext(cmd.Context())
	// Even a same-origin redirect must not replay explicit task consent or stop.
	httpClient := *c.HTTPClient
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c.HTTPClient = &httpClient
	return c, nil
}

// craftInstructionTimeout bounds a send that only saves one message.
const craftInstructionTimeout = 30 * time.Second

// craftInstructionStream notes the id the server saved the instruction under.
// The stream's read loop runs on its own goroutine.
type craftInstructionStream struct {
	*cli.WSClient
	messageID atomic.Pointer[string]
}

func (s *craftInstructionStream) ReadMessage() (*cli.WSMessage, error) {
	message, err := s.WSClient.ReadMessage()
	if err != nil {
		return nil, err
	}
	if event, parseErr := cli.ParseChatEvent(message); parseErr == nil && event != nil && event.Type == "user_message" {
		if id := metaStr(eventMeta(event.Metadata), "message_id"); id != "" {
			s.messageID.Store(&id)
		}
	}
	return message, nil
}

// craftAgentInstruction is the browser's start up to the task request: a new
// private chat and a message that starts no agent turn, so the text reaches no
// ordinary run, agent log or journal entry. `task start` binds only such a message.
func craftAgentInstruction(cmd *cobra.Command, args []string) error {
	if strings.TrimSpace(args[1]) == "" {
		return fmt.Errorf("native agent instruction is empty")
	}
	c, err := craftAgentClient(cmd)
	if err != nil {
		return err
	}
	agent, err := resolveAgentID(c, args[0])
	if err != nil {
		return err
	}
	response, err := c.Post("/api/v1/agents/"+url.PathEscape(agent)+"/chats", ChatCreationBody())
	if err != nil {
		return fmt.Errorf("create chat: %w", err)
	}
	if err = cli.CheckError(response); err != nil {
		return err
	}
	var chat struct {
		ID string `json:"id" yaml:"id"`
	}
	if err = cli.ReadJSON(response, &chat); err != nil {
		return err
	}
	restricted, err := restrictedChatProfile(c, chat.ID)
	if err != nil {
		return err
	}
	if restricted {
		return fmt.Errorf("native agent tasks require a trusted human chat")
	}
	token, err := cli.WSTokenFromServer(c)
	if err != nil {
		return fmt.Errorf("get WS token: %w", err)
	}
	ws, err := cli.NewWSClient(streamServerURL(), token)
	if err != nil {
		return err
	}
	defer ws.Close()
	if err = ws.Subscribe("session:" + chat.ID); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	if err = ws.SendMessageWithMetadata("agent:"+agent, chat.ID, args[1], 0, map[string]any{chatbridge.NativeCraftInstructionMetadataKey: true}); err != nil {
		return fmt.Errorf("send instruction: %w", err)
	}
	stream := &craftInstructionStream{WSClient: ws}
	result := collectAgentStream(stream, craftInstructionTimeout)
	id := stream.messageID.Load()
	if !result.GotDone || id == nil {
		return fmt.Errorf("native agent instruction was not confirmed as saved; do not start a task from chat %s", chat.ID)
	}
	raw, err := json.Marshal(map[string]string{"chat_id": chat.ID, "message_id": *id})
	if err != nil {
		return err
	}
	return craftEmit(cmd, raw, "")
}
func craftAgentStart(cmd *cobra.Command, args []string) error {
	chat, _ := cmd.Flags().GetString("chat-id")
	message, _ := cmd.Flags().GetString("message-id")
	consent, _ := cmd.Flags().GetBool("whole-document")
	if !cmd.Flags().Changed("whole-document") || !consent {
		return fmt.Errorf("native agent start requires explicit --whole-document=true consent")
	}
	if !craftAgentIdentifier(args[0]) || !craftAgentIdentifier(chat) || !craftAgentIdentifier(message) {
		return fmt.Errorf("invalid native agent Page, chat or message identifier")
	}
	c, err := craftAgentClient(cmd)
	if err != nil {
		return err
	}
	body := struct {
		ChatID        string `json:"chat_id" yaml:"chat_id"`
		MessageID     string `json:"message_id" yaml:"message_id"`
		WholeDocument bool   `json:"whole_document" yaml:"whole_document"`
	}{chat, message, consent}
	response, err := c.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/craft/tasks", body)
	return craftAgentResponse(cmd, response, err)
}
func craftAgentStatus(cmd *cobra.Command, args []string) error {
	if !craftAgentIdentifier(args[0]) || !craftAgentIdentifier(args[1]) {
		return fmt.Errorf("invalid native agent Page or work identifier")
	}
	c, err := craftAgentClient(cmd)
	if err != nil {
		return err
	}
	response, err := c.Get("/api/v1/pages/" + pagePathEscape(args[0]) + "/craft/tasks/" + pagePathEscape(args[1]))
	return craftAgentResponse(cmd, response, err)
}
func craftAgentStop(cmd *cobra.Command, args []string) error {
	if !craftAgentIdentifier(args[0]) {
		return fmt.Errorf("invalid native agent Page identifier")
	}
	c, err := craftAgentClient(cmd)
	if err != nil {
		return err
	}
	response, err := c.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/craft/tasks/cancel", struct{}{})
	return craftAgentResponse(cmd, response, err)
}
