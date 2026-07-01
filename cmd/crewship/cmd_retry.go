package main

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// retryCmd re-runs a previous run by fetching its first user prompt from
// chat messages and starting a fresh run on the same agent. Useful for
// iteration loops:
//
//	crewship history --status failed
//	crewship retry <run-id>           # tweak parameters via flags
//	crewship retry <run-id> --new-prompt "be more specific"
//
// `--continue` flips behaviour: instead of starting a fresh chat, the new
// message is appended to the original chat (true continuation, with the
// original conversation history preserved).
var retryCmd = &cobra.Command{
	Use:   "retry <run-id>",
	Short: "Re-run a previous run with the same agent + prompt",
	Long: `Look up a prior run, recover its first user prompt, and start a new run
on the same agent.

Examples:
  crewship retry r_abc                       # same agent, same prompt, new chat
  crewship retry r_abc --continue            # append to the same chat
  crewship retry r_abc --new-prompt "..."    # same agent, different prompt
  crewship retry r_abc --quiet`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		runID := args[0]
		client := newAPIClient()

		runMeta, err := fetchRun(client, runID)
		if err != nil {
			return err
		}
		if runMeta.AgentID == "" {
			return fmt.Errorf("run %s has no agent_id", runID)
		}

		newPrompt, _ := cmd.Flags().GetString("new-prompt")
		prompt := newPrompt
		if prompt == "" {
			if runMeta.ChatID == "" {
				return fmt.Errorf("run %s has no chat_id; pass --new-prompt to retry", runID)
			}
			p := fetchFirstUserPrompt(client, runMeta.ChatID)
			if p == "" {
				return fmt.Errorf("could not recover original prompt for run %s; pass --new-prompt", runID)
			}
			prompt = p
		}

		quiet, _ := cmd.Flags().GetBool("quiet")
		noStream, _ := cmd.Flags().GetBool("no-stream")
		continueChat, _ := cmd.Flags().GetBool("continue")
		md := resolveMarkdownFromCmd(cmd)
		saveFile, err := openSaveFile(cmd)
		if err != nil {
			return err
		}
		if saveFile != nil {
			defer saveFile.Close()
		}

		chatID := ""
		if continueChat {
			chatID = runMeta.ChatID
			if chatID == "" {
				return fmt.Errorf("--continue requires the original run to have a chat_id")
			}
		} else {
			resp, err := client.Post("/api/v1/agents/"+runMeta.AgentID+"/chats", map[string]string{
				"mode":   "CHAT",
				"origin": "CLI",
			})
			if err != nil {
				return fmt.Errorf("create chat: %w", err)
			}
			if err := cli.CheckError(resp); err != nil {
				return err
			}
			var out struct {
				ID string `json:"id"`
			}
			if err := cli.ReadJSON(resp, &out); err != nil {
				return err
			}
			chatID = out.ID
		}

		wsToken, err := cli.WSTokenFromServer(client)
		if err != nil {
			return fmt.Errorf("get WS token: %w", err)
		}
		server := cli.ResolveServer(flagServer, cliCfg)

		agentSlug := runMeta.AgentSlug
		if agentSlug == "" {
			agentSlug = runMeta.AgentID
		}

		if !quiet {
			summary := truncateString(strings.ReplaceAll(prompt, "\n", " "), 60)
			fmt.Fprintf(os.Stderr, "%s[retry %s → %s]%s %q\n",
				cli.Dim, runID, agentSlug, cli.Reset, summary)
		}

		if noStream {
			return runNoStream(server, wsToken, runMeta.AgentID, chatID, prompt, quiet, md, saveFile, 0)
		}
		return runStream(server, wsToken, runMeta.AgentID, agentSlug, chatID, prompt, quiet, md, saveFile, 0)
	},
}

// runMetadata is what we need from a run to retry it: agent + chat IDs, and
// agent slug for friendly logging. Returned by fetchRun.
type runMetadata struct {
	AgentID   string
	AgentSlug string
	ChatID    string
	// Model is the model the run actually resolved to (server-side ground
	// truth), surfaced by `crewship inspect` so an operator can confirm the
	// tier the subscription served. Empty when the server didn't record one.
	Model string
}

// fetchRun looks up a run by ID via /api/v1/runs and extracts the fields we
// need. The list endpoint is used (not a per-run endpoint) because that's
// what already exists; we filter client-side to keep changes scoped to the
// CLI. If the run is not on the first page, the user can pass --limit via
// `crewship history` first to find the ID — retry itself targets a known ID.
func fetchRun(client *cli.Client, runID string) (runMetadata, error) {
	q := url.Values{}
	q.Set("limit", "100")
	resp, err := client.Get("/api/v1/runs?" + q.Encode())
	if err != nil {
		return runMetadata{}, err
	}
	if err := cli.CheckError(resp); err != nil {
		return runMetadata{}, err
	}
	var body struct {
		Data []struct {
			ID        string  `json:"id"`
			AgentID   string  `json:"agent_id"`
			AgentSlug *string `json:"agent_slug"`
			ChatID    *string `json:"chat_id"`
			Model     *string `json:"model"`
		} `json:"data"`
	}
	if err := cli.ReadJSON(resp, &body); err != nil {
		return runMetadata{}, err
	}
	for _, r := range body.Data {
		if r.ID != runID {
			continue
		}
		out := runMetadata{AgentID: r.AgentID}
		if r.AgentSlug != nil {
			out.AgentSlug = *r.AgentSlug
		}
		if r.ChatID != nil {
			out.ChatID = *r.ChatID
		}
		if r.Model != nil {
			out.Model = *r.Model
		}
		return out, nil
	}
	return runMetadata{}, fmt.Errorf("run %s not found in last 100 runs", runID)
}

func init() {
	retryCmd.Flags().String("new-prompt", "", "Override the original prompt with this text")
	retryCmd.Flags().Bool("continue", false, "Append to the original chat instead of starting a new one")
	retryCmd.Flags().BoolP("quiet", "q", false, "Only output text, no meta info")
	retryCmd.Flags().Bool("no-stream", false, "Wait for completion, show only result")
	retryCmd.Flags().Bool("markdown", false, "Render markdown ANSI styling (overrides config)")
	retryCmd.Flags().Bool("no-markdown", false, "Disable markdown ANSI styling (overrides config)")
	retryCmd.Flags().String("save", "", "Also write the agent's text response (no ANSI) to this path")
}
