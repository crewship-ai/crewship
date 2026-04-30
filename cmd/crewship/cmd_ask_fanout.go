package main

import (
	"fmt"
	"os"
	"strings"
	"sync"

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
func runFanout(server, wsToken string, agentsByID map[string]string, prompt string, quiet bool, md *cli.MarkdownRenderer, save *cli.AtomicFile) error {
	if len(agentsByID) == 0 {
		return fmt.Errorf("no agents")
	}

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
			text, err := fanoutOne(client, server, wsToken, agentID, prompt)
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

	for i, slug := range slugs {
		if i > 0 {
			fmt.Println()
		}
		r := collected[slug]
		header := fmt.Sprintf("=== %s ===", slug)
		if !quiet {
			fmt.Printf("%s%s%s\n", cli.Bold, header, cli.Reset)
		}
		if save != nil {
			_, _ = save.WriteString(header)
			_, _ = save.WriteString("\n")
		}

		if r.err != nil {
			fmt.Fprintf(os.Stderr, "%s[error]%s %v\n", cli.Red, cli.Reset, r.err)
			continue
		}

		text := r.text
		if save != nil {
			_, _ = save.WriteString(text)
			if !strings.HasSuffix(text, "\n") {
				_, _ = save.WriteString("\n")
			}
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
	// All agents iterated — commit the save file. Even if individual agents
	// errored, the saved artefact captures every header + (partial) text we
	// printed, which is what the user just saw on screen.
	if save != nil {
		if err := save.Commit(); err != nil {
			fmt.Fprintf(os.Stderr, "%s[save]%s commit failed: %v\n", cli.Yellow, cli.Reset, err)
		}
	}
	return nil
}

// fanoutOne creates a chat, sends the prompt, and returns the full text
// response (or an error). All run-level errors are returned to the caller
// — fan-out's display layer decides how to surface them.
func fanoutOne(client *cli.Client, server, wsToken, agentID, prompt string) (string, error) {
	resp, err := client.Post("/api/v1/agents/"+agentID+"/chats", map[string]string{
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

	var text strings.Builder
	for {
		msg, err := ws.ReadMessage()
		if err != nil {
			return text.String(), nil
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
