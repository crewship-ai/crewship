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
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		estimate, _ := cmd.Flags().GetBool("estimate")
		offline := dryRun || estimate

		// Skip auth + agent resolution for offline modes so users can compose
		// and inspect prompts (or token estimates) without a login or server.
		if !offline {
			if err := requireAuth(); err != nil {
				return err
			}
			if err := requireWorkspace(); err != nil {
				return err
			}
		}

		agentFlag, _ := cmd.Flags().GetString("agent")
		fanoutAgents, _ := cmd.Flags().GetStringSlice("agents")
		// Single-agent default only matters when --agents is not given —
		// otherwise the fan-out path resolves each agent in its own loop and
		// neither the picker nor the "no default" error should fire.
		var agentSlug string
		if len(fanoutAgents) == 0 {
			agentSlug = cli.ResolveDefaultAgent(agentFlag, cliCfg)
		}

		var client *cli.Client
		if !offline {
			client = newAPIClient()
		}

		// No default agent: open an interactive picker on a TTY, error in
		// non-TTY mode (CI / scripts can't satisfy a prompt). Saves the
		// pick as the default if the user opts in, so the next run is
		// frictionless. Skipped entirely in offline modes (dry-run/estimate)
		// and in fan-out mode (--agents resolves each slug itself).
		if !offline && len(fanoutAgents) == 0 && agentSlug == "" {
			if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stderr.Fd())) {
				return fmt.Errorf("no default agent set. Use --agent <slug>, --agents <list>, or run 'crewship config set default-agent <slug>'")
			}
			picked, save, err := pickAgentInteractive(client)
			if err != nil {
				return err
			}
			agentSlug = picked
			if save {
				// Only print the saved-default banner after SaveConfig actually
				// returns nil — disk-full / permission failures otherwise leave
				// the user thinking the choice was persisted when it wasn't.
				if cfg, _ := cli.LoadConfig(); cfg != nil {
					cfg.DefaultAgent = agentSlug
					if err := cli.SaveConfig(cfg); err != nil {
						fmt.Fprintf(os.Stderr, "%s[warn]%s could not save default-agent=%s: %v\n",
							cli.Yellow, cli.Reset, agentSlug, err)
					} else {
						fmt.Fprintf(os.Stderr, "%s[saved default-agent=%s]%s\n", cli.Dim, agentSlug, cli.Reset)
					}
				}
			}
		}

		var agentID string
		// Resolve the single-agent ID only on the single-agent path.
		// Fan-out path resolves each slug inline below.
		if !offline && len(fanoutAgents) == 0 {
			id, err := resolveAgentID(client, agentSlug)
			if err != nil {
				return err
			}
			agentID = id
		}

		flagPrompt, _ := cmd.Flags().GetString("prompt")
		withGitDiff, _ := cmd.Flags().GetBool("with-git-diff")
		withGitDiffStaged, _ := cmd.Flags().GetBool("with-git-staged")
		withGitLog, _ := cmd.Flags().GetBool("with-git-log")
		withGitStatus, _ := cmd.Flags().GetBool("with-git-status")
		withFiles, _ := cmd.Flags().GetStringSlice("with-file")
		withCmds, _ := cmd.Flags().GetStringSlice("with-cmd")
		paste, _ := cmd.Flags().GetBool("paste")

		prompt, err := cli.BuildPrompt(cmd.Context(), cli.PromptOptions{
			Positional:        args,
			PromptFlag:        flagPrompt,
			AutoStdin:         true,
			WithGitDiff:       withGitDiff,
			WithGitDiffStaged: withGitDiffStaged,
			WithGitLog:        withGitLog,
			WithGitStatus:     withGitStatus,
			WithFiles:         withFiles,
			WithCmds:          withCmds,
			Paste:             paste,
		})
		if err != nil {
			return err
		}
		if strings.TrimSpace(prompt) == "" {
			return fmt.Errorf("prompt is required (positional, --prompt, stdin pipe, or --with-* flag)")
		}

		// --plan flag opts this run into plan-mode (prompt-engineered,
		// no backend mode change). The latch is also set by `crewship
		// plan` which dispatches into this same RunE.
		planFlag, _ := cmd.Flags().GetBool("plan")
		if planFlag {
			planModeRequested = true
		}
		prompt = ApplyPlanFlag(prompt)

		if eff, _ := cmd.Flags().GetString("effort"); eff != "" {
			if err := SetEffort(eff); err != nil {
				return err
			}
		}
		if st, _ := cmd.Flags().GetBool("show-thinking"); st {
			SetShowThinking(true)
		}

		if dryRun {
			fmt.Print(prompt)
			if !strings.HasSuffix(prompt, "\n") {
				fmt.Println()
			}
			return nil
		}

		if estimate {
			fmt.Print(cli.FormatEstimate(prompt))
			return nil
		}

		quiet, _ := cmd.Flags().GetBool("quiet")
		md := resolveMarkdownFromCmd(cmd)
		saveFile, err := openSaveFile(cmd)
		if err != nil {
			return err
		}
		if saveFile != nil {
			defer saveFile.Close()
		}

		noStream, _ := cmd.Flags().GetBool("no-stream")
		timeoutSecs, _ := cmd.Flags().GetInt("timeout")

		// Fan-out path: --agents takes precedence over --agent / default-agent.
		if len(fanoutAgents) > 0 {
			agentsByID := map[string]string{}
			for _, slug := range fanoutAgents {
				slug = strings.TrimSpace(slug)
				if slug == "" {
					continue
				}
				id, err := resolveAgentID(client, slug)
				if err != nil {
					return fmt.Errorf("resolve %q: %w", slug, err)
				}
				agentsByID[id] = slug
			}
			wsToken, err := cli.WSTokenFromServer(client)
			if err != nil {
				return fmt.Errorf("get WS token: %w", err)
			}
			server := cli.ResolveServer(flagServer, cliCfg)
			return runFanout(server, wsToken, agentsByID, prompt, quiet, md, saveFile, timeoutSecs)
		}
		if timeoutSecs > 0 {
			client.HTTPClient.Timeout = time.Duration(timeoutSecs) * time.Second
		}

		// Create one-shot chat (origin=CLI keeps web UI sidebar tagged correctly).
		resp, err := client.Post("/api/v1/agents/"+agentID+"/chats", ChatCreationBody())
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
	askCmd.Flags().StringSlice("agents", nil, "Comma-separated list of agents for fan-out (overrides --agent; runs in parallel)")
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
	askCmd.Flags().Bool("paste", false, "Append the system clipboard as context (pbpaste/wl-paste/xclip/xsel)")
	askCmd.Flags().Bool("dry-run", false, "Print the assembled prompt and exit (no auth, no agent, no run)")
	askCmd.Flags().Bool("estimate", false, "Print token count + cost estimate and exit (no run)")
	askCmd.Flags().Bool("markdown", false, "Render markdown ANSI styling (overrides config)")
	askCmd.Flags().Bool("no-markdown", false, "Disable markdown ANSI styling (overrides config)")
	askCmd.Flags().String("save", "", "Also write the agent's text response (no ANSI) to this path")
	askCmd.Flags().Bool("plan", false, "Plan mode: output a step-by-step plan without executing tools")
	askCmd.Flags().String("effort", "", "Reasoning effort: minimal|low|medium|high|xhigh")
	askCmd.Flags().Bool("show-thinking", false, "Surface reasoning blocks on stdout (not truncated)")
}
