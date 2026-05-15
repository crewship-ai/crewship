package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// promptCmd is a local, server-free prompt library. Save common prompts
// once and reach for them by name in `crewship ask --prompt @prompt:foo`
// or via `crewship prompt use foo | crewship ask`.
//
// Why local-only:
//   - No round-trip → instant.
//   - User's own keep — the workspace can move (dev → prod), the prompt
//     library follows the user's machine.
//   - Plain markdown files in ~/.crewship/prompts/ → portable, grep-able,
//     trivially shared via dotfile repos.
//
// Naming: filenames `<name>.md` (extension auto-added/stripped). Names
// must be filesystem-safe — we restrict to a permissive but bounded
// alphabet to prevent path traversal.
var promptCmd = &cobra.Command{
	Use:   "prompt",
	Short: "Manage a local library of reusable prompts",
	Long: `Save, list, view, edit, and delete prompts kept on disk under
~/.crewship/prompts/<name>.md. Pure local — no server round-trip.

Example workflow:

  crewship prompt save review-go < ~/team/prompts/review.md
  crewship prompt list
  crewship prompt use review-go | crewship ask
  crewship prompt edit review-go     # opens in $EDITOR

Combine with --prompt @<file> on ask/run for re-use:
  crewship ask --prompt @$(crewship prompt path review-go)`,
}

var promptListCmd = &cobra.Command{
	Use:   "list",
	Short: "List saved prompts with size and last-modified time",
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := promptDir()
		if err != nil {
			return err
		}
		entries, err := os.ReadDir(dir)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		type row struct {
			Name string `json:"name"`
			Size int64  `json:"size_bytes"`
			Time string `json:"modified"`
		}
		var rows []row
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			rows = append(rows, row{
				Name: strings.TrimSuffix(e.Name(), ".md"),
				Size: info.Size(),
				Time: info.ModTime().Format("2006-01-02 15:04"),
			})
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })

		f := newFormatter()
		switch f.Format {
		case "json":
			return f.JSON(rows)
		case "yaml":
			return f.YAML(rows)
		case "quiet":
			// Quiet mode = names only, one per line — handy for shell pipelines.
			for _, r := range rows {
				fmt.Println(r.Name)
			}
			return nil
		}
		if len(rows) == 0 {
			fmt.Printf("%sNo prompts saved.%s  Try: crewship prompt save <name>\n", cli.Dim, cli.Reset)
			return nil
		}
		fmt.Printf("%s%-30s  %10s  %s%s\n", cli.Bold, "NAME", "SIZE", "MODIFIED", cli.Reset)
		for _, r := range rows {
			fmt.Printf("%-30s  %10d  %s\n", r.Name, r.Size, r.Time)
		}
		return nil
	},
}

var promptSaveCmd = &cobra.Command{
	Use:   "save <name>",
	Short: "Save a prompt from stdin or --content to ~/.crewship/prompts/<name>.md",
	Long: `Save a prompt to the local library. Content comes from --content
flag, --file, or stdin (in that order of precedence).

Examples:
  crewship prompt save review --content "Review this diff:"
  crewship prompt save review --file ~/notes/review.md
  cat review.md | crewship prompt save review`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if err := validatePromptName(name); err != nil {
			return err
		}
		path, err := promptPath(name)
		if err != nil {
			return err
		}
		// Auto-create the prompts directory on first use.
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return fmt.Errorf("create prompts dir: %w", err)
		}

		content, _ := cmd.Flags().GetString("content")
		fileFlag, _ := cmd.Flags().GetString("file")

		var data []byte
		switch {
		case content != "":
			data = []byte(content)
		case fileFlag != "":
			data, err = os.ReadFile(fileFlag)
			if err != nil {
				return fmt.Errorf("read --file: %w", err)
			}
		default:
			if term.IsTerminal(int(os.Stdin.Fd())) {
				return fmt.Errorf("no content: pass --content, --file, or pipe content via stdin")
			}
			data, err = io.ReadAll(os.Stdin)
			if err != nil {
				return fmt.Errorf("read stdin: %w", err)
			}
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		fmt.Fprintf(os.Stderr, "%s[saved %d bytes → %s]%s\n",
			cli.Dim, len(data), path, cli.Reset)
		return nil
	},
}

var promptUseCmd = &cobra.Command{
	Use:   "use <name>",
	Short: "Print a saved prompt to stdout (pipe into ask/run)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := promptPath(args[0])
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return suggestSimilarPrompt(args[0], err)
		}
		_, err = os.Stdout.Write(data)
		return err
	},
}

var promptPathCmd = &cobra.Command{
	Use:   "path <name>",
	Short: "Print the filesystem path of a saved prompt",
	Long: `Useful for shell composition with --prompt @<path>:

  crewship ask --prompt @"$(crewship prompt path review)"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := promptPath(args[0])
		if err != nil {
			return err
		}
		if _, err := os.Stat(path); err != nil {
			return suggestSimilarPrompt(args[0], err)
		}
		fmt.Println(path)
		return nil
	},
}

var promptEditCmd = &cobra.Command{
	Use:   "edit <name>",
	Short: "Open a prompt in $EDITOR (creates if missing)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if err := validatePromptName(name); err != nil {
			return err
		}
		path, err := promptPath(name)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return fmt.Errorf("create prompts dir: %w", err)
		}
		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = "vi" // POSIX-mandated fallback; almost always present.
		}
		// $EDITOR commonly carries arguments — `code -w`, `vim -c "set wrap"`,
		// `emacsclient -t`. exec.Command(editor, path) would treat the whole
		// string as the executable name and fail. strings.Fields splits on
		// any whitespace so the first token becomes the binary and the rest
		// become leading args, with the file path appended.
		parts := strings.Fields(editor)
		if len(parts) == 0 {
			return fmt.Errorf("$EDITOR is empty after whitespace split")
		}
		// `args` is the cobra positional slice — use a different name here.
		editorArgs := append(parts[1:], path)
		c := exec.Command(parts[0], editorArgs...)
		c.Stdin = os.Stdin
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		return c.Run()
	},
}

var promptDeleteCmd = &cobra.Command{
	Use:     "delete <name>",
	Aliases: []string{"rm"},
	Short:   "Remove a saved prompt",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := promptPath(args[0])
		if err != nil {
			return err
		}
		if err := os.Remove(path); err != nil {
			return suggestSimilarPrompt(args[0], err)
		}
		fmt.Fprintf(os.Stderr, "%s[deleted %s]%s\n", cli.Dim, path, cli.Reset)
		return nil
	},
}

// validatePromptName guards against path traversal and empty/bizarre
// inputs. We accept letters, digits, dash, underscore, and dot — enough
// for natural names like "review-go" or "v1.2-deploy" without opening
// "../etc/passwd"-style escapes.
func validatePromptName(name string) error {
	if name == "" {
		return fmt.Errorf("name required")
	}
	if len(name) > 64 {
		return fmt.Errorf("name too long (max 64)")
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return fmt.Errorf("invalid character %q in name (allowed: a-z A-Z 0-9 - _ .)", r)
		}
	}
	if name == "." || name == ".." || strings.HasPrefix(name, ".") {
		return fmt.Errorf("name cannot start with %q or be %q / %q", ".", ".", "..")
	}
	return nil
}

// promptDir returns ~/.crewship/prompts (without creating it).
func promptDir() (string, error) {
	dir, err := cli.DefaultConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "prompts"), nil
}

// promptPath returns the absolute path to a prompt by name. validatePromptName
// is run inside; the result is always within promptDir() so a callers can't
// trick us into reading arbitrary files even if they bypass the validation.
func promptPath(name string) (string, error) {
	if err := validatePromptName(name); err != nil {
		return "", err
	}
	dir, err := promptDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name+".md"), nil
}

// suggestSimilarPrompt enriches a not-found error with did-you-mean
// suggestions drawn from the prompt library. Only kicks in for ENOENT
// — non-existence errors. Permission denied / I/O errors propagate
// through unchanged so the user isn't misled by a "not found" message
// when the real problem is `chmod 000` on the prompts directory.
//
// Reuses nearestSlugs since the matching pattern is identical to
// agent-slug lookups.
func suggestSimilarPrompt(name string, baseErr error) error {
	if !os.IsNotExist(baseErr) {
		return baseErr
	}
	dir, err := promptDir()
	if err != nil {
		return baseErr
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Promptdir itself isn't readable — propagate the underlying error
		// rather than dressing it up as "not found".
		if os.IsNotExist(err) {
			return fmt.Errorf("prompt %q not found (no prompts saved yet — try `crewship prompt save`)", name)
		}
		return fmt.Errorf("read prompts dir: %w", err)
	}
	available := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			available = append(available, strings.TrimSuffix(e.Name(), ".md"))
		}
	}
	if len(available) == 0 {
		return fmt.Errorf("prompt %q not found (no prompts saved yet — try `crewship prompt save`)", name)
	}
	if hits := nearestSlugs(name, available, 3); len(hits) > 0 {
		return fmt.Errorf("prompt %q not found. Did you mean: %s?", name, strings.Join(hits, ", "))
	}
	return fmt.Errorf("prompt %q not found. Available: %s",
		name, strings.Join(truncateList(available, 8), ", "))
}

func init() {
	promptSaveCmd.Flags().String("content", "", "Inline prompt content (overrides stdin and --file)")
	promptSaveCmd.Flags().String("file", "", "Read content from this file instead of stdin")

	promptCmd.AddCommand(promptListCmd)
	promptCmd.AddCommand(promptSaveCmd)
	promptCmd.AddCommand(promptUseCmd)
	promptCmd.AddCommand(promptPathCmd)
	promptCmd.AddCommand(promptEditCmd)
	promptCmd.AddCommand(promptDeleteCmd)
}
