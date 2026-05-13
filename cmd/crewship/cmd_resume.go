package main

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/crewship-ai/crewship/internal/cli"
)

// resumeCmd picks up an existing session/run and continues it.
//
// Three resolution paths:
//
//	crewship resume                  no arg → interactive picker over recent CLI sessions
//	crewship resume <chat-id>        continue an explicit chat
//	crewship resume <run-id>         look up the run, find its chat, continue
//	crewship resume <pr-url>         find the session that produced the PR
//
// "Continue" means: re-enter the run flow against the agent that owns
// the chat, with --chat <id> threaded through so the new message goes
// into the existing thread.
var resumeCmd = &cobra.Command{
	Use:   "resume [chat-id | run-id | pr-url]",
	Short: "Pick up an existing session",
	Long: `Resume a previous CLI session by chat-id, run-id, or pull-request URL.
With no argument, opens an interactive picker over the 10 most recent
CLI sessions in the current workspace.

Examples:
  crewship resume
  crewship resume c_abc123
  crewship resume r_xyz789
  crewship resume https://github.com/foo/bar/pull/42`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		var chatID, agentSlug string

		switch {
		case len(args) == 0:
			// Interactive: list recent CLI-origin chats, let user pick.
			id, slug, err := pickRecentChat(client)
			if err != nil {
				return err
			}
			chatID, agentSlug = id, slug
		default:
			arg := strings.TrimSpace(args[0])
			if owner, repo, num, ok := cli.ParsePRURL(arg); ok {
				id, slug, err := findChatForPR(client, owner, repo, num)
				if err != nil {
					return err
				}
				chatID, agentSlug = id, slug
			} else if strings.HasPrefix(arg, "r_") || strings.HasPrefix(arg, "run_") {
				// Run id — resolve to its chat.
				detail, err := client.GetRun(cmd.Context(), arg)
				if err != nil {
					return err
				}
				if detail.ChatID == nil || *detail.ChatID == "" {
					return fmt.Errorf("run %s has no associated chat to resume", arg)
				}
				chatID = *detail.ChatID
				if detail.AgentSlug != nil {
					agentSlug = *detail.AgentSlug
				}
			} else {
				// Assume chat id.
				chatID = arg
			}
		}

		if chatID == "" {
			return fmt.Errorf("could not resolve a session to resume")
		}

		// If we don't know the agent slug yet, look it up via /chats/{id}.
		if agentSlug == "" {
			var chat struct {
				AgentSlug string `json:"agent_slug"`
				AgentID   string `json:"agent_id"`
			}
			if err := getJSON(client, "/api/v1/chats/"+url.PathEscape(chatID), &chat); err == nil {
				agentSlug = chat.AgentSlug
				if agentSlug == "" {
					agentSlug = chat.AgentID
				}
			}
		}
		if agentSlug == "" {
			return fmt.Errorf("could not determine agent for chat %s", chatID)
		}

		fmt.Fprintf(os.Stderr, "%s[resume]%s chat=%s agent=%s\n",
			cli.Dim, cli.Reset, chatID, agentSlug)

		// Dispatch into runCmd with --chat <id>, --interactive.
		_ = runCmd.Flags().Set("chat", chatID)
		_ = runCmd.Flags().Set("interactive", "true")
		if runCmd.RunE == nil {
			return fmt.Errorf("internal: run command has no RunE")
		}
		return runCmd.RunE(runCmd, []string{agentSlug})
	},
}

// pickRecentChat lists the user's recent CLI-origin chats with huh.
// Non-TTY → error rather than picking arbitrarily.
func pickRecentChat(client *cli.Client) (chatID, agentSlug string, err error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stderr.Fd())) {
		return "", "", fmt.Errorf("interactive picker requires a TTY; pass a chat-id or run-id")
	}
	q := url.Values{}
	q.Set("origin", "CLI")
	q.Set("limit", "10")
	var body struct {
		Data []struct {
			ID        string `json:"id"`
			AgentSlug string `json:"agent_slug"`
			Title     string `json:"title"`
			UpdatedAt string `json:"updated_at"`
		} `json:"data"`
	}
	// Try /chats; fall back to /runs if the endpoint shape differs.
	if err := getJSON(client, "/api/v1/chats?"+q.Encode(), &body); err != nil {
		var runs struct {
			Data []struct {
				ID        string  `json:"id"`
				AgentSlug *string `json:"agent_slug"`
				ChatID    *string `json:"chat_id"`
				CreatedAt string  `json:"created_at"`
			} `json:"data"`
		}
		if err2 := getJSON(client, "/api/v1/runs?limit=10", &runs); err2 != nil {
			return "", "", fmt.Errorf("list recent: %w", err)
		}
		for _, r := range runs.Data {
			if r.ChatID == nil || *r.ChatID == "" {
				continue
			}
			body.Data = append(body.Data, struct {
				ID        string `json:"id"`
				AgentSlug string `json:"agent_slug"`
				Title     string `json:"title"`
				UpdatedAt string `json:"updated_at"`
			}{
				ID:        *r.ChatID,
				AgentSlug: deref(r.AgentSlug),
				Title:     "run " + r.ID,
				UpdatedAt: r.CreatedAt,
			})
		}
	}
	if len(body.Data) == 0 {
		return "", "", fmt.Errorf("no recent sessions to resume")
	}
	type pick struct {
		ID    string
		Slug  string
		Label string
	}
	picks := make([]pick, 0, len(body.Data))
	options := make([]huh.Option[string], 0, len(body.Data))
	for _, c := range body.Data {
		label := c.ID
		if c.Title != "" {
			label = fmt.Sprintf("%s — %s", c.AgentSlug, c.Title)
		}
		if c.UpdatedAt != "" {
			label = label + "  (" + c.UpdatedAt + ")"
		}
		picks = append(picks, pick{ID: c.ID, Slug: c.AgentSlug, Label: label})
		options = append(options, huh.NewOption(label, c.ID))
	}
	var pickedID string
	if err := huh.NewSelect[string]().
		Title("Resume which session?").
		Options(options...).
		Value(&pickedID).
		Run(); err != nil {
		return "", "", fmt.Errorf("aborted")
	}
	for _, p := range picks {
		if p.ID == pickedID {
			return p.ID, p.Slug, nil
		}
	}
	return pickedID, "", nil
}

// findChatForPR looks up the session that produced a PR — searches the
// journal for entries referencing the PR URL/number.
//
// The endpoint shape varies (journal supports a `query` parameter for
// substring search); on miss we return a clear error pointing the user
// at the manual chat-id form. This is best-effort — the link only
// exists if an agent journaled the PR creation.
func findChatForPR(client *cli.Client, owner, repo string, num int) (chatID, agentSlug string, err error) {
	needle := fmt.Sprintf("%s/%s#%d", owner, repo, num)
	q := url.Values{}
	q.Set("query", needle)
	q.Set("limit", "5")
	var body struct {
		Entries []struct {
			TraceID string `json:"trace_id"`
			ChatID  string `json:"chat_id"`
			AgentID string `json:"agent_id"`
		} `json:"entries"`
	}
	if err := getJSON(client, "/api/v1/journal?"+q.Encode(), &body); err != nil {
		return "", "", fmt.Errorf("journal search: %w (try a chat-id instead)", err)
	}
	for _, e := range body.Entries {
		if e.ChatID != "" {
			return e.ChatID, "", nil
		}
		// trace_id-only entries would need a follow-up GetRun lookup;
		// we skip that to keep this best-effort path tight. The user
		// can fall back to passing a chat-id directly.
	}
	return "", "", fmt.Errorf("no session found for PR %s — pass a chat-id directly", needle)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func init() {
	rootCmd.AddCommand(resumeCmd)
}
