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
	wsproto "github.com/crewship-ai/crewship/internal/ws"
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
		waitFlag, _ := cmd.Flags().GetBool("wait")
		noStream = noStream || waitFlag
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

		server := streamServerURL()

		md := resolveMarkdownFromCmd(cmd)
		saveFile, err := openSaveFile(cmd)
		if err != nil {
			return err
		}
		if saveFile != nil {
			defer saveFile.Close()
		}

		maxTurns, _ := cmd.Flags().GetInt("max-turns")

		if interactive {
			return runInteractive(server, wsToken, agentID, agentSlug, chatID, prompt, quiet, md, saveFile, maxTurns)
		}

		if noStream {
			return runNoStream(server, wsToken, agentID, chatID, prompt, quiet, md, saveFile, maxTurns)
		}

		return runStream(server, wsToken, agentID, agentSlug, chatID, prompt, quiet, md, saveFile, maxTurns)
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

func runStream(serverURL, wsToken, agentID, agentSlug, chatID, prompt string, quiet bool, md *cli.MarkdownRenderer, save *cli.AtomicFile, maxTurns int) error {
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
	if err := ws.SendMessage(agentChannel, chatID, prompt, maxTurns); err != nil {
		return fmt.Errorf("send message: %w", err)
	}

	return streamEvents(ws, quiet, md, save)
}

func runNoStream(serverURL, wsToken, agentID, chatID, prompt string, quiet bool, md *cli.MarkdownRenderer, save *cli.AtomicFile, maxTurns int) error {
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
	if err := ws.SendMessage(agentChannel, chatID, prompt, maxTurns); err != nil {
		return fmt.Errorf("send message: %w", err)
	}

	// Collect all text, display only at the end. Terminal state is tracked
	// so that callers (e.g. `crewship seed --smoke-test` which execs this
	// subprocess) see a non-zero exit + diagnostic on error, instead of a
	// silent success. The collect loop is shared with routine iterate
	// (collectAgentStream, #998); timeout 0 = block until the server closes
	// the stream, the historical interactive behaviour.
	res := collectAgentStream(ws, 0)
	streamErr := res.StreamErr
	gotDone := res.GotDone
	readErr := res.ReadErr

	text := res.Text
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
	//
	// The bounce is checked first: it is the one outcome where the agent
	// never ran at all, so the generic "no text"/"no done" diagnostics below
	// would name the wrong cause. Non-zero exit like the rest, so
	// `crewship run x "y" || alert` notices, and worded as a wait rather
	// than a failure because the remedy is to retry once the agent frees up.
	if res.Busy {
		notice := res.BusyNotice
		if notice == "" {
			notice = "the agent is busy with another run"
		}
		fmt.Fprintf(os.Stderr, "%s[busy]%s %s\n", cli.Yellow, cli.Reset, notice)
		return fmt.Errorf("agent busy: %s", notice)
	}
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

func runInteractive(serverURL, wsToken, agentID, agentSlug, chatID, initialPrompt string, quiet bool, md *cli.MarkdownRenderer, save *cli.AtomicFile, maxTurns int) error {
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
		if err := ws.SendMessage(agentChannel, chatID, initialPrompt, maxTurns); err != nil {
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

		if err := ws.SendMessage(agentChannel, chatID, input, maxTurns); err != nil {
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
	var closeReason string
	for {
		msg, err := ws.ReadMessage()
		if err != nil {
			flush()
			// A dropped WS connection is a real failure — exit non-zero so
			// scripts (e.g. `crewship run x "y" || alert`) notice. Was
			// previously masking this as success when --save was unset.
			// #1386: if the server sent a rejection frame just before the
			// close, print WHY instead of a bare "ws read: EOF".
			if closeReason != "" {
				return joinErrs(fmt.Errorf("server rejected the connection: %s", closeReason))
			}
			return joinErrs(fmt.Errorf("ws read: %w", err))
		}

		// #1386: the server writes one error/session_revoked frame before
		// closing a refused connection. Capture its reason so the EOF above
		// can report it (x/net/websocket drops the WS close reason itself).
		if reason, ok := cli.CloseReason(msg); ok {
			closeReason = reason
			continue
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
		case wsproto.AgentBusyEventType:
			// The send bounced off the per-agent run lock (#2269): the agent
			// already has a live run, this message was NOT persisted, and the
			// server deliberately sends no terminal `done` after this frame —
			// emitting one would travel the shared session channel and
			// finalize the WINNING sender's live turn mid-generation
			// (internal/ws/client.go). So this frame IS the end of the
			// exchange, and a client that does not return here waits for a
			// `done` that is never coming: before this case existed,
			// `crewship run` against a busy agent hung silently until the
			// caller's own timeout killed it.
			//
			// Non-zero exit, like every other unstarted-run path, so
			// `crewship run x "y" || alert` notices. No maybeNotifyRunComplete:
			// nothing ran, so there is no completion to report — the notice
			// below is the whole outcome.
			flush()
			// Same as the error case: leave save uncommitted so the caller's
			// deferred Close discards the tempfile rather than truncating a
			// previous artefact over a run that never produced output.
			notice := sanitizeTerminal(event.Content)
			if notice == "" {
				notice = "the agent is busy with another run"
			}
			fmt.Fprintf(os.Stderr, "%s[busy]%s %s\n", cli.Yellow, cli.Reset, notice)
			return joinErrs(fmt.Errorf("agent busy: %s", notice))
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
		default:
			// An event type this client does not know. Silence here is how
			// the agent_busy hang survived: the server sent a frame, the
			// switch ignored it, and the loop went back to waiting for a
			// `done` that the server had already decided not to send. Under
			// -v, say what arrived so the next unhandled frame is one grep
			// away instead of an unexplained stall.
			if flagVerbose {
				fmt.Fprintf(os.Stderr, "%s[unhandled event]%s type=%q content=%s\n",
					cli.Gray, cli.Reset, event.Type, truncate(sanitizeTerminal(event.Content), 120))
			}
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

		// Decoded into the shared cli.RunDetail rather than a local struct.
		// `--format json` re-serialises whatever this decode produced, so a
		// field the local struct omitted was dropped from the CLI's own output
		// while the server had been sending it all along — `model` was missing
		// that way from the day it shipped. One type for the run shape means
		// the next field added server-side cannot repeat it.
		var result struct {
			Data []cli.RunDetail `json:"data"`
		}
		if err := cli.ReadJSON(resp, &result); err != nil {
			return err
		}

		f := newFormatter()
		// Seven columns is what fits: they answer "which run, whose, how did
		// it end, and which engine ran it". Model and the session-provenance
		// fields are per-run detail — `crewship run get <id>` shows them
		// without squeezing the listing. KIND was added for #2284: before
		// that fix, every row here was necessarily an ad-hoc agent run, so
		// the column would have said nothing; now a routine run can appear
		// too and the column is how a script (or a human) tells them apart
		// without guessing from TRIGGER, which routine runs don't populate.
		headers := []string{"ID", "AGENT", "STATUS", "KIND", "TRIGGER", "CREATED", "FINISHED"}
		var rows [][]string
		for _, r := range result.Data {
			finished := "-"
			if r.FinishedAt != nil {
				finished = *r.FinishedAt
			}
			// Truncate for the TABLE only — ShortID is what enforces that.
			// `--format quiet` renders the first cell of each row and exists
			// so a script can pipe ids into the next command; a 16-character
			// prefix is not an id, and feeding one back into `run get` answers
			// 404. This used to be an inline `f.Format != "quiet"` check here,
			// i.e. a rule every other list command had to rediscover — and
			// none of them had.
			id := r.ID
			if len(id) > 16 {
				id = f.ShortID(r.ID, id[:16])
			}
			rows = append(rows, []string{id, derefStr(r.AgentSlug, ""), r.Status, r.Kind, r.TriggerType, r.CreatedAt, finished})
		}
		if err := f.Auto(result.Data, headers, rows); err != nil {
			return err
		}
		// A skipped MCP server is the one piece of provenance that is a
		// finding rather than a label, and it is invisible in every column:
		// the run exited 0 and looks clean. Flag it under the table so a
		// listing cannot hide it. Machine formats already carry the field
		// verbatim and quiet is id-only for scripts, so the switch mirrors
		// Formatter.Auto's own.
		switch f.Format {
		case "json", "yaml", "ndjson", "quiet":
		default:
			printMCPSkipNotice(result.Data)
		}
		return nil
	},
}

// printMCPSkipNotice names the listed runs that started with an MCP server
// missing, and which servers those were. Prints nothing when every run got
// what it was configured with — a notice that appears unconditionally is one
// operators learn to skip.
func printMCPSkipNotice(runs []cli.RunDetail) {
	var affected []cli.RunDetail
	for _, r := range runs {
		if len(r.MCPServerErrors) > 0 {
			affected = append(affected, r)
		}
	}
	if len(affected) == 0 {
		return
	}
	noun := "run"
	if len(affected) > 1 {
		noun = "runs"
	}
	fmt.Printf("\n%s⚠ %d %s started with MCP servers skipped — the agent ran without them and still exited normally%s\n",
		cli.Yellow, len(affected), noun, cli.Reset)
	for _, r := range affected {
		names := make([]string, 0, len(r.MCPServerErrors)+1)
		for _, e := range r.MCPServerErrors {
			names = append(names, mcpSkipLabel(e))
		}
		// Same reason as in `run get`: this line is what an operator scans, and
		// naming three of five servers without saying so reads as five of five.
		if note := mcpSkipShortfall(len(r.MCPServerErrors), r.MCPServerErrorCount, r.MCPServerErrorsTruncated); note != "" {
			names = append(names, note)
		}
		fmt.Printf("  %s  %sskipped: %s%s\n", r.ID, cli.Dim, strings.Join(names, ", "), cli.Reset)
	}
	fmt.Printf("  %screwship run get <id> — shows why each one was skipped%s\n", cli.Dim, cli.Reset)
}

// mcpSkipLabel identifies one skipped server for a human: the name, qualified
// by the failure category when there is one.
//
// The fallback to the category alone is what makes the producer's
// "unrecognized_shape" sentinel usable. That entry is stored deliberately
// nameless — the CLI reported a skip in a shape the producer could not read,
// and it will not invent a server name — and both renderers here formatted
// these entries by NAME, so the alarm arrived empty: a banner counting runs
// with servers skipped, followed by nothing, and a bare "MCP skipped:" row.
// The alarm survived and the ability to act on it did not. The same fallback
// covers any real entry the CLI sends without a name.
func mcpSkipLabel(e cli.MCPServerError) string {
	switch {
	case e.Name == "" && e.Type == "":
		// Nothing to identify it by at all (a pre-projection run, or another
		// producer). The row still prints: that a server was skipped is the
		// alarm, and dropping it would be the one outcome worse than a vague
		// one.
		return "(unnamed)"
	case e.Name == "":
		return e.Type
	case e.Type == "":
		return e.Name
	default:
		return e.Name + " (" + e.Type + ")"
	}
}

var runGetCmd = &cobra.Command{
	Use:   "get <run-id>",
	Short: "Show one run in full, including how it was served",
	Long: `Print everything recorded about a single run.

Beyond status and timing this is where the session provenance lives: the CLI
version that served the run, which credential path resolved, the permission
mode in force, the CLI's own session id — and any MCP server that was skipped
at startup, which a run reports while still exiting 0.

Fields the run never recorded (older runs, non-Claude adapters) are omitted
rather than shown blank.

Examples:
  crewship run get msg_abc
  crewship run get msg_abc -o json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		run, err := client.GetRun(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		f := newFormatter()
		// Detail, not Table: this is one entity with a dozen optional fields,
		// several of them long (a session id, an MCP error message). A
		// key/value dump can skip the rows that have no answer; a table row
		// would have to render a placeholder for each.
		return f.AutoDetail(run, runDetailPairs(run))
	},
}

// runDetailPairs builds the key/value rows for `run get`. Optional fields are
// appended only when the server sent them: an empty "CLI version:" row would
// claim the run was asked and answered nothing, which is a different fact from
// a run that predates the field.
func runDetailPairs(r *cli.RunDetail) [][]string {
	pairs := [][]string{
		{"ID", r.ID},
		{"Status", r.Status},
	}
	// Kind (agent vs pipeline, #2284) is a plain string, not a pointer like
	// the optional fields below — but a server that predates the field
	// leaves it "" on decode, so still guard rather than print a blank row.
	if r.Kind != "" {
		pairs = append(pairs, []string{"Kind", r.Kind})
	}
	add := func(label string, v *string) {
		if v != nil && *v != "" {
			pairs = append(pairs, []string{label, *v})
		}
	}
	add("Agent", r.AgentSlug)
	add("Crew", r.CrewName)
	if r.TriggerType != "" {
		pairs = append(pairs, []string{"Trigger", r.TriggerType})
	}
	add("Triggered by", r.TriggeredBy)
	add("Chat", r.ChatID)
	add("Started", r.StartedAt)
	add("Finished", r.FinishedAt)
	if r.ExitCode != nil {
		pairs = append(pairs, []string{"Exit code", fmt.Sprintf("%d", *r.ExitCode)})
	}
	add("Error", r.ErrorMessage)
	add("Model", r.Model)
	add("CLI version", r.CLIVersion)
	add("Auth source", r.APIKeySource)
	add("Permission mode", r.PermissionMode)
	add("Session", r.SessionID)
	// One row per skipped server, each carrying its own reason — collapsing
	// them into a count would hide which capability was lost, and that is the
	// only actionable part.
	for _, e := range r.MCPServerErrors {
		detail := mcpSkipLabel(e)
		if e.Message != "" {
			detail += ": " + e.Message
		}
		pairs = append(pairs, []string{"MCP skipped", detail})
	}
	// The rows above are what the record could name, which is not always what
	// the CLI reported: entries whose fields the producer could not read are
	// dropped, and the list is capped. Saying so is the difference between a
	// partial list and a partial list that reads as complete.
	if note := mcpSkipShortfall(len(r.MCPServerErrors), r.MCPServerErrorCount, r.MCPServerErrorsTruncated); note != "" {
		pairs = append(pairs, []string{"MCP skipped", note})
	}
	// Tools the CLI refused to let the agent use. Without this row the run
	// reads as one that chose not to act, and the operator goes looking for a
	// prompt problem instead of a permission rule.
	if len(r.PermissionDenials) > 0 {
		names := make([]string, 0, len(r.PermissionDenials))
		for _, d := range r.PermissionDenials {
			names = append(names, deniedToolLabel(d))
		}
		pairs = append(pairs, []string{"Tools denied", strings.Join(names, ", ")})
		if r.PermissionDenialsTruncated {
			pairs = append(pairs, []string{"Tools denied",
				"… more tools were denied than this record kept (list capped)"})
		}
	}
	return pairs
}

// deniedToolLabel names one denied tool and, when the agent was refused more
// than once, how often. One refusal is an agent that tried something and moved
// on; forty is an agent hammering a wall it cannot see, and the two want
// different fixes. A "×1" on every other row would bury that difference in
// noise, so the count shows only when it carries the signal.
func deniedToolLabel(d cli.DeniedTool) string {
	if d.Count > 1 {
		return fmt.Sprintf("%s ×%d", d.ToolName, d.Count)
	}
	return d.ToolName
}

// mcpSkipShortfall describes what a skip list does NOT show: servers the record
// could not identify, or ones a cap cut. Empty when the list is everything the
// CLI reported — a caveat printed unconditionally is one operators learn to
// skip, and then the real one is invisible too.
//
// total is 0 on runs recorded before the count existed, which is why the
// shortfall is computed rather than assumed: reporting "-1 more" on every old
// run would be exactly that kind of noise.
func mcpSkipShortfall(shown, total int, truncated bool) string {
	switch {
	case total > shown && truncated:
		return fmt.Sprintf("… and %d more this record did not keep (list capped)", total-shown)
	case total > shown:
		// The CLI reported them; the producer could not read their fields, so
		// naming them is not possible — saying how many is.
		return fmt.Sprintf("… and %d more the CLI reported in a shape this record could not identify", total-shown)
	case truncated:
		return "… more servers were skipped than this record kept (list capped)"
	}
	return ""
}

// runInsightsResp mirrors the /api/v1/runs/insights response.
type runInsightsResp struct {
	Window string `json:"window"`
	Totals struct {
		Total     int `json:"total"`
		Succeeded int `json:"succeeded"`
		Failed    int `json:"failed"`
		Running   int `json:"running"`
	} `json:"totals"`
	Duration struct {
		P50Ms int64 `json:"p50_ms"`
		P95Ms int64 `json:"p95_ms"`
	} `json:"duration"`
	ByTrigger []insightCat   `json:"by_trigger"`
	ByModel   []insightCat   `json:"by_model"`
	ByCrew    []insightCrew  `json:"by_crew"`
	TopAgents []insightAgent `json:"top_agents"`
	Truncated bool           `json:"truncated"`
}

type insightCat struct {
	Key    string `json:"key"`
	Total  int    `json:"total"`
	Failed int    `json:"failed"`
}
type insightCrew struct {
	Name   string `json:"name"`
	Total  int    `json:"total"`
	Failed int    `json:"failed"`
}
type insightAgent struct {
	Name     string `json:"name"`
	CrewName string `json:"crew_name"`
	Total    int    `json:"total"`
	Failed   int    `json:"failed"`
}

var runInsightsCmd = &cobra.Command{
	Use:   "insights",
	Short: "Fleet operations overview — outcome, duration, and breakdowns across ALL runs",
	Long: `Aggregate every run in the workspace over a window (not just routine runs)
into an operations snapshot: success/fail split, duration percentiles, and
breakdowns by trigger, crew and model.

Examples:
  crewship run insights
  crewship run insights --window 7d
  crewship run insights -o json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		window, _ := cmd.Flags().GetString("window")
		switch window {
		case "24h", "7d", "30d":
		default:
			return fmt.Errorf("bad --window %q: must be one of 24h, 7d, 30d", window)
		}

		client := newAPIClient()
		resp, err := client.Get("/api/v1/runs/insights?window=" + window)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var body runInsightsResp
		if err := cli.ReadJSON(resp, &body); err != nil {
			return err
		}

		f := newFormatter()
		return f.AutoHuman(body, func() {
			// renderRunInsights only ever returns nil (it prints); the
			// AutoHuman human closure can't propagate an error anyway.
			_ = renderRunInsights(body)
		})
	},
}

// renderRunInsights prints the human-readable ops snapshot.
func renderRunInsights(b runInsightsResp) error {
	windowLabel := map[string]string{"24h": "last 24h", "7d": "last 7 days", "30d": "last 30 days"}[b.Window]
	fmt.Printf("%sFleet operations · %s%s\n", cli.Bold, windowLabel, cli.Reset)
	rate := "—"
	if b.Totals.Succeeded+b.Totals.Failed > 0 {
		rate = fmt.Sprintf("%d%%", int(float64(b.Totals.Succeeded)*100/float64(b.Totals.Succeeded+b.Totals.Failed)))
	}
	fmt.Printf("  %d runs   %d ok   %d failed   %d running   ·   success %s   ·   p50 %s  p95 %s\n\n",
		b.Totals.Total, b.Totals.Succeeded, b.Totals.Failed, b.Totals.Running, rate,
		fmtMillis(b.Duration.P50Ms), fmtMillis(b.Duration.P95Ms))

	printCatSection("By trigger", b.ByTrigger)
	printCrewSection("Top crews", b.ByCrew)
	printCatSection("By model", b.ByModel)
	printAgentSection("Top agents", b.TopAgents)

	if b.Truncated {
		fmt.Printf("%s(window exceeded the aggregation cap — figures cover the most recent runs only)%s\n", cli.Dim, cli.Reset)
	}
	if b.Totals.Total == 0 {
		fmt.Printf("%sNo runs in this window.%s\n", cli.Dim, cli.Reset)
	}
	return nil
}

func printCatSection(title string, cats []insightCat) {
	if len(cats) == 0 {
		return
	}
	fmt.Printf("%s%s%s\n", cli.Dim, title, cli.Reset)
	for _, c := range cats {
		fmt.Printf("  %-18s %5d   %sfail %d%s\n", c.Key, c.Total, cli.Dim, c.Failed, cli.Reset)
	}
	fmt.Println()
}

func printCrewSection(title string, crews []insightCrew) {
	if len(crews) == 0 {
		return
	}
	fmt.Printf("%s%s%s\n", cli.Dim, title, cli.Reset)
	for _, c := range crews {
		fmt.Printf("  %-18s %5d   %sfail %d%s\n", c.Name, c.Total, cli.Dim, c.Failed, cli.Reset)
	}
	fmt.Println()
}

func printAgentSection(title string, agents []insightAgent) {
	if len(agents) == 0 {
		return
	}
	fmt.Printf("%s%s%s\n", cli.Dim, title, cli.Reset)
	for _, a := range agents {
		crew := a.CrewName
		if crew == "" {
			crew = "—"
		}
		fmt.Printf("  %-18s %5d   %s%s · fail %d%s\n", a.Name, a.Total, cli.Dim, crew, a.Failed, cli.Reset)
	}
	fmt.Println()
}

// fmtMillis renders a millisecond count as a compact duration ("18.4s",
// "1m12s"), or "—" when zero (no finished runs to measure).
func fmtMillis(ms int64) string {
	if ms <= 0 {
		return "—"
	}
	return (time.Duration(ms) * time.Millisecond).String()
}

func init() {
	runCmd.Flags().StringP("prompt", "p", "", "Prompt text, @file, or @- for stdin")
	runCmd.Flags().Bool("interactive", false, "Interactive chat mode")
	runCmd.Flags().String("chat", "", "Continue existing chat (chat ID)")
	runCmd.Flags().Bool("no-stream", false, "Wait for completion, show only result")
	runCmd.Flags().Bool("wait", false, "Wait for completion, show only result (alias for --no-stream, matches 'crewship pipeline run --wait')")
	runCmd.Flags().BoolP("quiet", "q", false, "Only output text, no meta info")
	runCmd.Flags().Int("timeout", 0, "Timeout in seconds (0 = no timeout)")
	runCmd.Flags().Int("max-turns", 0, "Cap the agent loop at N turns for this run (0 = adapter default, 50 interactive)")
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

	runInsightsCmd.Flags().String("window", "24h", "Aggregation window: 24h, 7d, or 30d")
	runCmd.AddCommand(runListCmd)
	runCmd.AddCommand(runGetCmd)
	runCmd.AddCommand(runInsightsCmd)
}
