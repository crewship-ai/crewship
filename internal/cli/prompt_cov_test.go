package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"golang.org/x/term"
)

func TestCapBytes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"no cap when max zero", "abcdef", 0, "abcdef"},
		{"no cap when negative", "abcdef", -1, "abcdef"},
		{"under limit untouched", "abc", 10, "abc"},
		{"exactly at limit untouched", "abc", 3, "abc"},
		{"over limit truncated", "abcdef", 3, "abc\n... (truncated)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := capBytes(tt.in, tt.max); got != tt.want {
				t.Errorf("capBytes(%q, %d) = %q, want %q", tt.in, tt.max, got, tt.want)
			}
		})
	}
}

// stubRunCmd returns canned output keyed by the joined argv.
func stubRunCmd(outputs map[string]string) func(context.Context, string, ...string) ([]byte, error) {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		key := strings.Join(append([]string{name}, args...), " ")
		out, ok := outputs[key]
		if !ok {
			return nil, fmt.Errorf("unexpected command: %s", key)
		}
		return []byte(out), nil
	}
}

func TestBuildPrompt_GitStagedStatusLog(t *testing.T) {
	opts := PromptOptions{
		Positional:        []string{"review", "this"},
		WithGitDiffStaged: true,
		WithGitStatus:     true,
		WithGitLog:        true,
		runCmd: stubRunCmd(map[string]string{
			"git diff --staged":     "staged-hunk",
			"git status -s":         " M main.go",
			"git log --oneline -20": "abc123 latest commit",
		}),
	}
	got, err := BuildPrompt(context.Background(), opts)
	if err != nil {
		t.Fatalf("BuildPrompt: %v", err)
	}
	for _, want := range []string{
		"review this",
		"~~~ git diff --staged\nstaged-hunk",
		"~~~ git status\n M main.go",
		"~~~ git log\nabc123 latest commit",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q:\n%s", want, got)
		}
	}
}

func TestBuildPrompt_GitCommandErrors(t *testing.T) {
	boom := errors.New("not a git repo")
	failCmd := func(context.Context, string, ...string) ([]byte, error) { return nil, boom }

	tests := []struct {
		name    string
		opts    PromptOptions
		wantMsg string
	}{
		{"diff", PromptOptions{WithGitDiff: true}, "git diff:"},
		{"staged", PromptOptions{WithGitDiffStaged: true}, "git diff --staged:"},
		{"status", PromptOptions{WithGitStatus: true}, "git status:"},
		{"log", PromptOptions{WithGitLog: true}, "git log:"},
		{"with-cmd", PromptOptions{WithCmds: []string{"date"}}, `run "date":`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.opts.runCmd = failCmd
			_, err := BuildPrompt(context.Background(), tt.opts)
			if err == nil || !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("err = %v, want containing %q", err, tt.wantMsg)
			}
			if err != nil && !errors.Is(err, boom) {
				t.Errorf("err = %v, want wrapped %v", err, boom)
			}
		})
	}
}

func TestBuildPrompt_AutoStdinReadError(t *testing.T) {
	boom := errors.New("stdin broken")
	opts := PromptOptions{
		Positional:  []string{"hi"},
		AutoStdin:   true,
		isStdinPipe: func() bool { return true },
		readStdin:   func(context.Context, int) ([]byte, error) { return nil, boom },
	}
	_, err := BuildPrompt(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "read stdin") {
		t.Errorf("err = %v, want read stdin error", err)
	}
}

func TestBuildPrompt_WithFilesReadError(t *testing.T) {
	boom := errors.New("no such file")
	opts := PromptOptions{
		Positional: []string{"hi"},
		WithFiles:  []string{"missing.txt"},
		readFile:   func(context.Context, string, int) ([]byte, error) { return nil, boom },
	}
	_, err := BuildPrompt(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "read missing.txt") {
		t.Errorf("err = %v, want read missing.txt error", err)
	}
}

func TestResolveBase_AtDashStdinError(t *testing.T) {
	boom := errors.New("pipe closed")
	o := withDefaults(PromptOptions{
		PromptFlag: "@-",
		readStdin:  func(context.Context, int) ([]byte, error) { return nil, boom },
	})
	_, consumed, err := resolveBase(context.Background(), o)
	if err == nil || !strings.Contains(err.Error(), "read stdin") {
		t.Errorf("err = %v, want read stdin error", err)
	}
	if !consumed {
		t.Error("stdinConsumed should be true for @- even on error")
	}
}

func TestWithDefaults_FillsAllInjectionPoints(t *testing.T) {
	o := withDefaults(PromptOptions{})
	if o.MaxContextBytes != 64*1024 {
		t.Errorf("MaxContextBytes = %d, want 64 KiB default", o.MaxContextBytes)
	}
	if o.readFile == nil || o.readStdin == nil || o.runCmd == nil || o.isStdinPipe == nil || o.readPaste == nil {
		t.Fatal("withDefaults left an injection point nil")
	}

	// Explicit values survive.
	o2 := withDefaults(PromptOptions{MaxContextBytes: 7})
	if o2.MaxContextBytes != 7 {
		t.Errorf("explicit MaxContextBytes overwritten: %d", o2.MaxContextBytes)
	}

	// The default runCmd really executes a subprocess.
	out, err := o.runCmd(context.Background(), "echo", "covered")
	if err != nil {
		t.Fatalf("default runCmd: %v", err)
	}
	if strings.TrimSpace(string(out)) != "covered" {
		t.Errorf("default runCmd output = %q", out)
	}

	// isStdinPipe default consults the real stdin; just assert it agrees
	// with the term package (no panic, consistent answer).
	wantPipe := !term.IsTerminal(int(os.Stdin.Fd()))
	if got := o.isStdinPipe(); got != wantPipe {
		t.Errorf("default isStdinPipe() = %v, want %v", got, wantPipe)
	}

	// Default readStdin path: only exercise when stdin is not a terminal
	// (under `go test` it is /dev/null, so this reads instant EOF).
	if wantPipe {
		data, err := o.readStdin(context.Background(), 16)
		if err != nil {
			t.Fatalf("default readStdin: %v", err)
		}
		if len(data) != 0 {
			t.Errorf("default readStdin from /dev/null = %q, want empty", data)
		}
	}
}

func TestReadClipboard_HelperExecError(t *testing.T) {
	dir := t.TempDir()
	// A pbpaste that exists but fails: readClipboard must surface the error
	// rather than falling through to the "no helper found" message.
	writeFakeTool(t, dir, "pbpaste", dir+"/r.txt", 1)
	t.Setenv("PATH", dir)

	_, err := readClipboard(context.Background(), 1024)
	if err == nil {
		t.Fatal("want error from failing clipboard helper")
	}
	if strings.Contains(err.Error(), "no clipboard helper found") {
		t.Errorf("err = %v; should be the exec failure, not the missing-helper fallback", err)
	}
}
