package main

// `crewship chat stop` and the stop half of `crewship run`'s signal handling.
//
// The web chat's Stop button sends one `cancel_message` frame over the socket
// and then WAITS: the request is not the stop, the run's terminal `done` is
// (hooks/use-chat.ts stopGeneration — "A Stop request is not a server terminal
// acknowledgement"). Until that frame arrives the run is still spending, still
// writing files, still able to call tools. The CLI used to print `[cancelled]`
// the instant Ctrl-C was pressed, which claimed an outcome nobody had
// confirmed, and SIGTERM — what a supervisor, `timeout(1)` or a CI runner
// sends — skipped the cancel entirely and left the run going server-side.
//
// The server keys the cancel on (user, chat): internal/ws/client.go
// handleCancelMessage looks up cancelFns[userID+":"+chatID]. So this stops a
// run YOU started on that chat, from any socket — a second terminal, the
// browser's run seen from a shell — and is a silent no-op for a run another
// member started. --wait is how a caller tells those apart.

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// chatStopDefaultWait matches the web chat's own patience for a delivery
// confirmation (use-chat.ts, 30 s), so the two surfaces give up together.
const chatStopDefaultWait = 30 * time.Second

var chatStopCmd = &cobra.Command{
	Use:   "stop <chat-id>",
	Short: "Stop the agent run you started in a chat session",
	Long: `Ask the server to cancel the run in flight on a chat session — the CLI
equivalent of the chat's Stop button.

The cancel is scoped to you: it stops a run you started on this chat, from any
terminal or browser tab, and is ignored for a run another member started.

Without --wait the request is sent and the command returns. With --wait it
stays connected until the run's terminal event confirms the stop, and exits
non-zero if no confirmation arrives within --timeout (no run of yours was
active, or it belongs to someone else).

Examples:
  crewship chat stop c_abc123
  crewship chat stop c_abc123 --wait
  crewship chat stop c_abc123 --wait --timeout 2m --format json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		chatID := args[0]
		wait, _ := cmd.Flags().GetBool("wait")
		timeout, _ := cmd.Flags().GetDuration("timeout")
		if timeout <= 0 {
			timeout = chatStopDefaultWait
		}

		wsToken, err := cli.WSTokenFromServer(client)
		if err != nil {
			return fmt.Errorf("get WS token: %w", err)
		}
		ws, err := cli.NewWSClient(streamServerURL(), wsToken)
		if err != nil {
			return err
		}
		defer ws.Close()

		// Subscribe BEFORE cancelling: the terminal frame can follow the cancel
		// within milliseconds, and a subscription that lands after it has
		// nothing left to confirm.
		if wait {
			if err := ws.Subscribe("session:" + chatID); err != nil {
				return fmt.Errorf("subscribe: %w", err)
			}
		}
		if err := ws.CancelMessage(chatID); err != nil {
			return fmt.Errorf("send stop: %w", err)
		}

		status := "requested"
		if wait {
			fmt.Fprintf(os.Stderr, "%s[stopping…]%s waiting for the run to confirm\n", cli.Dim, cli.Reset)
			if err := waitForRunEnd(ws, timeout); err != nil {
				return err
			}
			status = "stopped"
		}

		out := map[string]string{"chat_id": chatID, "status": status}
		f := newFormatter()
		if !f.RoutesToHuman() {
			return f.Machine(out)
		}
		if wait {
			cli.PrintSuccess(fmt.Sprintf("Run in chat %s stopped.", chatID))
		} else {
			cli.PrintSuccess(fmt.Sprintf("Stop requested for chat %s (use --wait to confirm).", chatID))
		}
		return nil
	},
}

// errStopUnconfirmed is the --wait timeout. Its own value so a caller in this
// package (and its tests) can tell "no confirmation" from a transport failure.
var errStopUnconfirmed = errors.New("no run end was confirmed")

// waitForRunEnd reads the session channel until the run's terminal `done`.
// An `error` frame is not terminal on its own — the server always follows it
// with `done` (ws/client.go emits the pair) — so it is skipped like any other
// non-terminal event.
func waitForRunEnd(ws *cli.WSClient, timeout time.Duration) error {
	type read struct {
		msg *cli.WSMessage
		err error
	}
	reads := make(chan read, 1)
	go func() {
		for {
			msg, err := ws.ReadMessage()
			reads <- read{msg, err}
			if err != nil {
				return
			}
		}
	}()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	var closeReason string
	for {
		select {
		case <-deadline.C:
			return cli.WithExitCode(fmt.Errorf("%w within %s — no run of yours was active on this chat, or it was started by another member",
				errStopUnconfirmed, timeout), cli.ExitGeneric)
		case r := <-reads:
			if r.err != nil {
				if closeReason != "" {
					return fmt.Errorf("server rejected the connection: %s", closeReason)
				}
				return fmt.Errorf("ws read: %w", r.err)
			}
			if reason, ok := cli.CloseReason(r.msg); ok {
				closeReason = reason
				continue
			}
			ev, err := cli.ParseChatEvent(r.msg)
			if err != nil || ev == nil {
				continue
			}
			if ev.Type == "done" {
				return nil
			}
		}
	}
}

// runStop is the signal-driven stop of a run this process is streaming. One
// instance per streamed run; nil when the run was not started by this
// process's stream path (interactive mode keeps its own per-turn handler).
//
// First SIGINT/SIGTERM: send cancel_message, say "stopping…", keep reading so
// the run's own terminal frame is what ends the command. Second signal: the
// default handler is restored, so the process exits immediately — the escape
// hatch for a server that never answers. A stop that is not confirmed within
// grace closes the socket so the read loop returns an error instead of
// waiting forever.
type runStop struct {
	mu        sync.Mutex
	requested bool
	timedOut  bool
}

// activeRunStop is read by streamEvents to word its terminal line. Package
// level for the same reason as the AI-first latches in cmd_run_metadata.go:
// the CLI streams one run per process, and threading it through every helper
// signature would touch every caller for one bit of state.
var activeRunStop *runStop

// runStopGrace bounds how long a requested stop may stay unconfirmed. A var so
// tests can shorten it.
var runStopGrace = chatStopDefaultWait

func (s *runStop) wasRequested() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requested
}

func (s *runStop) didTimeOut() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.timedOut
}

// installRunStop wires SIGINT and SIGTERM to a confirmed stop of chatID. The
// returned func detaches the handler; call it when the stream ends.
func installRunStop(ws *cli.WSClient, chatID string) (*runStop, func()) {
	stop := &runStop{}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		select {
		case <-sig:
		case <-done:
			return
		}
		// Second signal → default action (terminate). Reset before sending so
		// an impatient second Ctrl-C is never swallowed.
		signal.Reset(syscall.SIGINT, syscall.SIGTERM)
		stop.mu.Lock()
		stop.requested = true
		stop.mu.Unlock()
		if err := ws.CancelMessage(chatID); err != nil {
			fmt.Fprintf(os.Stderr, "\n%s[stop failed]%s %v\n", cli.Red, cli.Reset, err)
			_ = ws.Close()
			return
		}
		fmt.Fprintf(os.Stderr, "\n%s[stopping…]%s waiting for the run to confirm (press Ctrl-C again to quit now)\n", cli.Yellow, cli.Reset)
		select {
		case <-time.After(runStopGrace):
			stop.mu.Lock()
			stop.timedOut = true
			stop.mu.Unlock()
			_ = ws.Close()
		case <-done:
		}
	}()
	var once sync.Once
	return stop, func() {
		once.Do(func() {
			signal.Stop(sig)
			close(done)
		})
	}
}

func init() {
	chatStopCmd.Flags().Bool("wait", false, "Stay connected until the run's terminal event confirms the stop")
	chatStopCmd.Flags().Duration("timeout", chatStopDefaultWait, "With --wait: give up after this long without confirmation")
	chatCmd.AddCommand(chatStopCmd)
}
