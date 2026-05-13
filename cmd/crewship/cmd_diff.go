package main

import (
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// diffCmd compares two persisted runs side-by-side.
//
// Distinct from `eval compare` (which re-runs an eval scenario on two
// tiers): this one takes two existing run-ids, fetches their state +
// the final assistant message of each linked chat, and renders a
// terse diff. Use cases:
//
//	- "did v2 of my routine actually fix the bug?" → diff before/after
//	- "what changed between Friday's prod run and Monday's regression?"
//	- "did the agent give two different answers to the same prompt?"
var diffCmd = &cobra.Command{
	Use:   "diff <run-a> <run-b>",
	Short: "Diff two existing runs (status, output, journal head)",
	Long: `Compare two runs by id and report what changed.

Examples:
  crewship diff r_abc r_def
  crewship diff r_abc r_def --format json
  crewship diff r_abc r_def --full  # show full output instead of head only`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}

		type sidePayload struct {
			Detail   *cli.RunDetail
			Messages []map[string]any
			Err      error
		}
		fetch := func(id string) sidePayload {
			d, err := client.GetRun(cmd.Context(), id)
			if err != nil {
				return sidePayload{Err: err}
			}
			out := sidePayload{Detail: d}
			if d.ChatID != nil && *d.ChatID != "" {
				path := "/api/v1/chats/" + url.PathEscape(*d.ChatID) + "/messages?limit=50"
				var body struct {
					Messages []map[string]any `json:"messages"`
				}
				if e := getJSON(client, path, &body); e == nil {
					out.Messages = body.Messages
				}
			}
			return out
		}

		var a, b sidePayload
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); a = fetch(args[0]) }()
		go func() { defer wg.Done(); b = fetch(args[1]) }()
		wg.Wait()

		if a.Err != nil {
			return fmt.Errorf("run-a %s: %w", args[0], a.Err)
		}
		if b.Err != nil {
			return fmt.Errorf("run-b %s: %w", args[1], b.Err)
		}

		full, _ := cmd.Flags().GetBool("full")
		aText := lastAssistantText(a.Messages, full)
		bText := lastAssistantText(b.Messages, full)

		f := newFormatter()
		if f.Format == "json" || f.Format == "yaml" || f.Format == "ndjson" {
			return f.Auto(map[string]any{
				"a":      a.Detail,
				"b":      b.Detail,
				"a_text": aText,
				"b_text": bText,
			}, nil, nil)
		}
		printRunDiff(args[0], args[1], a.Detail, b.Detail, aText, bText)
		return nil
	},
}

// lastAssistantText returns the last assistant message's content. With
// full=false, truncates to 1 KiB to keep the diff scannable; full=true
// returns the entire body.
func lastAssistantText(messages []map[string]any, full bool) string {
	for i := len(messages) - 1; i >= 0; i-- {
		role, _ := messages[i]["role"].(string)
		if strings.EqualFold(role, "assistant") {
			content, _ := messages[i]["content"].(string)
			if !full && len(content) > 1024 {
				return content[:1024] + "…"
			}
			return content
		}
	}
	return ""
}

// printRunDiff renders a colour-coded side-by-side header + a line
// diff of the assistant output. The diff is a minimal "old/new" hunk
// rather than full Myers — we don't need precision, just signal.
func printRunDiff(idA, idB string, a, b *cli.RunDetail, textA, textB string) {
	tag := func(s string) string {
		if s == "" {
			return "-"
		}
		return s
	}
	statusColor := func(s string) string {
		switch strings.ToUpper(s) {
		case "COMPLETED":
			return cli.Green + s + cli.Reset
		case "FAILED", "TIMEOUT":
			return cli.Red + s + cli.Reset
		case "CANCELLED":
			return cli.Yellow + s + cli.Reset
		}
		return s
	}
	fmt.Printf("%sRun A%s  %s\n", cli.Bold, cli.Reset, idA)
	fmt.Printf("  agent  : %s\n", tag(safeStr(a.AgentSlug)))
	fmt.Printf("  status : %s\n", statusColor(a.Status))
	fmt.Printf("  started: %s\n", tag(safeStr(a.StartedAt)))
	fmt.Printf("  done   : %s\n", tag(safeStr(a.FinishedAt)))
	fmt.Println()
	fmt.Printf("%sRun B%s  %s\n", cli.Bold, cli.Reset, idB)
	fmt.Printf("  agent  : %s\n", tag(safeStr(b.AgentSlug)))
	fmt.Printf("  status : %s\n", statusColor(b.Status))
	fmt.Printf("  started: %s\n", tag(safeStr(b.StartedAt)))
	fmt.Printf("  done   : %s\n", tag(safeStr(b.FinishedAt)))
	fmt.Println()

	fmt.Printf("%s── Output diff ──%s\n", cli.Bold, cli.Reset)
	printLineDiff(textA, textB)
}

// printLineDiff is a minimal LCS-free line-by-line diff: lines that
// differ are tagged with `-` (A) and `+` (B). It's intentionally not a
// real diff — for run comparisons users want the gist, not patch-
// applicable hunks.
func printLineDiff(a, b string) {
	aLines := strings.Split(a, "\n")
	bLines := strings.Split(b, "\n")
	max := len(aLines)
	if len(bLines) > max {
		max = len(bLines)
	}
	for i := 0; i < max; i++ {
		var la, lb string
		if i < len(aLines) {
			la = aLines[i]
		}
		if i < len(bLines) {
			lb = bLines[i]
		}
		if la == lb {
			fmt.Printf("  %s\n", la)
			continue
		}
		if la != "" {
			fmt.Printf("%s- %s%s\n", cli.Red, la, cli.Reset)
		}
		if lb != "" {
			fmt.Printf("%s+ %s%s\n", cli.Green, lb, cli.Reset)
		}
	}
}

// safeStr dereferences a *string or returns "" if nil.
func safeStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func init() {
	diffCmd.Flags().Bool("full", false, "Show full output (no 1 KiB truncation)")
	rootCmd.AddCommand(diffCmd)
}
