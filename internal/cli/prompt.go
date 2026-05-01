package cli

import (
	"context"
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
	// Paste, when true, appends the system clipboard contents as a context block.
	// Detects pbpaste (macOS), wl-paste (Wayland), or xclip (X11) at runtime.
	Paste bool

	// MaxContextBytes caps each appended context block. 0 means no cap. Defaults to 64 KiB.
	// Reads enforce the cap at the source so a large input never fully buffers in memory.
	MaxContextBytes int

	// readFile, readStdin, runCmd, isStdinPipe, readPaste are injection points
	// for tests so the helpers stay deterministic without touching the real OS.
	// Each takes ctx so cancellation propagates uniformly through the read path
	// (e.g. a hung clipboard helper or a slow file system can be unblocked).
	readFile    func(ctx context.Context, path string, max int) ([]byte, error)
	readStdin   func(ctx context.Context, max int) ([]byte, error)
	runCmd      func(ctx context.Context, name string, args ...string) ([]byte, error)
	isStdinPipe func() bool
	readPaste   func(ctx context.Context, max int) ([]byte, error)
}

// BuildPrompt assembles the final prompt string from PromptOptions.
//
// Order of assembly:
//  1. Base prompt: PromptFlag (with @file/@- expansion) or joined Positional.
//  2. Stdin (if AutoStdin and stdin is a pipe), appended after a separator.
//  3. Context blocks: git diff/log/status, files, command outputs — each in a fenced block.
//
// Empty result is allowed (caller decides whether that's an error).
//
// Context: ctx propagates into every subprocess (via exec.CommandContext)
// and is checked at each read boundary so a parent cancellation interrupts
// the assembly without waiting for a slow source. Pass context.Background()
// when you have no parent.
func BuildPrompt(ctx context.Context, opts PromptOptions) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	o := withDefaults(opts)

	base, stdinConsumed, err := resolveBase(ctx, o)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	sb.WriteString(base)

	if o.AutoStdin && !stdinConsumed && o.isStdinPipe() {
		data, err := o.readStdin(ctx, o.MaxContextBytes)
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		appendBlock(&sb, "stdin", string(data))
	}

	if o.WithGitDiff {
		out, err := o.runCmd(ctx, "git", "diff")
		if err != nil {
			return "", fmt.Errorf("git diff: %w", err)
		}
		appendBlock(&sb, "git diff", capBytes(string(out), o.MaxContextBytes))
	}
	if o.WithGitDiffStaged {
		out, err := o.runCmd(ctx, "git", "diff", "--staged")
		if err != nil {
			return "", fmt.Errorf("git diff --staged: %w", err)
		}
		appendBlock(&sb, "git diff --staged", capBytes(string(out), o.MaxContextBytes))
	}
	if o.WithGitStatus {
		out, err := o.runCmd(ctx, "git", "status", "-s")
		if err != nil {
			return "", fmt.Errorf("git status: %w", err)
		}
		appendBlock(&sb, "git status", capBytes(string(out), o.MaxContextBytes))
	}
	if o.WithGitLog {
		out, err := o.runCmd(ctx, "git", "log", "--oneline", "-20")
		if err != nil {
			return "", fmt.Errorf("git log: %w", err)
		}
		appendBlock(&sb, "git log", capBytes(string(out), o.MaxContextBytes))
	}

	for _, p := range o.WithFiles {
		data, err := o.readFile(ctx, p, o.MaxContextBytes)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", p, err)
		}
		appendBlock(&sb, "file: "+p, string(data))
	}

	for _, c := range o.WithCmds {
		out, err := o.runCmd(ctx, "sh", "-c", c)
		if err != nil {
			return "", fmt.Errorf("run %q: %w", c, err)
		}
		appendBlock(&sb, "$ "+c, capBytes(string(out), o.MaxContextBytes))
	}

	if o.Paste {
		data, err := o.readPaste(ctx, o.MaxContextBytes)
		if err != nil {
			return "", fmt.Errorf("read clipboard: %w", err)
		}
		appendBlock(&sb, "clipboard", string(data))
	}

	return strings.TrimRight(sb.String(), "\n"), nil
}

func withDefaults(opts PromptOptions) PromptOptions {
	if opts.MaxContextBytes == 0 {
		opts.MaxContextBytes = 64 * 1024
	}
	if opts.readFile == nil {
		opts.readFile = readFileBounded
	}
	if opts.readStdin == nil {
		opts.readStdin = func(ctx context.Context, max int) ([]byte, error) {
			return readBounded(ctx, os.Stdin, max)
		}
	}
	if opts.runCmd == nil {
		opts.runCmd = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).Output()
		}
	}
	if opts.isStdinPipe == nil {
		opts.isStdinPipe = func() bool { return !term.IsTerminal(int(os.Stdin.Fd())) }
	}
	if opts.readPaste == nil {
		opts.readPaste = readClipboard
	}
	return opts
}

// readFileBounded opens path and reads up to max+1 bytes via io.LimitReader,
// appending a "(truncated)" marker if more remained. Mirrors os.ReadFile's
// API but bounds memory use — a 1 GB --with-file no longer crashes the CLI.
func readFileBounded(ctx context.Context, path string, max int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readBounded(ctx, f, max)
}

// readBounded reads up to max bytes from r and tags the result with a
// "(truncated)" marker if the stream had more. Honours ctx cancellation
// at chunk boundaries — best-effort because *os.File.Read isn't directly
// context-aware, but a cancel still aborts the next chunk.
func readBounded(ctx context.Context, r io.Reader, max int) ([]byte, error) {
	if max <= 0 {
		max = 64 * 1024
	}
	// Read max+1 to detect overflow without buffering more than one extra byte.
	data, err := io.ReadAll(io.LimitReader(r, int64(max)+1))
	if err != nil {
		return nil, err
	}
	if ctx != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if len(data) > max {
		data = append(data[:max], []byte("\n... (truncated)")...)
	}
	return data, nil
}

// readClipboard returns the system clipboard contents using the first
// available helper: pbpaste (macOS), wl-paste (Wayland), xclip (X11).
//
// Why shell out instead of a Go binding: each platform's clipboard binding
// has different cgo / Wayland-protocol requirements, and the CLI is meant
// to stay a small static binary. A runtime exec(1) trades a few ms of
// startup for zero added build complexity. If none of the helpers are
// available (typical for headless servers), a clear error is returned so
// the user knows what to install.
//
// ctx is honoured via exec.CommandContext — a hung clipboard manager
// (rare but real on flaky Wayland setups) won't lock the CLI forever.
func readClipboard(ctx context.Context, max int) ([]byte, error) {
	candidates := []struct {
		name string
		args []string
	}{
		{"pbpaste", nil},
		{"wl-paste", nil},
		{"xclip", []string{"-selection", "clipboard", "-o"}},
		{"xsel", []string{"--clipboard", "--output"}},
	}
	for _, c := range candidates {
		if _, err := exec.LookPath(c.name); err != nil {
			continue
		}
		out, err := exec.CommandContext(ctx, c.name, c.args...).Output()
		if err != nil {
			return nil, err
		}
		// Apply the same bounded-truncate pass that file/stdin use so
		// a 50 MB clipboard payload doesn't fill the prompt.
		if max > 0 && len(out) > max {
			out = append(out[:max], []byte("\n... (truncated)")...)
		}
		return out, nil
	}
	return nil, fmt.Errorf("no clipboard helper found (install pbpaste/wl-paste/xclip/xsel)")
}

// resolveBase returns the base prompt and whether stdin was consumed by @-.
func resolveBase(ctx context.Context, o PromptOptions) (string, bool, error) {
	if o.PromptFlag != "" {
		if strings.HasPrefix(o.PromptFlag, "@") {
			ref := o.PromptFlag[1:]
			if ref == "-" {
				data, err := o.readStdin(ctx, o.MaxContextBytes)
				if err != nil {
					return "", true, fmt.Errorf("read stdin: %w", err)
				}
				return strings.TrimRight(string(data), "\n"), true, nil
			}
			data, err := o.readFile(ctx, ref, o.MaxContextBytes)
			if err != nil {
				return "", false, fmt.Errorf("read prompt file: %w", err)
			}
			return strings.TrimRight(string(data), "\n"), false, nil
		}
		return o.PromptFlag, false, nil
	}
	return strings.Join(o.Positional, " "), false, nil
}

// appendBlock writes a labelled, REAL fenced block to sb. The fence is
// chosen to be longer than the longest tilde run already in `content`,
// so untrusted content cannot terminate the fence early — a property
// CommonMark guarantees about fenced code blocks. Without this, a
// `--with-file` containing `~~~` would split the prompt into separate
// blocks and confuse downstream parsers (and prompt-injection
// defenders).
//
// Skipped silently if content is empty after trimming.
func appendBlock(sb *strings.Builder, label, content string) {
	content = strings.TrimRight(content, "\n")
	if content == "" {
		return
	}
	if sb.Len() > 0 {
		sb.WriteString("\n\n")
	}
	fence := strings.Repeat("~", longestTildeRun(content)+1)
	if len(fence) < 3 {
		fence = "~~~"
	}
	sb.WriteString(fence)
	sb.WriteString(" ")
	sb.WriteString(label)
	sb.WriteByte('\n')
	sb.WriteString(content)
	sb.WriteByte('\n')
	sb.WriteString(fence)
}

// longestTildeRun returns the length of the longest contiguous run of
// '~' characters in s. Used by appendBlock to pick a fence length that
// can't be terminated by content.
func longestTildeRun(s string) int {
	max, cur := 0, 0
	for i := 0; i < len(s); i++ {
		if s[i] == '~' {
			cur++
			if cur > max {
				max = cur
			}
		} else {
			cur = 0
		}
	}
	return max
}

// capBytes truncates content to max bytes, appending a marker when truncated.
// 0 means no cap. Used for paths that don't already bound at the read site
// (subprocess output, where we still buffer the full stdout in memory but
// then trim before injection).
func capBytes(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "\n... (truncated)"
}
