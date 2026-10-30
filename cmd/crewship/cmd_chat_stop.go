package main

// `crewship chat stop` and signal handling require a private cancel_result
// correlated to their request ID. A shared chat's ordinary done does not prove
// whose run finished or whether cancellation caused its end.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
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
equivalent of the chat's Stop button. This targets ordinary human chat sends;
native Craft and Mooring tasks use their separate task cancellation controls.

The cancel is scoped to you: it stops a run you started on this chat, from any
terminal or browser tab, and is ignored for a run another member started.

Without --wait the request is sent and the command returns. With --wait it
stays connected until the server confirms cancellation of your exact request, and exits
non-zero if no confirmation arrives within --timeout (no run of yours was
active, or it belongs to someone else).

Confirmation needs a server with cancel confirmation (cancel_accepted /
cancel_result). Against an older server, once the run's done arrives without
an acknowledgement the command reports "run ended; cancellation not confirmed
by server" and exits 9.

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
		requestID := uuid.NewString()
		if err := ws.CancelMessageWithID(chatID, requestID); err != nil {
			return fmt.Errorf("send stop: %w", err)
		}

		status := "requested"
		if wait {
			fmt.Fprintf(os.Stderr, "%s[stopping…]%s waiting for the run to confirm\n", cli.Dim, cli.Reset)
			if err := waitForRunEnd(ws, chatID, requestID, timeout); err != nil {
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
var errStopUnconfirmed = errors.New("no cancellation was confirmed")

// errStopEndedUnconfirmed is the degraded outcome against a server that
// predates cancel confirmation: a run on the chat ended (`done`) after the
// stop was sent, but the server never acknowledged the request
// (`cancel_accepted`) nor answered it (`cancel_result`). The run is over; why
// it ended is unknown, so it is neither reported as stopped nor as a hang.
var errStopEndedUnconfirmed = errors.New("run ended; cancellation not confirmed by server (server too old?)")

// exitStopEndedUnconfirmed is the exit code of errStopEndedUnconfirmed, for
// `chat stop --wait` and a signal-stopped `run`. Command-specific, like
// `crewship wait`'s run-outcome codes; documented in docs/cli/chat.mdx.
const exitStopEndedUnconfirmed = 9

func stopEndedUnconfirmedError() error {
	return cli.WithExitCode(errStopEndedUnconfirmed, exitStopEndedUnconfirmed)
}

// stopResultGrace is how long a `done` observed after an unacknowledged stop
// waits for a late cancel_accepted/cancel_result before the outcome degrades
// to errStopEndedUnconfirmed. A current server acknowledges the request as
// soon as it reads it and queues its result right after the run's own done,
// so this only expires against an older server. A var so tests can shorten it.
var stopResultGrace = 3 * time.Second

// waitForRunEnd requires the private result of this request. Shared done and
// unrelated request IDs cannot confirm cancellation.
func waitForRunEnd(ws *cli.WSClient, chatID, requestID string, timeout time.Duration) error {
	type read struct {
		msg *cli.WSMessage
		err error
	}
	reads := make(chan read, 1)
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			msg, err := ws.ReadMessage()
			select {
			case reads <- read{msg, err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	var closeReason string
	var accepted, sawDone bool
	var legacy <-chan time.Time
	for {
		select {
		case <-legacy:
			return stopEndedUnconfirmedError()
		case <-deadline.C:
			if sawDone && !accepted {
				return stopEndedUnconfirmedError()
			}
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
			if cancellationAccepted(r.msg, chatID, requestID) {
				accepted, legacy = true, nil
				continue
			}
			if matched, cancelled := cancellationResult(r.msg, chatID, requestID); matched {
				if !cancelled {
					return fmt.Errorf("%w: server did not confirm cancellation of an active run of yours", errStopUnconfirmed)
				}
				return nil
			}
			if ev, err := cli.ParseChatEvent(r.msg); err == nil && ev != nil && ev.Type == "done" && !accepted && legacy == nil {
				// Possibly an older server: wait briefly for its answer,
				// then degrade explicitly instead of waiting out --timeout.
				sawDone = true
				legacy = time.After(stopResultGrace)
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
	chatID    string
	requestID string
	// accepted: the server acknowledged this request (cancel_accepted), so
	// it supports confirmation and a cancel_result will follow.
	accepted bool
	// sawDone: a done arrived on the chat after the stop was requested.
	sawDone bool
	// closer ends a blocking read loop; legacy is its pending timer.
	closer func()
	legacy *time.Timer
}

// noteAccepted records msg when it is this request's cancel_accepted.
func (s *runStop) noteAccepted(msg *cli.WSMessage) bool {
	if s == nil || !cancellationAccepted(msg, s.chatID, s.requestID) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accepted = true
	if s.legacy != nil {
		s.legacy.Stop()
		s.legacy = nil
	}
	return true
}

// noteDone records a done seen after the stop request. Without an
// acknowledgement the server may predate cancel confirmation; when a closer
// is set, the read loop is ended after stopResultGrace so the caller reports
// errStopEndedUnconfirmed instead of waiting out runStopGrace.
func (s *runStop) noteDone() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sawDone = true
	if s.accepted || s.legacy != nil || s.closer == nil {
		return
	}
	s.legacy = time.AfterFunc(stopResultGrace, s.closer)
}

// endedUnconfirmed reports the degraded outcome: the run ended after the stop
// request, and the server never acknowledged the request.
func (s *runStop) endedUnconfirmed() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requested && s.sawDone && !s.accepted
}

func (s *runStop) stopLegacyTimer() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.legacy != nil {
		s.legacy.Stop()
		s.legacy = nil
	}
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
	stop := &runStop{chatID: chatID, requestID: uuid.NewString(), closer: func() { _ = ws.Close() }}
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
		if err := ws.CancelOwnMessageWithID(chatID, stop.requestID); err != nil {
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
			stop.stopLegacyTimer()
		})
	}
}

func init() {
	chatStopCmd.Flags().Bool("wait", false, "Stay connected until the server confirms the stop")
	chatStopCmd.Flags().Duration("timeout", chatStopDefaultWait, "With --wait: give up after this long without confirmation")
	chatCmd.AddCommand(chatStopCmd)
}

// cancellationResult accepts only the private answer for this exact request.
func cancellationResult(msg *cli.WSMessage, chatID, requestID string) (bool, bool) {
	if msg.Type != "cancel_result" || requestID == "" {
		return false, false
	}
	var result struct {
		ChatID    string `json:"session_id" yaml:"session_id"`
		RequestID string `json:"request_id" yaml:"request_id"`
		Cancelled bool   `json:"cancelled" yaml:"cancelled"`
	}
	if json.Unmarshal(msg.Payload, &result) != nil || result.ChatID != chatID || result.RequestID != requestID {
		return false, false
	}
	return true, result.Cancelled
}

// cancellationAccepted reports this exact request's acknowledgement: the
// server registered it and will answer with a cancel_result.
func cancellationAccepted(msg *cli.WSMessage, chatID, requestID string) bool {
	if msg == nil || msg.Type != "cancel_accepted" || requestID == "" {
		return false
	}
	var ack struct {
		ChatID    string `json:"session_id" yaml:"session_id"`
		RequestID string `json:"request_id" yaml:"request_id"`
	}
	return json.Unmarshal(msg.Payload, &ack) == nil && ack.ChatID == chatID && ack.RequestID == requestID
}
