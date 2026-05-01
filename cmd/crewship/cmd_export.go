package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// exportCmd bundles a single run's complete artefact (prompt, response,
// journal entries scoped to the run, basic metadata) into a folder. One
// command instead of "fetch the chat, then the journal, then dump it
// somewhere" — the typical post-mortem workflow.
//
// Output layout:
//
//   <out>/
//     run.json       — runMetadata + window
//     prompt.md      — first user message recovered from chat
//     response.md    — concatenated assistant text from chat messages
//     messages.json  — full chat message list (raw)
//     journal.json   — journal entries (oldest-first)
//     timeline.txt   — same entries, human-readable
//
// We deliberately emit a folder rather than a tarball so users can grep
// it, edit it, and commit pieces selectively. Wrapping in `tar` is a
// trivial follow-up shell call: `tar czf run.tgz <out>/`.
var exportCmd = &cobra.Command{
	Use:   "export <run-id>",
	Short: "Bundle a run's prompt + response + journal into a folder",
	Long: `Fetch a run's chat history and journal entries, write them to
<out>/ alongside basic metadata. One-stop archival for handoffs,
post-mortems, or feeding a different LLM.

Examples:
  crewship export r_abc                          # writes ./run-r_abc/
  crewship export r_abc --out /tmp/post-mortem
  crewship export r_abc --no-journal             # skip journal pass`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		runID := args[0]

		out, _ := cmd.Flags().GetString("out")
		if out == "" {
			out = "./run-" + runID
		}
		if err := os.MkdirAll(out, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", out, err)
		}

		runMeta, err := fetchRun(client, runID)
		if err != nil {
			return err
		}
		windowStart, err := runWindowStart(client, runID)
		if err != nil {
			// Synthetic 1-hour window is a fallback for runs older than the
			// /api/v1/runs page horizon. Surface the warning so the operator
			// knows the journal slice in the bundle may be incomplete.
			fmt.Fprintf(os.Stderr,
				"%s[warn]%s could not resolve run window for %s; using last 1h: %v\n",
				cli.Yellow, cli.Reset, runID, err)
			windowStart = time.Now().Add(-1 * time.Hour)
		}

		// 1. metadata
		meta := map[string]any{
			"run_id":       runID,
			"agent_id":     runMeta.AgentID,
			"agent_slug":   runMeta.AgentSlug,
			"chat_id":      runMeta.ChatID,
			"window_start": windowStart.Format(time.RFC3339),
			"exported_at":  time.Now().UTC().Format(time.RFC3339),
		}
		if err := writeJSONFile(filepath.Join(out, "run.json"), meta); err != nil {
			return err
		}

		// 2. messages + prompt + response
		if runMeta.ChatID != "" {
			messages, err := fetchAllMessages(client, runMeta.ChatID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s[warn] could not fetch messages: %v%s\n", cli.Yellow, err, cli.Reset)
			} else {
				if err := writeJSONFile(filepath.Join(out, "messages.json"), messages); err != nil {
					return err
				}
				prompt, response := splitPromptResponse(messages)
				if prompt != "" {
					if err := os.WriteFile(filepath.Join(out, "prompt.md"), []byte(prompt+"\n"), 0o644); err != nil {
						return fmt.Errorf("write prompt.md: %w", err)
					}
				}
				if response != "" {
					if err := os.WriteFile(filepath.Join(out, "response.md"), []byte(response+"\n"), 0o644); err != nil {
						return fmt.Errorf("write response.md: %w", err)
					}
				}
			}
		}

		// 3. journal entries
		if skip, _ := cmd.Flags().GetBool("no-journal"); !skip {
			entries, err := fetchInspectEntries(client, runMeta.AgentID, windowStart, "")
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s[warn] could not fetch journal: %v%s\n", cli.Yellow, err, cli.Reset)
			} else {
				if err := writeJSONFile(filepath.Join(out, "journal.json"), entries); err != nil {
					return err
				}
				timeline := formatJournalTimeline(entries)
				if err := os.WriteFile(filepath.Join(out, "timeline.txt"), []byte(timeline), 0o644); err != nil {
					return fmt.Errorf("write timeline.txt: %w", err)
				}
			}
		}

		fmt.Fprintf(os.Stderr, "%s[exported run %s → %s]%s\n", cli.Dim, runID, out, cli.Reset)
		return nil
	},
}

// fetchAllMessages pulls the full message list for a chat (paginated up
// to a reasonable cap so a megachat doesn't blow memory).
func fetchAllMessages(client *cli.Client, chatID string) ([]map[string]any, error) {
	path := "/api/v1/chats/" + url.PathEscape(chatID) + "/messages?limit=500"
	var body struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := getJSON(client, path, &body); err != nil {
		return nil, err
	}
	return body.Messages, nil
}

// splitPromptResponse extracts the first user message and concatenates
// all subsequent assistant text. Multi-turn conversations are preserved
// in messages.json — these two are the at-a-glance artefacts.
func splitPromptResponse(messages []map[string]any) (prompt, response string) {
	gotUser := false
	var resp []byte
	for _, m := range messages {
		role, _ := m["role"].(string)
		content, _ := m["content"].(string)
		if !gotUser && (role == "user" || role == "human" || role == "Human") {
			prompt = content
			gotUser = true
			continue
		}
		if role == "assistant" || role == "model" {
			if len(resp) > 0 {
				resp = append(resp, '\n', '\n')
			}
			resp = append(resp, content...)
		}
	}
	return prompt, string(resp)
}

// formatJournalTimeline produces the same one-line-per-entry view as
// `crewship inspect` so the bundle is readable without re-running anything.
func formatJournalTimeline(entries []map[string]any) string {
	var sb []byte
	for _, e := range entries {
		ts, _ := e["ts"].(string)
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			ts = t.Format("2006-01-02 15:04:05")
		}
		entryType, _ := e["entry_type"].(string)
		severity, _ := e["severity"].(string)
		summary, _ := e["summary"].(string)
		line := fmt.Sprintf("%s  [%s/%s]  %s\n", ts, severity, entryType, summary)
		sb = append(sb, line...)
	}
	return string(sb)
}

// writeJSONFile marshals v with two-space indent and writes to path.
// Indented because the bundle is meant to be human-read or diffed in git.
func writeJSONFile(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func init() {
	exportCmd.Flags().String("out", "", "Output directory (default: ./run-<run-id>)")
	exportCmd.Flags().Bool("no-journal", false, "Skip journal entry export")
}
