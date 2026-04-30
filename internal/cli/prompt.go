package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/term"
)

// PromptOptions controls how BuildPrompt assembles a final prompt
// from positional args, flags, stdin, and contextual sources (git, files, commands).
//
// All fields are optional. The Stdin* fields are injected so tests can
// substitute deterministic inputs without touching os.Stdin.
type PromptOptions struct {
	// Positional is the prompt-as-args slice (already split). Joined with spaces.
	Positional []string
	// PromptFlag is the --prompt value. Supports "@path" (read file) and "@-" (read stdin).
	PromptFlag string
	// AutoStdin enables auto-detection of piped stdin. If true and stdin is not a TTY,
	// stdin is appended to the prompt as context. Disabled when PromptFlag is "@-".
	AutoStdin bool

	// WithGitDiff appends `git diff` (working tree vs HEAD) as context.
	WithGitDiff bool
	// WithGitDiffStaged appends `git diff --staged` (index vs HEAD) as context.
	WithGitDiffStaged bool
	// WithGitLog appends `git log --oneline -20` as context.
	WithGitLog bool
	// WithGitStatus appends `git status -s` as context.
	WithGitStatus bool
	// WithFiles paths whose contents are appended as labelled fenced blocks.
	WithFiles []string
	// WithCmds shell commands whose stdout is appended as labelled fenced blocks.
	WithCmds []string

	// MaxContextBytes caps each appended context block. 0 means no cap. Defaults to 64 KiB.
	MaxContextBytes int

	// readFile, readStdin, runCmd, isStdinPipe are injection points for tests.
	readFile     func(string) ([]byte, error)
	readStdin    func() ([]byte, error)
	runCmd       func(name string, args ...string) ([]byte, error)
	isStdinPipe  func() bool
}

// BuildPrompt assembles the final prompt string from PromptOptions.
//
// Order of assembly:
//  1. Base prompt: PromptFlag (with @file/@- expansion) or joined Positional.
//  2. Stdin (if AutoStdin and stdin is a pipe), appended after a separator.
//  3. Context blocks: git diff/log/status, files, command outputs — each in a fenced block.
//
// Empty result is allowed (caller decides whether that's an error).
func BuildPrompt(opts PromptOptions) (string, error) {
	o := withDefaults(opts)

	base, stdinConsumed, err := resolveBase(o)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	sb.WriteString(base)

	if o.AutoStdin && !stdinConsumed && o.isStdinPipe() {
		data, err := o.readStdin()
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		appendBlock(&sb, "stdin", capBytes(string(data), o.MaxContextBytes))
	}

	if o.WithGitDiff {
		out, err := o.runCmd("git", "diff")
		if err != nil {
			return "", fmt.Errorf("git diff: %w", err)
		}
		appendBlock(&sb, "git diff", capBytes(string(out), o.MaxContextBytes))
	}
	if o.WithGitDiffStaged {
		out, err := o.runCmd("git", "diff", "--staged")
		if err != nil {
			return "", fmt.Errorf("git diff --staged: %w", err)
		}
		appendBlock(&sb, "git diff --staged", capBytes(string(out), o.MaxContextBytes))
	}
	if o.WithGitStatus {
		out, err := o.runCmd("git", "status", "-s")
		if err != nil {
			return "", fmt.Errorf("git status: %w", err)
		}
		appendBlock(&sb, "git status", capBytes(string(out), o.MaxContextBytes))
	}
	if o.WithGitLog {
		out, err := o.runCmd("git", "log", "--oneline", "-20")
		if err != nil {
			return "", fmt.Errorf("git log: %w", err)
		}
		appendBlock(&sb, "git log", capBytes(string(out), o.MaxContextBytes))
	}

	for _, p := range o.WithFiles {
		data, err := o.readFile(p)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", p, err)
		}
		appendBlock(&sb, "file: "+p, capBytes(string(data), o.MaxContextBytes))
	}

	for _, c := range o.WithCmds {
		out, err := o.runCmd("sh", "-c", c)
		if err != nil {
			return "", fmt.Errorf("run %q: %w", c, err)
		}
		appendBlock(&sb, "$ "+c, capBytes(string(out), o.MaxContextBytes))
	}

	return strings.TrimRight(sb.String(), "\n"), nil
}

func withDefaults(opts PromptOptions) PromptOptions {
	if opts.MaxContextBytes == 0 {
		opts.MaxContextBytes = 64 * 1024
	}
	if opts.readFile == nil {
		opts.readFile = os.ReadFile
	}
	if opts.readStdin == nil {
		opts.readStdin = func() ([]byte, error) { return io.ReadAll(os.Stdin) }
	}
	if opts.runCmd == nil {
		opts.runCmd = func(name string, args ...string) ([]byte, error) {
			return exec.Command(name, args...).Output()
		}
	}
	if opts.isStdinPipe == nil {
		opts.isStdinPipe = func() bool { return !term.IsTerminal(int(os.Stdin.Fd())) }
	}
	return opts
}

// resolveBase returns the base prompt and whether stdin was consumed by @-.
func resolveBase(o PromptOptions) (string, bool, error) {
	if o.PromptFlag != "" {
		if strings.HasPrefix(o.PromptFlag, "@") {
			ref := o.PromptFlag[1:]
			if ref == "-" {
				data, err := o.readStdin()
				if err != nil {
					return "", true, fmt.Errorf("read stdin: %w", err)
				}
				return strings.TrimRight(string(data), "\n"), true, nil
			}
			data, err := o.readFile(ref)
			if err != nil {
				return "", false, fmt.Errorf("read prompt file: %w", err)
			}
			return strings.TrimRight(string(data), "\n"), false, nil
		}
		return o.PromptFlag, false, nil
	}
	return strings.Join(o.Positional, " "), false, nil
}

// appendBlock writes a labelled fenced block separator to sb.
// Skipped silently if content is empty after trimming.
func appendBlock(sb *strings.Builder, label, content string) {
	content = strings.TrimRight(content, "\n")
	if content == "" {
		return
	}
	if sb.Len() > 0 {
		sb.WriteString("\n\n")
	}
	sb.WriteString("--- ")
	sb.WriteString(label)
	sb.WriteString(" ---\n")
	sb.WriteString(content)
}

// capBytes truncates content to max bytes, appending a marker when truncated.
// 0 means no cap.
func capBytes(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "\n... (truncated)"
}
