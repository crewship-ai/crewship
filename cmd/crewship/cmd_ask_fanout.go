package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
)

// runFanout sends the same prompt to N agents in parallel and prints each
// agent's response sequentially under a labelled header.
//
// Design tradeoffs:
//   - Parallel sends, sequential display. Streaming output from N agents
//     simultaneously is unreadable; buffering each agent's text and printing
//     them in order trades real-time visibility for legibility. Use single-
//     agent `ask` when you need to watch tokens stream.
//   - One WS connection per agent. The hub already isolates by channel, so
//     multiplexing buys nothing and complicates the cancel/error path.
//   - First-failure does NOT abort the others. A common use case is "ask 3
//     agents, see who got it right" — losing 2 responses because 1 errored
//     would defeat the purpose. Failures are reported in their own slot.
func runFanout(server, wsToken string, agentsByID map[string]string, prompt string, quiet bool, md *cli.MarkdownRenderer, save *cli.AtomicFile, timeoutSecs int) error {
	if len(agentsByID) == 0 {
		return fmt.Errorf("no agents")
	}

	// Per-agent timeout + Ctrl-C: an agent whose WS hangs would otherwise
	// block wg.Wait() forever, freezing the CLI. Default 5 min when no
	// --timeout was passed; otherwise honour the user's value so
	// `crewship ask --agents ... --timeout 10` does what it says.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	timeout := 5 * time.Minute
	if timeoutSecs > 0 {
		timeout = time.Duration(timeoutSecs) * time.Second
	}
	ctx, cancelTimeout := context.WithTimeout(ctx, timeout)
	defer cancelTimeout()

	type result struct {
		slug string
		text string
		err  error
	}
	results := make(chan result, len(agentsByID))
	client := newAPIClient()

	var wg sync.WaitGroup
	for agentID, slug := range agentsByID {
		wg.Add(1)
		go func(agentID, slug string) {
			defer wg.Done()
			text, err := fanoutOne(ctx, client, server, wsToken, agentID, prompt)
			results <- result{slug: slug, text: text, err: err}
		}(agentID, slug)
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	// Drain results and bucket by slug so display order is deterministic
	// regardless of which agent finished first.
	collected := map[string]result{}
	for r := range results {
		collected[r.slug] = r
	}

	// Display in the order the slugs were originally given (map iteration
	// order would shuffle them per-run otherwise).
	slugs := make([]string, 0, len(agentsByID))
	for _, slug := range agentsByID {
		slugs = append(slugs, slug)
	}
	// Stable order: sort slugs alphabetically. Since agentsByID values may
	// repeat (shouldn't, but defensively) we de-dup.
	slugs = uniqueSorted(slugs)

	// saveErr captures the first write failure so a disk-full or permission
	// problem surfaces as a non-zero exit even when Commit happens to
	// succeed (e.g. tmpfs vs. final dir on different volumes).
	var saveErr error
	writeSave := func(s string) {
		if save == nil || saveErr != nil {
			return
		}
		if _, err := save.WriteString(s); err != nil {
			saveErr = fmt.Errorf("save write: %w", err)
		}
	}

	for i, slug := range slugs {
		if i > 0 {
			fmt.Println()
		}
		r := collected[slug]
		header := fmt.Sprintf("=== %s ===", slug)
		if !quiet {
			fmt.Printf("%s%s%s\n", cli.Bold, header, cli.Reset)
		}
		writeSave(header + "\n")

		// Print + save the partial text BEFORE the error footer — a late
		// read failure shouldn't throw away whatever the agent already
		// produced. Without this, `crewship ask --agents v,e --save out.md`
		// drops an agent's response if its WS dropped near the end.
		text := r.text
		if text != "" {
			writeSave(text)
			if !strings.HasSuffix(text, "\n") {
				writeSave("\n")
			}
			toPrint := text
			if md != nil {
				// Each agent's output gets its own renderer state — fenced blocks
				// don't bleed across the boundary.
				toPrint = cli.NewMarkdownRenderer().Render(text)
			}
			fmt.Print(toPrint)
			if !strings.HasSuffix(toPrint, "\n") {
				fmt.Println()
			}
		}
		if r.err != nil {
			fmt.Fprintf(os.Stderr, "%s[error]%s %v\n", cli.Red, cli.Reset, r.err)
		}
	}
	// All agents iterated — commit the save file. Even if individual agents
	// errored, the saved artefact captures every header + (partial) text we
	// printed, which is what the user just saw on screen. Surface the first
	// write error if any — Commit alone could otherwise mask earlier
	// truncation.
	if save != nil {
		if saveErr != nil {
			return saveErr
		}
		if err := save.Commit(); err != nil {
			return fmt.Errorf("save commit: %w", err)
		}
	}
	return nil
}

// fanoutOne creates a chat, sends the prompt, and returns the full text
// response (or an error). All run-level errors are returned to the caller
// — fan-out's display layer decides how to surface them.
//
// Honours `ctx`: if the parent fan-out context is cancelled (Ctrl-C or
// timeout) the goroutine wraps up promptly. WS read errors are now
// propagated to the caller with the partial text so a connection drop
// surfaces as a per-agent error instead of a silently-truncated success.
func fanoutOne(ctx context.Context, client *cli.Client, server, wsToken, agentID, prompt string) (string, error) {
	resp, err := client.WithContext(ctx).Post("/api/v1/agents/"+agentID+"/chats", map[string]string{
		"mode":   "CHAT",
		"origin": "CLI",
	})
	if err != nil {
		return "", fmt.Errorf("create chat: %w", err)
	}
	if err := cli.CheckError(resp); err != nil {
		return "", err
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := cli.ReadJSON(resp, &out); err != nil {
		return "", err
	}

	ws, err := cli.NewWSClient(server, wsToken)
	if err != nil {
		return "", fmt.Errorf("ws: %w", err)
	}
	defer ws.Close()

	if err := ws.Subscribe("session:" + out.ID); err != nil {
		return "", fmt.Errorf("subscribe: %w", err)
	}
	if err := ws.SendMessage("agent:"+agentID, out.ID, prompt); err != nil {
		return "", fmt.Errorf("send: %w", err)
	}

	// Bridge ctx → ws.Close() so a parent cancel unblocks the read loop.
	// Without this, ws.ReadMessage() blocks indefinitely on a hung agent
	// even though the goroutine was told to give up.
	go func() {
		<-ctx.Done()
		_ = ws.Close()
	}()

	var text strings.Builder
	for {
		msg, err := ws.ReadMessage()
		if err != nil {
			// If the cancel was the cause, surface that instead of a
			// generic "connection closed" — operator-facing clarity.
			if ctx.Err() != nil {
				return text.String(), fmt.Errorf("cancelled: %w", ctx.Err())
			}
			return text.String(), fmt.Errorf("read: %w", err)
		}
		event, err := cli.ParseChatEvent(msg)
		if err != nil || event == nil {
			continue
		}
		switch event.Type {
		case "text":
			text.WriteString(event.Content)
		case "error":
			return text.String(), fmt.Errorf("agent error: %s", event.Content)
		case "done":
			return text.String(), nil
		}
	}
}

// uniqueSorted returns the unique elements of `in` in alphabetical order.
// Used by the fan-out display so the output is deterministic regardless of
// goroutine scheduling.
func uniqueSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	// Manual sort to avoid pulling in sort just for this — the slice is
	// always tiny (number of agents the user typed). Insertion sort is fine.
	for i := 1; i < len(out); i++ {
		j := i
		for j > 0 && out[j-1] > out[j] {
			out[j-1], out[j] = out[j], out[j-1]
			j--
		}
	}
	return out
}
