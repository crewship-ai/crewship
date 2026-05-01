package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Test injection-shaped helpers. Tests rarely care about ctx semantics
// (cancellation isn't being exercised), so the wrappers just discard ctx.
func stubFile(b []byte, err error) func(context.Context, string, int) ([]byte, error) {
	return func(_ context.Context, _ string, _ int) ([]byte, error) {
		return b, err
	}
}
func stubStdin(b []byte, err error) func(context.Context, int) ([]byte, error) {
	return func(_ context.Context, _ int) ([]byte, error) {
		return b, err
	}
}
func stubCmd(fn func(name string, args ...string) ([]byte, error)) func(context.Context, string, ...string) ([]byte, error) {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		return fn(name, args...)
	}
}
func stubPaste(b []byte, err error) func(context.Context, int) ([]byte, error) {
	return func(_ context.Context, _ int) ([]byte, error) {
		return b, err
	}
}

func TestBuildPrompt_PositionalOnly(t *testing.T) {
	got, err := BuildPrompt(context.Background(), PromptOptions{
		Positional:  []string{"create", "a", "REST", "API"},
		isStdinPipe: func() bool { return false },
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "create a REST API" {
		t.Errorf("got %q, want %q", got, "create a REST API")
	}
}

func TestBuildPrompt_FlagOverridesPositional(t *testing.T) {
	got, err := BuildPrompt(context.Background(), PromptOptions{
		Positional:  []string{"ignored"},
		PromptFlag:  "use this instead",
		isStdinPipe: func() bool { return false },
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "use this instead" {
		t.Errorf("got %q", got)
	}
}

func TestBuildPrompt_AtFile(t *testing.T) {
	got, err := BuildPrompt(context.Background(), PromptOptions{
		PromptFlag:  "@task.txt",
		readFile:    stubFile([]byte("file body\n"), nil),
		isStdinPipe: func() bool { return false },
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "file body" {
		t.Errorf("got %q", got)
	}
}

func TestBuildPrompt_AtFile_Error(t *testing.T) {
	_, err := BuildPrompt(context.Background(), PromptOptions{
		PromptFlag:  "@missing.txt",
		readFile:    stubFile(nil, errors.New("nope")),
		isStdinPipe: func() bool { return false },
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestBuildPrompt_AtDashStdin(t *testing.T) {
	got, err := BuildPrompt(context.Background(), PromptOptions{
		PromptFlag:  "@-",
		readStdin:   stubStdin([]byte("from stdin\n"), nil),
		isStdinPipe: func() bool { return true },
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "from stdin" {
		t.Errorf("got %q", got)
	}
}

func TestBuildPrompt_AutoStdinAppends(t *testing.T) {
	got, err := BuildPrompt(context.Background(), PromptOptions{
		Positional:  []string{"review", "this"},
		AutoStdin:   true,
		readStdin:   stubStdin([]byte("diff line\nanother\n"), nil),
		isStdinPipe: func() bool { return true },
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(got, "review this\n\n") {
		t.Errorf("expected stdin appended: %q", got)
	}
	if !strings.Contains(got, "stdin") || !strings.Contains(got, "diff line") {
		t.Errorf("missing stdin block: %q", got)
	}
}

func TestBuildPrompt_AutoStdinSkippedWhenNotPipe(t *testing.T) {
	got, err := BuildPrompt(context.Background(), PromptOptions{
		Positional: []string{"hello"},
		AutoStdin:  true,
		readStdin: func(_ context.Context, _ int) ([]byte, error) {
			t.Fatal("stdin should not be read")
			return nil, nil
		},
		isStdinPipe: func() bool { return false },
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "hello" {
		t.Errorf("got %q", got)
	}
}

func TestBuildPrompt_AtDashSuppressesAutoStdin(t *testing.T) {
	calls := 0
	got, err := BuildPrompt(context.Background(), PromptOptions{
		PromptFlag: "@-",
		AutoStdin:  true,
		readStdin: func(_ context.Context, _ int) ([]byte, error) {
			calls++
			return []byte("once"), nil
		},
		isStdinPipe: func() bool { return true },
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if calls != 1 {
		t.Errorf("stdin should be read exactly once, got %d", calls)
	}
	if got != "once" {
		t.Errorf("got %q", got)
	}
}

func TestBuildPrompt_GitDiff(t *testing.T) {
	got, err := BuildPrompt(context.Background(), PromptOptions{
		Positional:  []string{"review"},
		WithGitDiff: true,
		runCmd: stubCmd(func(name string, args ...string) ([]byte, error) {
			if name != "git" || len(args) < 1 || args[0] != "diff" {
				t.Fatalf("unexpected cmd: %s %v", name, args)
			}
			return []byte("diff --git ...\n+changes\n"), nil
		}),
		isStdinPipe: func() bool { return false },
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(got, "git diff") {
		t.Errorf("missing label: %q", got)
	}
	if !strings.Contains(got, "+changes") {
		t.Errorf("missing content: %q", got)
	}
}

func TestBuildPrompt_WithFile(t *testing.T) {
	got, err := BuildPrompt(context.Background(), PromptOptions{
		Positional: []string{"explain"},
		WithFiles:  []string{"/tmp/foo.go"},
		readFile: func(_ context.Context, p string, _ int) ([]byte, error) {
			if p != "/tmp/foo.go" {
				t.Fatalf("unexpected path: %s", p)
			}
			return []byte("package foo\n"), nil
		},
		isStdinPipe: func() bool { return false },
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(got, "file: /tmp/foo.go") {
		t.Errorf("missing label: %q", got)
	}
	if !strings.Contains(got, "package foo") {
		t.Errorf("missing content: %q", got)
	}
}

func TestBuildPrompt_WithCmd(t *testing.T) {
	got, err := BuildPrompt(context.Background(), PromptOptions{
		Positional: []string{"investigate"},
		WithCmds:   []string{"ps aux"},
		runCmd: stubCmd(func(name string, args ...string) ([]byte, error) {
			if name != "sh" || len(args) != 2 || args[0] != "-c" || args[1] != "ps aux" {
				t.Fatalf("unexpected cmd: %s %v", name, args)
			}
			return []byte("PID 1 init\n"), nil
		}),
		isStdinPipe: func() bool { return false },
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(got, "$ ps aux") {
		t.Errorf("missing label: %q", got)
	}
}

func TestBuildPrompt_CapBytes(t *testing.T) {
	big := strings.Repeat("x", 10_000)
	got, err := BuildPrompt(context.Background(), PromptOptions{
		Positional:      []string{"review"},
		WithFiles:       []string{"big.txt"},
		MaxContextBytes: 100,
		readFile: func(_ context.Context, _ string, max int) ([]byte, error) {
			// Caller should pass max=100 through to the bounded reader.
			if max != 100 {
				t.Errorf("expected max=100, got %d", max)
			}
			// Stub mimics the production reader: truncate at max.
			if len(big) > max {
				return []byte(big[:max] + "\n... (truncated)"), nil
			}
			return []byte(big), nil
		},
		isStdinPipe: func() bool { return false },
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(got, "(truncated)") {
		t.Errorf("expected truncation marker: %q", got)
	}
}

func TestBuildPrompt_EmptyContextSkipped(t *testing.T) {
	got, err := BuildPrompt(context.Background(), PromptOptions{
		Positional:  []string{"hi"},
		WithGitDiff: true,
		runCmd:      stubCmd(func(name string, args ...string) ([]byte, error) { return []byte(""), nil }),
		isStdinPipe: func() bool { return false },
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "hi" {
		t.Errorf("expected empty diff to be skipped, got %q", got)
	}
}

func TestBuildPrompt_MultipleContexts(t *testing.T) {
	got, err := BuildPrompt(context.Background(), PromptOptions{
		Positional:    []string{"review"},
		WithGitDiff:   true,
		WithGitStatus: true,
		WithFiles:     []string{"a.txt"},
		runCmd: stubCmd(func(name string, args ...string) ([]byte, error) {
			if len(args) > 0 && args[0] == "diff" {
				return []byte("diff body"), nil
			}
			return []byte("status body"), nil
		}),
		readFile:    stubFile([]byte("file body"), nil),
		isStdinPipe: func() bool { return false },
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	for _, want := range []string{"git diff", "git status", "file: a.txt", "diff body", "status body", "file body"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in result", want)
		}
	}
}

func TestBuildPrompt_Paste(t *testing.T) {
	got, err := BuildPrompt(context.Background(), PromptOptions{
		Positional:  []string{"summarize"},
		Paste:       true,
		readPaste:   stubPaste([]byte("clipboard contents"), nil),
		isStdinPipe: func() bool { return false },
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(got, "clipboard") {
		t.Errorf("missing label: %q", got)
	}
	if !strings.Contains(got, "clipboard contents") {
		t.Errorf("missing content: %q", got)
	}
}

func TestBuildPrompt_PasteError(t *testing.T) {
	_, err := BuildPrompt(context.Background(), PromptOptions{
		Positional:  []string{"x"},
		Paste:       true,
		readPaste:   stubPaste(nil, fmt.Errorf("no helper")),
		isStdinPipe: func() bool { return false },
	})
	if err == nil {
		t.Fatal("expected error when paste fails")
	}
}

func TestBuildPrompt_OnlyContextNoBase(t *testing.T) {
	got, err := BuildPrompt(context.Background(), PromptOptions{
		WithGitDiff: true,
		runCmd:      stubCmd(func(name string, args ...string) ([]byte, error) { return []byte("just a diff"), nil }),
		isStdinPipe: func() bool { return false },
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.HasPrefix(got, "~~~") {
		t.Errorf("expected to start with fence, got: %q", got)
	}
	if !strings.Contains(got, "git diff") {
		t.Errorf("missing label: %q", got)
	}
}

// TestAppendBlock_FenceEscapesContent verifies the fence-length adapts so
// content with tildes can't terminate the fence early — a property that
// matters for prompt-injection resistance and downstream markdown parsers.
func TestAppendBlock_FenceEscapesContent(t *testing.T) {
	var sb strings.Builder
	appendBlock(&sb, "weird", "before\n~~~\nstuff\n~~~~\nafter")
	out := sb.String()
	// Content has runs of 3 and 4 tildes; fence must be ≥5.
	if !strings.Contains(out, "~~~~~ weird") {
		t.Errorf("expected ≥5-tilde fence to escape content, got: %q", out)
	}
}

func TestLongestTildeRun(t *testing.T) {
	cases := map[string]int{
		"":                       0,
		"no tildes here":         0,
		"~":                      1,
		"~~":                     2,
		"~~~":                    3,
		"foo ~~ bar ~~~~":        4,
		"~ ~ ~":                  1,
		"~~~~~~~ very many ~~~ ": 7,
	}
	for in, want := range cases {
		if got := longestTildeRun(in); got != want {
			t.Errorf("longestTildeRun(%q) = %d, want %d", in, got, want)
		}
	}
}

// TestReadBounded_TruncationMarker ensures the production bounded reader
// caps at max bytes and tags the result so callers (and humans) can tell
// the source was longer than the limit.
func TestReadBounded_TruncationMarker(t *testing.T) {
	src := strings.NewReader(strings.Repeat("x", 1000))
	got, err := readBounded(context.Background(), src, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "(truncated)") {
		t.Errorf("expected truncation marker, got: %q", string(got))
	}
}

func TestReadBounded_RespectsExactSize(t *testing.T) {
	src := strings.NewReader("hello")
	got, err := readBounded(context.Background(), src, 5)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Errorf("exact-size input should pass through, got %q", string(got))
	}
}
