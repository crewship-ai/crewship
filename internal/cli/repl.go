package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// MaxAtFileBytes caps how much content @file expansion will pull from
// any single referenced file. Prompts that need more than this should
// be assembled with `crewship run --with-file <path>` which has its
// own context-budget plumbing.
const MaxAtFileBytes = 1 << 20 // 1 MiB

// REPLHandler is one callback for a slash-prefixed command in the
// interactive shell. Returns (continueLoop, err) — set continueLoop
// false to exit the REPL cleanly (e.g. `/exit`).
type REPLHandler func(ctx context.Context, args []string) (bool, error)

// REPL is a minimal interactive command loop.
//
// Design choices:
//
//   - bufio.Scanner over chzyer/readline so the dependency surface
//     stays at the level the rest of the CLI already has. The trade-
//     off: no Up-arrow history navigation in v1. Users can compose
//     with shell-level history (`fc -s` in zsh) for now.
//
//   - Slash commands dispatch by exact name (`/help`, `/agent`). The
//     rest is forwarded to a configurable BareHandler, which lets the
//     caller hook this into ask/run/whatever.
//
//   - `@file` expansion happens at the bare-text layer via the
//     ExpandAtFiles helper — that's exercised both here and by
//     BuildPrompt, so the behaviour is consistent across surfaces.
type REPL struct {
	// Prompt is the per-line prompt string (e.g. "crewship › ").
	Prompt string

	// Out / Err / In wires for IO. Default to os.Stdout / os.Stderr /
	// os.Stdin when unset by NewREPL.
	Out io.Writer
	Err io.Writer
	In  io.Reader

	// Slash maps "/foo" → handler. Names should NOT include the
	// leading slash; the REPL strips that before dispatch.
	Slash map[string]REPLHandler

	// BareHandler is called for any non-slash input. If nil, the REPL
	// echoes the input back as `[no handler]` which is useful for
	// scaffolding before the real dispatcher is wired.
	BareHandler func(ctx context.Context, line string) error

	// OnUnknown is called for `/foo` where foo isn't in Slash. If nil,
	// the REPL emits a generic "unknown" warning.
	OnUnknown func(name string)
}

// NewREPL returns a REPL with safe defaults.
func NewREPL() *REPL {
	return &REPL{
		Prompt: "crewship › ",
		Out:    os.Stdout,
		Err:    os.Stderr,
		In:     os.Stdin,
		Slash:  map[string]REPLHandler{},
	}
}

// Register adds a slash command. `name` should not include the leading slash.
func (r *REPL) Register(name string, h REPLHandler) {
	if r.Slash == nil {
		r.Slash = map[string]REPLHandler{}
	}
	r.Slash[name] = h
}

// Run blocks until the user exits (Ctrl-D / `/exit`) or ctx is
// cancelled. Each line is parsed: slash → handler, else → BareHandler.
func (r *REPL) Run(ctx context.Context) error {
	if r.In == nil {
		r.In = os.Stdin
	}
	if r.Out == nil {
		r.Out = os.Stdout
	}
	if r.Err == nil {
		r.Err = os.Stderr
	}
	scanner := bufio.NewScanner(r.In)
	// 1 MiB line buffer so @file expansion on huge files works.
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1<<20)

	// scanner.Scan() blocks indefinitely on a quiet stdin, so ctx
	// cancellation would never be observed if we called Scan inline.
	// Pump scanned lines through a channel and select against ctx.Done
	// instead. The producer goroutine exits naturally on Ctrl-D / EOF
	// (Scan returns false) or when the caller closes r.In.
	type scanned struct {
		line string
		err  error
		eof  bool
	}
	lines := make(chan scanned)
	go func() {
		defer close(lines)
		for scanner.Scan() {
			lines <- scanned{line: scanner.Text()}
		}
		if err := scanner.Err(); err != nil {
			lines <- scanned{err: err}
			return
		}
		lines <- scanned{eof: true}
	}()

	fmt.Fprint(r.Out, r.Prompt)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case s, ok := <-lines:
			if !ok {
				// Producer closed without emitting EOF — treat as
				// clean shutdown to avoid an infinite select.
				fmt.Fprintln(r.Out)
				return nil
			}
			if s.err != nil {
				return s.err
			}
			if s.eof {
				fmt.Fprintln(r.Out)
				return nil
			}
			line := strings.TrimSpace(s.line)
			if line == "" {
				fmt.Fprint(r.Out, r.Prompt)
				continue
			}
			if strings.HasPrefix(line, "/") {
				cont, err := r.dispatchSlash(ctx, line)
				if err != nil {
					// ErrREPLExit is a sentinel for "leave cleanly" —
					// don't pollute the user's screen with a fake
					// error line for an intentional /exit.
					if !errors.Is(err, ErrREPLExit) {
						fmt.Fprintf(r.Err, "[err] %v\n", err)
					}
				}
				if !cont {
					// Surface any other stop-error so callers can
					// react. ErrREPLExit collapses to nil.
					if err != nil && !errors.Is(err, ErrREPLExit) {
						return err
					}
					return nil
				}
			} else {
				expanded, err := ExpandAtFiles(ctx, line)
				if err != nil {
					fmt.Fprintf(r.Err, "[err] @-expansion: %v\n", err)
				} else if r.BareHandler != nil {
					if err := r.BareHandler(ctx, expanded); err != nil {
						fmt.Fprintf(r.Err, "[err] %v\n", err)
					}
				} else {
					fmt.Fprintf(r.Out, "[no handler] %s\n", expanded)
				}
			}
			fmt.Fprint(r.Out, r.Prompt)
		}
	}
}

// dispatchSlash splits "/cmd arg1 arg2" → name + args, looks up, calls.
// Returns (continueLoop, err).
func (r *REPL) dispatchSlash(ctx context.Context, line string) (bool, error) {
	parts := strings.Fields(strings.TrimPrefix(line, "/"))
	if len(parts) == 0 {
		return true, nil
	}
	name, args := parts[0], parts[1:]
	h, ok := r.Slash[name]
	if !ok {
		if r.OnUnknown != nil {
			r.OnUnknown(name)
		} else {
			fmt.Fprintf(r.Err, "[unknown] /%s — try /help\n", name)
		}
		return true, nil
	}
	return h(ctx, args)
}

// ExpandAtFiles replaces each `@path` token in s with the file content.
// Multiple tokens are supported on one line. Paths starting with `~/`
// are expanded against $HOME. Files that don't exist remain as-is so
// the user sees the literal token rather than a silent drop.
//
// The ctx parameter is honored so a hung file open on a network mount
// can be cancelled by REPL shutdown. Reads are capped at MaxAtFileBytes
// (1 MiB) per file — larger payloads should be assembled via
// `crewship run --with-file` which has dedicated context-budget logic.
//
// Token rules:
//   - `@-` is reserved (stdin) and left untouched here; only file
//     references are inlined.
//   - The token ends at the first whitespace; quoted filenames are
//     out of scope for the v1 surface.
//   - `~/` expansion: if os.UserHomeDir fails, the token is preserved
//     verbatim instead of being silently dropped — a misleading "file
//     not found" downstream is worse than a literal `~/notes.md` the
//     user can debug.
func ExpandAtFiles(ctx context.Context, s string) (string, error) {
	if !strings.Contains(s, "@") {
		return s, nil
	}
	var b strings.Builder
	i := 0
	for i < len(s) {
		if ctx != nil {
			select {
			case <-ctx.Done():
				return b.String(), ctx.Err()
			default:
			}
		}
		r := s[i]
		if r != '@' {
			b.WriteByte(r)
			i++
			continue
		}
		// Find token end.
		j := i + 1
		for j < len(s) && !isSpace(s[j]) {
			j++
		}
		token := s[i+1 : j]
		if token == "" || token == "-" {
			b.WriteString(s[i:j])
			i = j
			continue
		}
		path := token
		homeToken := strings.HasPrefix(path, "~/")
		if homeToken {
			home, herr := os.UserHomeDir()
			if herr != nil {
				// Preserve the literal token rather than synthesising
				// a bogus absolute path. Downstream "file not found"
				// would be less obvious than the original ~/ form.
				b.WriteString(s[i:j])
				i = j
				continue
			}
			path = filepath.Join(home, path[2:])
		}
		// Containment: a plain relative/absolute @file token is confined
		// to the working directory so `@../../../etc/passwd` can't inline
		// arbitrary files. An explicit ~/ token is the one documented
		// exception (the user typed their home dir) and skips the cwd
		// check — but ~/ itself was joined onto $HOME above, so it still
		// can't climb out of the filesystem in a surprising way.
		data, err := readAtFileBounded(path, homeToken)
		if err != nil {
			b.WriteString(s[i:j])
		} else {
			b.WriteString(strings.TrimRight(string(data), "\n"))
		}
		i = j
	}
	return b.String(), nil
}

// readAtFileBounded opens path and reads up to MaxAtFileBytes. Returns
// the (possibly truncated) bytes and a wrapped error on failure.
//
// Unless homeToken is set, the path is contained to the current working
// directory: any ".." segment or an absolute/relative path that resolves
// outside the cwd subtree is rejected before the open. This stops a
// hostile prompt (`@../../../etc/passwd`, `@/etc/shadow`) from inlining
// arbitrary files into an agent's context via @file expansion. The
// containment shape mirrors cmd_memory_versions.go:canonicalPathIsSafe.
//
// homeToken == true is the single documented exception: the user typed
// an explicit `~/...` reference, which the caller already joined onto
// $HOME, so the cwd subtree check would be wrong for it.
func readAtFileBounded(path string, homeToken bool) ([]byte, error) {
	if !homeToken {
		if err := atFilePathContained(path); err != nil {
			return nil, err
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxAtFileBytes))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return data, nil
}

// atFilePathContained rejects @file paths that escape the working
// directory: any ".." segment, or a cleaned absolute path that does not
// sit under the cwd subtree. Empty paths are rejected too. Mirrors the
// containment in cmd_memory_versions.go:canonicalPathIsSafe but rooted
// at the cwd rather than a caller-supplied root.
func atFilePathContained(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("empty @file path")
	}
	// Reject ".." as a path SEGMENT, not a raw substring, so legitimate
	// in-tree names like "release..md" or "v1..backup/note.txt" are allowed
	// while "../escape" is not.
	for _, seg := range strings.Split(filepath.ToSlash(filepath.Clean(path)), "/") {
		if seg == ".." {
			return fmt.Errorf("@file path %q rejected: '..' traversal not allowed", path)
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("@file containment: resolve working dir: %w", err)
	}
	// Resolve symlinks on BOTH ends so an in-tree symlink that points
	// outside the working dir (work/sub -> /etc) can't smuggle an
	// out-of-tree read past a purely lexical prefix check.
	realRoot, err := filepath.EvalSymlinks(wd)
	if err != nil {
		return fmt.Errorf("@file containment: resolve working dir: %w", err)
	}
	absP, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("@file containment: resolve %q: %w", path, err)
	}
	realTarget, err := filepath.EvalSymlinks(absP)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("@file containment: resolve %q: %w", path, err)
		}
		// File (or a leaf component) doesn't exist yet: resolve the deepest
		// existing ancestor, then re-attach the unresolved base name.
		realParent, perr := filepath.EvalSymlinks(filepath.Dir(absP))
		if perr != nil {
			return fmt.Errorf("@file containment: resolve %q: %w", path, perr)
		}
		realTarget = filepath.Join(realParent, filepath.Base(absP))
	}
	rel, err := filepath.Rel(realRoot, realTarget)
	if err != nil {
		return fmt.Errorf("@file path %q rejected: %w", path, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("@file path %q rejected: resolves outside the working directory", path)
	}
	return nil
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// ErrREPLExit is returned by `/exit` handlers to signal a clean shutdown.
var ErrREPLExit = errors.New("repl: exit requested")

// ApplyPlanShellPrefix is the REPL-side equivalent of the main package
// ApplyPlanFlag — kept here so internal/cli stays self-contained for
// the REPL layer. The actual plan prefix lives in cmd_plan.go's
// planSystemPrefix constant; the REPL only needs a marker that the
// dispatcher will see and rewrite at the cmd level.
func ApplyPlanShellPrefix(line string) string {
	if strings.HasPrefix(line, "[plan-mode]") {
		return line
	}
	return "[plan-shell] " + line
}
