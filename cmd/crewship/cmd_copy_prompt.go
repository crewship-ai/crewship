package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// copyPromptCmd recovers the first user prompt from a previous run and
// writes it to stdout — or, with --clipboard, into the system clipboard.
//
// Why a top-level command rather than a flag on retry: copy-prompt is
// the "I want to tweak this and rerun" workflow that doesn't end with a
// network round-trip. Users routinely want to grab a past prompt, edit
// it in their editor, and re-issue. Having it as a separate command keeps
// retry's mental model ("rerun this exactly") clean.
//
// `--clipboard` mirrors the same helper detection as --paste: pbcopy on
// macOS, wl-copy on Wayland, xclip on X11, xsel as last resort. Falls
// through to a clear error on headless servers so the user can install
// what they need.
var copyPromptCmd = &cobra.Command{
	Use:   "copy-prompt <run-id>",
	Short: "Recover the original prompt of a previous run",
	Long: `Print or copy the first user prompt from a previous run's chat. Use
this when you want to edit a past prompt and re-issue it.

Examples:
  crewship copy-prompt r_abc                  # write to stdout
  crewship copy-prompt r_abc --clipboard      # copy via pbcopy/wl-copy/xclip
  crewship copy-prompt r_abc | pbcopy         # alternate route on macOS
  crewship copy-prompt r_abc > prompt.txt     # save to file for editing

To re-run with the recovered prompt, use:
  crewship retry r_abc                        # same agent + prompt, new chat
  crewship retry r_abc --new-prompt "tweaked" # same agent, edited prompt`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		runID := args[0]
		client := newAPIClient()

		runMeta, err := fetchRun(client, runID)
		if err != nil {
			return err
		}
		if runMeta.ChatID == "" {
			return fmt.Errorf("run %s has no chat_id", runID)
		}
		prompt := fetchFirstUserPrompt(client, runMeta.ChatID)
		if prompt == "" {
			return fmt.Errorf("could not recover prompt for run %s", runID)
		}

		if clip, _ := cmd.Flags().GetBool("clipboard"); clip {
			if err := writeClipboard(prompt); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "%s[copied %d chars to clipboard]%s\n",
				cli.Dim, len(prompt), cli.Reset)
			return nil
		}
		fmt.Print(prompt)
		// Mirror the convention used elsewhere — preserve a trailing newline if
		// the prompt already had one, otherwise add one so shells display cleanly.
		if len(prompt) > 0 && prompt[len(prompt)-1] != '\n' {
			fmt.Println()
		}
		return nil
	},
}

// writeClipboard pipes data into the first available system clipboard
// helper. Counterpart to readClipboard in internal/cli/prompt.go.
//
// We deliberately keep this in cmd/ rather than internal/cli so the cli
// package stays free of process-spawning utilities — read uses exec
// because there's no good Go-only path on Wayland; write has the same
// constraint.
func writeClipboard(s string) error {
	candidates := []struct {
		name string
		args []string
	}{
		{"pbcopy", nil},
		{"wl-copy", nil},
		{"xclip", []string{"-selection", "clipboard"}},
		{"xsel", []string{"--clipboard", "--input"}},
	}
	for _, c := range candidates {
		if _, err := exec.LookPath(c.name); err != nil {
			continue
		}
		cmd := exec.Command(c.name, c.args...)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return fmt.Errorf("%s stdin: %w", c.name, err)
		}
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("%s start: %w", c.name, err)
		}
		_, _ = stdin.Write([]byte(s))
		_ = stdin.Close()
		return cmd.Wait()
	}
	return fmt.Errorf("no clipboard helper found (install pbcopy/wl-copy/xclip/xsel)")
}

func init() {
	copyPromptCmd.Flags().Bool("clipboard", false, "Copy to system clipboard instead of stdout")
}
