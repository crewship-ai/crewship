package main

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
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
//	crewship resume                  no arg → interactive picker over recent sessions
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
	Long: `Resume a previous session by chat-id, run-id, or pull-request URL.
With no argument, opens an interactive picker over the 10 most recently
active sessions in the current workspace — every session, whatever
started it, not only the ones this CLI started.

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
			// Interactive: list the workspace's recently active chats and
			// let the user pick. Not filtered by origin — the runs list
			// this reads has no such filter, so a session started in the
			// web UI is offered here too, and --help says so.
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

		// If we don't know the agent slug yet, ask which agent owns the
		// chat. A chat is only addressable under its agent, so this is the
		// agents walk — the flat GET /api/v1/chats/{id} this used to call
		// has never been a route and 404'd on every resume (#2086).
		if agentSlug == "" {
			agentID, slug, err := lookupChatAgent(client, chatID)
			if err != nil {
				return fmt.Errorf("could not determine agent for chat %s: %w", chatID, err)
			}
			agentSlug = slug
			if agentSlug == "" {
				agentSlug = agentID
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

// resumeSessionCount is how many sessions the picker offers.
const resumeSessionCount = 10

// runsPageMax is the largest `limit` GET /api/v1/runs honours. Out-of-range is
// not an error there: `if limit < 1 || limit > 100 { limit = 50 }`
// (internal/api/runs.go), so asking for 200 gets 50 — a SMALLER page than
// asking for 100. That inverts the over-fetch recentSessions relies on, and it
// does it silently, at whatever value of resumeSessionCount someone picks
// next. Clamping here keeps the constant above safe to raise.
const runsPageMax = 100

// recentSession is one row of the picker: a chat that can be resumed, with
// the agent that owns it.
type recentSession struct {
	ChatID    string
	AgentSlug string
	Title     string
	UpdatedAt string
}

// recentSessions lists the workspace's most recently active resumable
// sessions, newest first.
//
// It reads the RUNS list, not a chat list, because there is no
// workspace-wide chat list to read: a chat is addressable only under the
// agent that owns it (GET /api/v1/agents/{agentId}/chats), so a flat listing
// would be an N+1 walk over every agent. GET /api/v1/runs is workspace-
// scoped, ordered started_at DESC, and each row already carries chat_id and
// agent_slug — exactly what the picker shows. (This was already the only
// code path that ever ran: the flat GET /api/v1/chats it used to try first
// has never been registered, so every resume 404'd into this fallback —
// #2086.)
//
// One chat can own several runs — resuming a session twice is two runs of
// one chat — so ask for more runs than sessions and dedupe, keeping the
// newest run per chat. The over-fetch is capped at runsPageMax; past that the
// server would hand back FEWER rows, not more.
//
// The list is not filtered by origin. There is nothing to filter it by here —
// `origin` is a property of the chat, and this reads runs — so a session
// started from the web UI is offered alongside one started from this CLI.
// resumeCmd's --help says so; keep the two in step.
func recentSessions(client *cli.Client, want int) ([]recentSession, error) {
	var runs struct {
		Data []struct {
			ID        string  `json:"id"`
			AgentSlug *string `json:"agent_slug"`
			ChatID    *string `json:"chat_id"`
			CreatedAt string  `json:"created_at"`
		} `json:"data"`
	}
	q := url.Values{}
	q.Set("limit", strconv.Itoa(min(want*5, runsPageMax)))
	if err := getJSON(client, "/api/v1/runs?"+q.Encode(), &runs); err != nil {
		return nil, fmt.Errorf("list recent sessions: %w", err)
	}
	seen := map[string]bool{}
	out := make([]recentSession, 0, want)
	for _, r := range runs.Data {
		if r.ChatID == nil || *r.ChatID == "" || seen[*r.ChatID] {
			continue
		}
		seen[*r.ChatID] = true
		out = append(out, recentSession{
			ChatID:    *r.ChatID,
			AgentSlug: deref(r.AgentSlug),
			Title:     "run " + r.ID,
			UpdatedAt: r.CreatedAt,
		})
		if len(out) == want {
			break
		}
	}
	return out, nil
}

// pickRecentChat lists the user's recent sessions with huh.
// Non-TTY → error rather than picking arbitrarily.
func pickRecentChat(client *cli.Client) (chatID, agentSlug string, err error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stderr.Fd())) {
		return "", "", fmt.Errorf("interactive picker requires a TTY; pass a chat-id or run-id")
	}
	sessions, err := recentSessions(client, resumeSessionCount)
	if err != nil {
		return "", "", err
	}
	if len(sessions) == 0 {
		return "", "", fmt.Errorf("no recent sessions to resume")
	}
	options := make([]huh.Option[string], 0, len(sessions))
	for _, s := range sessions {
		label := s.ChatID
		if s.Title != "" {
			label = fmt.Sprintf("%s — %s", s.AgentSlug, s.Title)
		}
		if s.UpdatedAt != "" {
			label = label + "  (" + s.UpdatedAt + ")"
		}
		options = append(options, huh.NewOption(label, s.ChatID))
	}
	var pickedID string
	if err := huh.NewSelect[string]().
		Title("Resume which session?").
		Options(options...).
		Value(&pickedID).
		Run(); err != nil {
		// Wrap the huh error so the caller can distinguish Ctrl-C
		// (terminal aborted) from a render failure (TTY changed mid-
		// run, missing terminfo) instead of seeing a flat "aborted".
		return "", "", fmt.Errorf("picker aborted: %w", err)
	}
	for _, s := range sessions {
		if s.ChatID == pickedID {
			return s.ChatID, s.AgentSlug, nil
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
