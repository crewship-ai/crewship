package main

import (
	"bufio"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

var runCmd = &cobra.Command{
	Use:               "run <agent-slug> [prompt]",
	Short:             "Run an agent with a prompt",
	ValidArgsFunction: completeAgentSlug,
	Long: `Run an agent with a prompt and stream output to the terminal.

Examples:
  crewship run viktor "Create a REST API"
  crewship run viktor --prompt @task.txt
  crewship run viktor --prompt @-           # read from stdin
  cat issue.md | crewship run viktor "fix"  # stdin auto-appended as context
  git diff | crewship run viktor "review" --with-git-status
  crewship run viktor --interactive
  crewship run viktor --chat <chatId> "follow-up question"`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		agentSlug := args[0]
		client := newAPIClient()

		// Resolve agent
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
		paste, _ := cmd.Flags().GetBool("paste")

		var positional []string
		if len(args) > 1 {
			positional = args[1:]
		}

		prompt, err := cli.BuildPrompt(cli.PromptOptions{
			Positional:        positional,
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

		interactive, _ := cmd.Flags().GetBool("interactive")
		noStream, _ := cmd.Flags().GetBool("no-stream")
		quiet, _ := cmd.Flags().GetBool("quiet")
		existingChat, _ := cmd.Flags().GetString("chat")
		timeoutSecs, _ := cmd.Flags().GetInt("timeout")

		if !interactive && prompt == "" {
			return fmt.Errorf("prompt is required (provide as argument, --prompt flag, or use --interactive)")
		}

		if timeoutSecs > 0 {
			client.HTTPClient.Timeout = time.Duration(timeoutSecs) * time.Second
		}

		// Create or reuse chat
		chatID := existingChat
		if chatID == "" {
			// Tag the session as CLI-origin so the SessionsSidebar in
			// the chat UI shows a violet "CLI" chip — lets the user
			// tell at a glance which sessions were spun up from a
			// terminal vs the web UI.
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
			chatID = chatResult.ID
		}

		// Get WS token
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

		if interactive {
			return runInteractive(server, wsToken, agentID, agentSlug, chatID, prompt, quiet, md, saveFile)
		}

		if noStream {
			return runNoStream(server, wsToken, agentID, chatID, prompt, quiet, md, saveFile)
		}

		return runStream(server, wsToken, agentID, agentSlug, chatID, prompt, quiet, md, saveFile)
	},
}

// resolveMarkdownFromCmd reads --markdown / --no-markdown and returns a renderer
// (or nil if rendering is disabled). Callers pass the result through to
// streaming/no-stream printers.
func resolveMarkdownFromCmd(cmd *cobra.Command) *cli.MarkdownRenderer {
	on, _ := cmd.Flags().GetBool("markdown")
	off, _ := cmd.Flags().GetBool("no-markdown")
	setting := ""
	if cliCfg != nil {
		setting = cliCfg.Markdown
	}
	if cli.ResolveMarkdown(setting, on, off, flagNoColor) {
		return cli.NewMarkdownRenderer()
	}
	return nil
}

// openSaveFile reads the --save flag and opens a writable file for tee'ing
// agent text. Returns (nil, nil) when the flag is unset.
//
// Files are truncated on open — `--save` is "save this run's output", not
// "append to a log". Append behaviour is one level of magic the user can
// trivially get with `crewship run ... | tee -a log` if they really want it.
func openSaveFile(cmd *cobra.Command) (*os.File, error) {
	path, _ := cmd.Flags().GetString("save")
	if path == "" {
		return nil, nil
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("open save file: %w", err)
	}
	return f, nil
}

func runStream(serverURL, wsToken, agentID, agentSlug, chatID, prompt string, quiet bool, md *cli.MarkdownRenderer, save *os.File) error {
	ws, err := cli.NewWSClient(serverURL, wsToken)
	if err != nil {
		return err
	}
	defer ws.Close()

	channel := "session:" + chatID
	if err := ws.Subscribe(channel); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	if !quiet {
		fmt.Fprintf(os.Stderr, "%s[agent: %s]%s Starting run...\n", cli.Dim, agentSlug, cli.Reset)
	}

	// Handle Ctrl+C: first cancels the run, second terminates the process
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT)
	go func() {
		<-sig
		ws.CancelMessage(chatID)
		fmt.Fprintf(os.Stderr, "\n%s[cancelled]%s\n", cli.Yellow, cli.Reset)
		signal.Reset(syscall.SIGINT)
	}()

	agentChannel := "agent:" + agentID
	if err := ws.SendMessage(agentChannel, chatID, prompt); err != nil {
		return fmt.Errorf("send message: %w", err)
	}

	return streamEvents(ws, quiet, md, save)
}

func runNoStream(serverURL, wsToken, agentID, chatID, prompt string, quiet bool, md *cli.MarkdownRenderer, save *os.File) error {
	ws, err := cli.NewWSClient(serverURL, wsToken)
	if err != nil {
		return err
	}
	defer ws.Close()

	channel := "session:" + chatID
	if err := ws.Subscribe(channel); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	agentChannel := "agent:" + agentID
	if err := ws.SendMessage(agentChannel, chatID, prompt); err != nil {
		return fmt.Errorf("send message: %w", err)
	}

	// Collect all text, display only at the end. Track terminal state so that
	// callers (e.g. `crewship seed --smoke-test` which execs this subprocess)
	// see a non-zero exit + diagnostic on error, instead of a silent success.
	var fullText strings.Builder
	var streamErr string // populated by "error" events
	gotDone := false
	readErr := error(nil)
	for {
		msg, err := ws.ReadMessage()
		if err != nil {
			readErr = err
			break
		}
		event, err := cli.ParseChatEvent(msg)
		if err != nil || event == nil {
			continue
		}
		switch event.Type {
		case "text":
			fullText.WriteString(event.Content)
		case "error":
			streamErr = event.Content
		case "done":
			gotDone = true
		}
		if event.Type == "done" || event.Type == "error" {
			break
		}
	}

	text := fullText.String()
	if text != "" {
		// Save raw (un-styled) text to file so the saved artefact is plain
		// markdown — useful for piping into tools or committing.
		if save != nil {
			_, _ = save.WriteString(text)
			if !strings.HasSuffix(text, "\n") {
				_, _ = save.WriteString("\n")
			}
		}
		toPrint := text
		if md != nil {
			toPrint = md.Render(text)
		}
		fmt.Print(toPrint)
		if !strings.HasSuffix(toPrint, "\n") {
			fmt.Println()
		}
	}

	// Failure cases — emit a clear stderr message so exec callers can diagnose,
	// and return an error so the process exits non-zero.
	if streamErr != "" {
		fmt.Fprintf(os.Stderr, "%s[error]%s %s\n", cli.Red, cli.Reset, streamErr)
		return fmt.Errorf("agent error: %s", streamErr)
	}
	if text == "" {
		if readErr != nil {
			fmt.Fprintf(os.Stderr, "%s[error]%s connection closed before any output: %v\n",
				cli.Red, cli.Reset, readErr)
			return fmt.Errorf("connection closed before any output: %w", readErr)
		}
		if !gotDone {
			fmt.Fprintln(os.Stderr, cli.Red+"[error]"+cli.Reset+" stream ended without done event and no text received")
			return fmt.Errorf("stream ended without done event and no text received")
		}
		fmt.Fprintln(os.Stderr, cli.Red+"[error]"+cli.Reset+" agent returned no text")
		return fmt.Errorf("agent returned no text")
	}
	return nil
}

func runInteractive(serverURL, wsToken, agentID, agentSlug, chatID, initialPrompt string, quiet bool, md *cli.MarkdownRenderer, save *os.File) error {
	ws, err := cli.NewWSClient(serverURL, wsToken)
	if err != nil {
		return err
	}
	defer ws.Close()

	channel := "session:" + chatID
	if err := ws.Subscribe(channel); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	agentChannel := "agent:" + agentID

	if !quiet {
		fmt.Fprintf(os.Stderr, "%s[agent: %s]%s Ready. Type your message (Ctrl+D to exit):\n\n",
			cli.Dim, agentSlug, cli.Reset)
	}

	// Handle Ctrl+C: cancel current run, second Ctrl+C terminates
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT)
	go func() {
		for range sig {
			ws.CancelMessage(chatID)
		}
	}()

	// If initial prompt given, send it first
	if initialPrompt != "" {
		if err := ws.SendMessage(agentChannel, chatID, initialPrompt); err != nil {
			return fmt.Errorf("send message: %w", err)
		}
		if err := streamEvents(ws, quiet, md, save); err != nil {
			return err
		}
	}

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("\n> ")
		if !scanner.Scan() {
			// Ctrl+D
			if !quiet {
				fmt.Fprintf(os.Stderr, "\n%s[session ended]%s\n", cli.Dim, cli.Reset)
			}
			return nil
		}

		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}

		if err := ws.SendMessage(agentChannel, chatID, input); err != nil {
			return fmt.Errorf("send message: %w", err)
		}

		if err := streamEvents(ws, quiet, md, save); err != nil {
			return err
		}
	}
}

func streamEvents(ws *cli.WSClient, quiet bool, md *cli.MarkdownRenderer, save *os.File) error {
	flush := func() {
		if md != nil {
			fmt.Print(md.Flush())
		}
	}
	emitText := func(s string) {
		// Always write raw text to the save file before any markdown
		// styling — saved files should be plain markdown the user can
		// re-process, not a screencast of ANSI codes.
		if save != nil {
			_, _ = save.WriteString(s)
		}
		if md != nil {
			fmt.Print(md.Write(s))
		} else {
			fmt.Print(s)
		}
	}
	for {
		msg, err := ws.ReadMessage()
		if err != nil {
			flush()
			return nil
		}

		event, err := cli.ParseChatEvent(msg)
		if err != nil || event == nil {
			if msg.Type == "pong" || msg.Type == "ping" {
				continue
			}
			continue
		}

		switch event.Type {
		case "text":
			emitText(event.Content)
		case "thinking":
			if !quiet {
				fmt.Fprintf(os.Stderr, "%s[thinking]%s %s\n", cli.Gray, cli.Reset, truncate(event.Content, 100))
			}
		case "tool_call":
			if !quiet {
				fmt.Fprintf(os.Stderr, "%s[tool]%s %s\n", cli.Cyan, cli.Reset, truncate(event.Content, 100))
			}
		case "tool_result":
			if !quiet && flagVerbose {
				fmt.Fprintf(os.Stderr, "%s[result]%s %s\n", cli.Gray, cli.Reset, truncate(event.Content, 200))
			}
		case "status":
			if !quiet {
				fmt.Fprintf(os.Stderr, "%s[status]%s %s\n", cli.Dim, cli.Reset, event.Content)
			}
		case "error":
			flush()
			fmt.Fprintf(os.Stderr, "%s[error]%s %s\n", cli.Red, cli.Reset, event.Content)
			return nil
		case "done":
			flush()
			if !quiet {
				fmt.Fprintf(os.Stderr, "\n%s[done]%s\n", cli.Green, cli.Reset)
			}
			return nil
		}
	}
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if utf8.RuneCountInString(s) > n {
		runes := []rune(s)
		return string(runes[:n-3]) + "..."
	}
	return s
}

var runListCmd = &cobra.Command{
	Use:   "list",
	Short: "List recent runs across all agents",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Get("/api/v1/runs")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var result struct {
			Data []struct {
				ID          string  `json:"id"`
				AgentSlug   string  `json:"agent_slug"`
				Status      string  `json:"status"`
				TriggerType string  `json:"trigger_type"`
				CreatedAt   string  `json:"created_at"`
				FinishedAt  *string `json:"finished_at"`
			} `json:"data"`
		}
		if err := cli.ReadJSON(resp, &result); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"ID", "AGENT", "STATUS", "TRIGGER", "CREATED", "FINISHED"}
		var rows [][]string
		for _, r := range result.Data {
			finished := "-"
			if r.FinishedAt != nil {
				finished = *r.FinishedAt
			}
			id := r.ID
			if len(id) > 16 {
				id = id[:16]
			}
			rows = append(rows, []string{id, r.AgentSlug, r.Status, r.TriggerType, r.CreatedAt, finished})
		}
		return f.Auto(result.Data, headers, rows)
	},
}

func init() {
	runCmd.Flags().StringP("prompt", "p", "", "Prompt text, @file, or @- for stdin")
	runCmd.Flags().Bool("interactive", false, "Interactive chat mode")
	runCmd.Flags().String("chat", "", "Continue existing chat (chat ID)")
	runCmd.Flags().Bool("no-stream", false, "Wait for completion, show only result")
	runCmd.Flags().BoolP("quiet", "q", false, "Only output text, no meta info")
	runCmd.Flags().Int("timeout", 0, "Timeout in seconds (0 = no timeout)")
	runCmd.Flags().Bool("with-git-diff", false, "Append `git diff` as context")
	runCmd.Flags().Bool("with-git-staged", false, "Append `git diff --staged` as context")
	runCmd.Flags().Bool("with-git-log", false, "Append last 20 commits as context")
	runCmd.Flags().Bool("with-git-status", false, "Append `git status -s` as context")
	runCmd.Flags().StringSlice("with-file", nil, "Append file content(s) as context (repeatable)")
	runCmd.Flags().StringSlice("with-cmd", nil, "Append shell command output as context (repeatable)")
	runCmd.Flags().Bool("paste", false, "Append the system clipboard as context (pbpaste/wl-paste/xclip/xsel)")
	runCmd.Flags().Bool("markdown", false, "Render markdown ANSI styling (overrides config)")
	runCmd.Flags().Bool("no-markdown", false, "Disable markdown ANSI styling (overrides config)")
	runCmd.Flags().String("save", "", "Also write the agent's text response (no ANSI) to this path")

	runCmd.AddCommand(runListCmd)
}
