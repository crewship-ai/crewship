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
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		estimate, _ := cmd.Flags().GetBool("estimate")
		offline := dryRun || estimate

		// Auth/workspace + agent resolution are skipped for offline modes
		// (--dry-run / --estimate) so users can preview prompts and token
		// counts without a login or server.
		if !offline {
			if err := requireAuth(); err != nil {
				return err
			}
			if err := requireWorkspace(); err != nil {
				return err
			}
		}

		agentSlug := args[0]
		var client *cli.Client
		var agentID string
		if !offline {
			client = newAPIClient()
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

		var positional []string
		if len(args) > 1 {
			positional = args[1:]
		}

		prompt, err := cli.BuildPrompt(cmd.Context(), cli.PromptOptions{
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

		// Plan mode is a prompt-prefix injection rather than a server
		// flag — see cmd_plan.go for the full rationale. Reset latches
		// on the way out so a second invocation in the same process
		// (REPL turn, test) sees a clean slate.
		defer ResetAIFirstLatches()
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

		interactive, _ := cmd.Flags().GetBool("interactive")
		noStream, _ := cmd.Flags().GetBool("no-stream")
		quiet, _ := cmd.Flags().GetBool("quiet")
		existingChat, _ := cmd.Flags().GetString("chat")
		timeoutSecs, _ := cmd.Flags().GetInt("timeout")

		if !interactive && prompt == "" {
			return fmt.Errorf("prompt is required (provide as argument, --prompt flag, or use --interactive)")
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

		if timeoutSecs > 0 {
			client.HTTPClient.Timeout = time.Duration(timeoutSecs) * time.Second
		}

		// Create or reuse chat
		chatID := existingChat
		if chatID == "" {
			// Tag the session as CLI-origin so the SessionsSidebar in
			// the chat UI shows a violet "CLI" chip — lets the user
			// tell at a glance which sessions were spun up from a
			// terminal vs the web UI. ChatCreationBody folds in plan /
			// effort metadata when active.
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

// openSaveFile reads the --save flag and opens an atomic file for tee'ing
// agent text. Returns (nil, nil) when the flag is unset.
//
// Atomic = a tempfile in the target's directory; the caller must call
// Commit() on the success path. A crash mid-stream leaves the previous
// file (or no file) intact rather than a half-written replacement.
//
// Files are truncated on commit — `--save` is "save this run's output",
// not "append to a log". Append behaviour is trivially available via
// shell `tee -a` if the user really wants it.
func openSaveFile(cmd *cobra.Command) (*cli.AtomicFile, error) {
	path, _ := cmd.Flags().GetString("save")
	if path == "" {
		return nil, nil
	}
	f, err := cli.NewAtomicFile(path)
	if err != nil {
		return nil, fmt.Errorf("open save file: %w", err)
	}
	return f, nil
}

func runStream(serverURL, wsToken, agentID, agentSlug, chatID, prompt string, quiet bool, md *cli.MarkdownRenderer, save *cli.AtomicFile) error {
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

func runNoStream(serverURL, wsToken, agentID, chatID, prompt string, quiet bool, md *cli.MarkdownRenderer, save *cli.AtomicFile) error {
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
			// Sanitise on capture rather than on emit so every later
			// use (stderr print, returned error string) is uniformly
			// safe and we don't have to remember at each call site.
			streamErr = sanitizeTerminal(event.Content)
		case "done":
			gotDone = true
		}
		if event.Type == "done" || event.Type == "error" {
			break
		}
	}

	text := fullText.String()
	if text != "" {
		// Save un-styled, control-char-stripped text to file so the saved
		// artefact is plain markdown — useful for piping into tools or
		// committing. Sanitising before write means a malicious tool
		// result that emitted ANSI/OSC sequences can't survive into the
		// persisted artifact (and surprise the next `cat saved.md`).
		// Failures here (disk full, permission denied) propagate as a
		// non-zero exit so scripts can rely on the artefact being
		// either complete or known-broken.
		safeText := sanitizeTerminal(text)
		if save != nil {
			if _, err := save.WriteString(safeText); err != nil {
				return fmt.Errorf("save write: %w", err)
			}
			if !strings.HasSuffix(safeText, "\n") {
				if _, err := save.WriteString("\n"); err != nil {
					return fmt.Errorf("save write: %w", err)
				}
			}
			// Commit only on a clean stream — error/missing-done branches below
			// fall through without committing so the tempfile is discarded.
			if streamErr == "" && gotDone {
				if err := save.Commit(); err != nil {
					return fmt.Errorf("save commit: %w", err)
				}
			}
		}
		toPrint := text
		if md != nil {
			toPrint = md.Render(text)
		} else {
			// Strip control characters (ANSI escapes, OSC sequences,
			// cursor manipulation) from raw model output before
			// printing — agents have no legitimate need to drive the
			// terminal, and a malicious tool result could otherwise
			// rewrite the user's scrollback. The markdown renderer
			// already does its own sanitisation, so the strip only
			// runs on the raw path.
			toPrint = sanitizeTerminal(toPrint)
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

func runInteractive(serverURL, wsToken, agentID, agentSlug, chatID, initialPrompt string, quiet bool, md *cli.MarkdownRenderer, save *cli.AtomicFile) error {
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

func streamEvents(ws *cli.WSClient, quiet bool, md *cli.MarkdownRenderer, save *cli.AtomicFile) error {
	startedAt := time.Now()
	flush := func() {
		if md != nil {
			fmt.Print(md.Flush())
		}
	}
	// saveErr captures the first error from Write/Commit so a script-mode
	// caller can detect that --save failed even though the on-screen
	// stream looked fine. Returning it from streamEvents propagates to a
	// non-zero exit at the cobra level.
	var saveErr error
	emitText := func(s string) {
		// Sanitise once so both the save file and the raw-terminal
		// branch get control-char-stripped bytes. The markdown
		// renderer does its own escaping so it still gets the
		// original `s`. Saved files are meant to be plain markdown
		// the user can re-process — not a screencast of ANSI codes.
		safe := sanitizeTerminal(s)
		if save != nil && saveErr == nil {
			if _, err := save.WriteString(safe); err != nil {
				saveErr = fmt.Errorf("save write: %w", err)
				fmt.Fprintf(os.Stderr, "%s[save]%s write failed: %v\n", cli.Yellow, cli.Reset, err)
			}
		}
		if md != nil {
			fmt.Print(md.Write(s))
		} else {
			// Raw text from the agent flows straight to the user's
			// terminal — strip control chars so a tool result can't
			// emit ANSI escapes / OSC links and rewrite the scrollback.
			fmt.Print(safe)
		}
	}
	// joinErrs combines a save-time error with a stream-time error so
	// the caller sees both. Without this, "agent error" and "save commit
	// failed" together would lose one — exit-code reliability matters
	// for scripts wrapping run/ask.
	joinErrs := func(streamErr error) error {
		if saveErr != nil && streamErr != nil {
			return fmt.Errorf("%v; %w", saveErr, streamErr)
		}
		if streamErr != nil {
			return streamErr
		}
		return saveErr
	}
	for {
		msg, err := ws.ReadMessage()
		if err != nil {
			flush()
			// A dropped WS connection is a real failure — exit non-zero so
			// scripts (e.g. `crewship run x "y" || alert`) notice. Was
			// previously masking this as success when --save was unset.
			return joinErrs(fmt.Errorf("ws read: %w", err))
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
			// --show-thinking emits the full reasoning to stdout so it
			// becomes part of the captured output; --quiet alone still
			// suppresses the dim stderr peek. Untruncated text can be
			// huge for some models — that's the user's choice.
			// sanitizeTerminal strips any control chars the model
			// emitted before they reach the user's terminal.
			thinking := sanitizeTerminal(event.Content)
			if showThinking {
				fmt.Print(thinking)
				if !strings.HasSuffix(thinking, "\n") {
					fmt.Println()
				}
			} else if !quiet {
				fmt.Fprintf(os.Stderr, "%s[thinking]%s %s\n", cli.Gray, cli.Reset, truncate(thinking, 100))
			}
		case "tool_call":
			if !quiet {
				fmt.Fprintf(os.Stderr, "%s[tool]%s %s\n", cli.Cyan, cli.Reset, truncate(sanitizeTerminal(event.Content), 100))
			}
		case "tool_result":
			if !quiet && flagVerbose {
				fmt.Fprintf(os.Stderr, "%s[result]%s %s\n", cli.Gray, cli.Reset, truncate(sanitizeTerminal(event.Content), 200))
			}
		case "status":
			if !quiet {
				fmt.Fprintf(os.Stderr, "%s[status]%s %s\n", cli.Dim, cli.Reset, sanitizeTerminal(event.Content))
			}
		case "error":
			flush()
			// Don't commit save — defer Close in the caller discards the
			// tempfile so an aborted run never overwrites a previous artefact.
			// Sanitise once and reuse so both the stderr line and the
			// returned error string are uniformly free of control chars.
			safeErr := sanitizeTerminal(event.Content)
			fmt.Fprintf(os.Stderr, "%s[error]%s %s\n", cli.Red, cli.Reset, safeErr)
			maybeNotifyRunComplete(startedAt, "", "FAILED")
			return joinErrs(fmt.Errorf("agent error: %s", safeErr))
		case "done":
			flush()
			if save != nil && saveErr == nil {
				if err := save.Commit(); err != nil {
					saveErr = fmt.Errorf("save commit: %w", err)
					fmt.Fprintf(os.Stderr, "%s[save]%s commit failed: %v\n", cli.Yellow, cli.Reset, err)
				}
			}
			if !quiet {
				fmt.Fprintf(os.Stderr, "\n%s[done]%s\n", cli.Green, cli.Reset)
			}
			maybeNotifyRunComplete(startedAt, "", "COMPLETED")
			return saveErr
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
	runCmd.Flags().Bool("dry-run", false, "Print the assembled prompt (with all context) and exit without running")
	runCmd.Flags().Bool("estimate", false, "Print token count + cost estimate for the prompt and exit (no run)")
	runCmd.Flags().Bool("markdown", false, "Render markdown ANSI styling (overrides config)")
	runCmd.Flags().Bool("no-markdown", false, "Disable markdown ANSI styling (overrides config)")
	runCmd.Flags().String("save", "", "Also write the agent's text response (no ANSI) to this path")
	runCmd.Flags().Bool("plan", false, "Plan mode: output a step-by-step plan without executing tools")
	runCmd.Flags().String("effort", "", "Reasoning effort: minimal|low|medium|high|xhigh")
	runCmd.Flags().Bool("show-thinking", false, "Surface reasoning blocks on stdout (not truncated)")

	runCmd.AddCommand(runListCmd)
}
