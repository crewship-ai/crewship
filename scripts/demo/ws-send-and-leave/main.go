// ws-send-and-leave starts a direct agent chat without subscribing to its
// session channel. It exercises the unread reply → Inbox path that a normal
// `crewship ask` cannot test because ask watches the reply live.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
)

func main() {
	server := flag.String("server", "", "Crewship server URL")
	agentSlug := flag.String("agent", "morgan", "agent slug")
	prompt := flag.String("prompt", "", "message to send")
	flag.Parse()
	if *server == "" || *prompt == "" {
		fmt.Fprintln(os.Stderr, "--server and --prompt are required")
		os.Exit(2)
	}
	if err := run(*server, *agentSlug, *prompt); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(server, agentSlug, prompt string) error {
	if wd, err := os.Getwd(); err == nil {
		cli.SetWorkingDir(wd)
	}
	cfg, err := cli.LoadConfig()
	if err != nil {
		return err
	}
	cfg = cfg.WithActiveProfile(os.Getenv("CREWSHIP_PROFILE"))
	if cfg.Token == "" {
		return fmt.Errorf("no CLI login for the active Crewship profile")
	}
	client := cli.NewClient(server, cfg.Token, cfg.Workspace)
	resp, err := client.Get("/api/v1/agents")
	if err != nil {
		return err
	}
	if err := cli.CheckError(resp); err != nil {
		return err
	}
	var agents []struct{ ID, Slug string }
	if err := cli.ReadJSON(resp, &agents); err != nil {
		return err
	}
	var agentID string
	for _, agent := range agents {
		if agent.Slug == agentSlug {
			agentID = agent.ID
			break
		}
	}
	if agentID == "" {
		return fmt.Errorf("agent %q not found", agentSlug)
	}
	resp, err = client.Post("/api/v1/agents/"+agentID+"/chats", map[string]any{"mode": "CHAT", "origin": "CLI"})
	if err != nil {
		return err
	}
	if err := cli.CheckError(resp); err != nil {
		return err
	}
	var chat struct{ ID string }
	if err := cli.ReadJSON(resp, &chat); err != nil {
		return err
	}
	if chat.ID == "" {
		return fmt.Errorf("chat creation returned no ID")
	}
	// Print the ID the moment the chat exists, not on the way out: the
	// driving script captures stdout and registers its own `chat delete`
	// cleanup from that value, so a failure in the WebSocket steps below must
	// not take the ID with it. This stays the ONLY stdout line.
	fmt.Println(chat.ID)

	wsToken, err := cli.WSTokenFromServer(client)
	if err != nil {
		deleteChatBestEffort(client, agentID, chat.ID)
		return err
	}
	ws, err := cli.NewWSClient(server, wsToken)
	if err != nil {
		deleteChatBestEffort(client, agentID, chat.ID)
		return err
	}
	if err := ws.SendMessage("agent:"+agentID, chat.ID, prompt); err != nil {
		_ = ws.Close()
		deleteChatBestEffort(client, agentID, chat.ID)
		return err
	}
	// The server handles the send asynchronously. Leave the socket open just
	// long enough for it to accept the frame; no session subscription exists.
	time.Sleep(time.Second)
	_ = ws.Close()
	return nil
}

// deleteChatBestEffort cleans up the chat on the failure paths after it was
// created: a chat whose message never went out is an orphan the demo script
// cannot assert on, and its projected Inbox row would linger. The delete is
// best-effort on purpose — the original failure is the error worth returning,
// so this one only surfaces on stderr.
func deleteChatBestEffort(client *cli.Client, agentID, chatID string) {
	resp, err := client.Delete("/api/v1/agents/" + agentID + "/chats/" + chatID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "best-effort delete of chat %s failed: %v\n", chatID, err)
		return
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		fmt.Fprintf(os.Stderr, "best-effort delete of chat %s failed: %v\n", chatID, err)
	}
}
