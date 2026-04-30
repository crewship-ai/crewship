package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/crewship-ai/crewship/internal/cli"
)

// askCmd is a low-friction one-shot prompt against a configured default
// agent. It exists so common shell workflows like
//
//	git diff | crewship ask "review this"
//	crewship ask "summarize today's runs" --with-cmd "crewship run list"
//
// don't require the user to remember an agent slug. The agent is resolved
// from --agent > config.default_agent > error.
var askCmd = &cobra.Command{
	Use:   "ask [prompt]",
	Short: "Ask the default agent a quick question",
	Long: `Ask the default agent a one-shot question and stream the response.

The agent is resolved from --agent flag, then the 'default-agent' config key.
Set it once with:

  crewship config set default-agent <slug>

Examples:
  crewship ask "what time is it?"
  git diff | crewship ask "review this change"
  crewship ask "summarize" --with-file notes.md
  crewship ask --agent viktor "explain how the journal works"
  crewship ask --prompt @-                 # full prompt from stdin`,
	Args: cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		agentSlug, _ := cmd.Flags().GetString("agent")
		if agentSlug == "" && cliCfg != nil {
			agentSlug = cliCfg.DefaultAgent
		}

		client := newAPIClient()

		// No default agent: open an interactive picker on a TTY, error in
		// non-TTY mode (CI / scripts can't satisfy a prompt). Saves the
		// pick as the default if the user opts in, so the next run is
		// frictionless.
		if agentSlug == "" {
			if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stderr.Fd())) {
				return fmt.Errorf("no default agent set. Use --agent <slug> or run 'crewship config set default-agent <slug>'")
			}
			picked, save, err := pickAgentInteractive(client)
			if err != nil {
				return err
			}
			agentSlug = picked
			if save {
				if cfg, _ := cli.LoadConfig(); cfg != nil {
					cfg.DefaultAgent = agentSlug
					_ = cli.SaveConfig(cfg)
					fmt.Fprintf(os.Stderr, "%s[saved default-agent=%s]%s\n", cli.Dim, agentSlug, cli.Reset)
				}
			}
		}

		agentID, err := resolveAgentID(client, agentSlug)
		if err != nil {
			return err
		}

		flagPrompt, _ := cmd.Flags().GetString("prompt")
		withGitDiff, _ := cmd.Flags().GetBool("with-git-diff")
		withGitDiffStaged, _ := cmd.Flags().GetBool("with-git-staged")
		withGitLog, _ := cmd.Flags().GetBool("with-git-log")
		withGitStatus, _ := cmd.Flags().GetBool("with-git-status")
		withFiles, _ := cmd.Flags().GetStringSlice("with-file")
		withCmds, _ := cmd.Flags().GetStringSlice("with-cmd")

		prompt, err := cli.BuildPrompt(cli.PromptOptions{
			Positional:        args,
			PromptFlag:        flagPrompt,
			AutoStdin:         true,
			WithGitDiff:       withGitDiff,
			WithGitDiffStaged: withGitDiffStaged,
			WithGitLog:        withGitLog,
			WithGitStatus:     withGitStatus,
			WithFiles:         withFiles,
			WithCmds:          withCmds,
		})
		if err != nil {
			return err
		}
		if strings.TrimSpace(prompt) == "" {
			return fmt.Errorf("prompt is required (positional, --prompt, stdin pipe, or --with-* flag)")
		}

		quiet, _ := cmd.Flags().GetBool("quiet")
		noStream, _ := cmd.Flags().GetBool("no-stream")
		timeoutSecs, _ := cmd.Flags().GetInt("timeout")
		if timeoutSecs > 0 {
			client.HTTPClient.Timeout = time.Duration(timeoutSecs) * time.Second
		}

		// Create one-shot chat (origin=CLI keeps web UI sidebar tagged correctly).
		resp, err := client.Post("/api/v1/agents/"+agentID+"/chats", map[string]string{
			"mode":   "CHAT",
			"origin": "CLI",
		})
		if err != nil {
			return fmt.Errorf("create chat: %w", err)
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var chatResult struct {
			ID string `json:"id"`
		}
		if err := cli.ReadJSON(resp, &chatResult); err != nil {
			return err
		}

		wsToken, err := cli.WSTokenFromServer(client)
		if err != nil {
			return fmt.Errorf("get WS token: %w", err)
		}
		server := cli.ResolveServer(flagServer, cliCfg)

		md := resolveMarkdownFromCmd(cmd)
		saveFile, err := openSaveFile(cmd)
		if err != nil {
			return err
		}
		if saveFile != nil {
			defer saveFile.Close()
		}

		if noStream {
			return runNoStream(server, wsToken, agentID, chatResult.ID, prompt, quiet, md, saveFile)
		}
		return runStream(server, wsToken, agentID, agentSlug, chatResult.ID, prompt, quiet, md, saveFile)
	},
}

// pickAgentInteractive shows a huh-based picker over the agents the caller
// can see. Returns the chosen slug and whether the user wants it saved as
// the default-agent config key.
//
// Errors fall through:
//   - HTTP failure → returned (no agent selected).
//   - Empty agent list → "no agents available" error so the user knows the
//     workspace truly has none rather than thinking the picker broke.
//   - User aborted (Ctrl-C) → "aborted" error matching confirmAction.
func pickAgentInteractive(client *cli.Client) (string, bool, error) {
	resp, err := client.Get("/api/v1/agents")
	if err != nil {
		return "", false, err
	}
	if err := cli.CheckError(resp); err != nil {
		return "", false, err
	}
	var agents []struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	}
	if err := cli.ReadJSON(resp, &agents); err != nil {
		return "", false, err
	}
	if len(agents) == 0 {
		return "", false, fmt.Errorf("no agents available in this workspace")
	}

	options := make([]huh.Option[string], 0, len(agents))
	for _, a := range agents {
		if a.Slug == "" {
			continue
		}
		label := a.Slug
		if a.Name != "" {
			label = fmt.Sprintf("%s — %s", a.Slug, a.Name)
		}
		options = append(options, huh.NewOption(label, a.Slug))
	}

	var picked string
	if err := huh.NewSelect[string]().
		Title("Pick an agent").
		Options(options...).
		Value(&picked).
		Run(); err != nil {
		return "", false, errors.New("aborted")
	}

	var save bool
	// Best-effort save prompt — if it errors (e.g. piped stdin), default to
	// "no, don't save" so the run still proceeds.
	_ = huh.NewConfirm().
		Title(fmt.Sprintf("Save %q as the default agent?", picked)).
		Affirmative("Yes, save").
		Negative("No, ask again next time").
		Value(&save).
		Run()

	return picked, save, nil
}

func init() {
	askCmd.Flags().String("agent", "", "Agent slug or ID (overrides default-agent config)")
	askCmd.Flags().StringP("prompt", "p", "", "Prompt text, @file, or @- for stdin")
	askCmd.Flags().BoolP("quiet", "q", false, "Only output text, no meta info")
	askCmd.Flags().Bool("no-stream", false, "Wait for completion, show only result")
	askCmd.Flags().Int("timeout", 0, "Timeout in seconds (0 = no timeout)")
	askCmd.Flags().Bool("with-git-diff", false, "Append `git diff` as context")
	askCmd.Flags().Bool("with-git-staged", false, "Append `git diff --staged` as context")
	askCmd.Flags().Bool("with-git-log", false, "Append last 20 commits as context")
	askCmd.Flags().Bool("with-git-status", false, "Append `git status -s` as context")
	askCmd.Flags().StringSlice("with-file", nil, "Append file content(s) as context (repeatable)")
	askCmd.Flags().StringSlice("with-cmd", nil, "Append shell command output as context (repeatable)")
	askCmd.Flags().Bool("markdown", false, "Render markdown ANSI styling (overrides config)")
	askCmd.Flags().Bool("no-markdown", false, "Disable markdown ANSI styling (overrides config)")
	askCmd.Flags().String("save", "", "Also write the agent's text response (no ANSI) to this path")
}
