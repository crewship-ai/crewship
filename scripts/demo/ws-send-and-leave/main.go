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
	wsToken, err := cli.WSTokenFromServer(client)
	if err != nil {
		return err
	}
	ws, err := cli.NewWSClient(server, wsToken)
	if err != nil {
		return err
	}
	if err := ws.SendMessage("agent:"+agentID, chat.ID, prompt); err != nil {
		_ = ws.Close()
		return err
	}
	// The server handles the send asynchronously. Leave the socket open just
	// long enough for it to accept the frame; no session subscription exists.
	time.Sleep(time.Second)
	_ = ws.Close()
	fmt.Println(chat.ID)
	return nil
}
