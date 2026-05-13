package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestREPL_SlashAndBareDispatch(t *testing.T) {
	in := strings.NewReader("hello\n/foo bar baz\n/exit\n")
	var out, errOut bytes.Buffer
	r := NewREPL()
	r.In = in
	r.Out = &out
	r.Err = &errOut

	var fooArgs []string
	r.Register("foo", func(_ context.Context, args []string) (bool, error) {
		fooArgs = args
		return true, nil
	})
	r.Register("exit", func(_ context.Context, _ []string) (bool, error) { return false, nil })

	var bareLines []string
	r.BareHandler = func(_ context.Context, line string) error {
		bareLines = append(bareLines, line)
		return nil
	}
	if err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(bareLines) != 1 || bareLines[0] != "hello" {
		t.Errorf("bare=%v", bareLines)
	}
	if len(fooArgs) != 2 || fooArgs[0] != "bar" {
		t.Errorf("fooArgs=%v", fooArgs)
	}
}

func TestREPL_UnknownSlash(t *testing.T) {
	in := strings.NewReader("/zzz\n/exit\n")
	var out, errOut bytes.Buffer
	r := NewREPL()
	r.In = in
	r.Out = &out
	r.Err = &errOut
	r.Register("exit", func(_ context.Context, _ []string) (bool, error) { return false, nil })
	if err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "unknown") {
		t.Errorf("expected unknown warning, got: %s", errOut.String())
	}
}

func TestExpandAtFiles(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "note.md")
	if err := os.WriteFile(p, []byte("FOO BAR\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("expands existing file", func(t *testing.T) {
		got, err := ExpandAtFiles(context.Background(), "look at @"+p+" and decide")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got, "FOO BAR") {
			t.Errorf("expansion missing content: %q", got)
		}
		if !strings.Contains(got, "and decide") {
			t.Errorf("expansion lost trailing text: %q", got)
		}
	})

	t.Run("preserves missing-file token", func(t *testing.T) {
		got, _ := ExpandAtFiles(context.Background(), "nope @/does/not/exist done")
		if !strings.Contains(got, "@/does/not/exist") {
			t.Errorf("missing-file token should be preserved: %q", got)
		}
	})

	t.Run("preserves @- stdin marker", func(t *testing.T) {
		got, _ := ExpandAtFiles(context.Background(), "read @- now")
		if !strings.Contains(got, "@-") {
			t.Errorf("@- should be preserved: %q", got)
		}
	})

	t.Run("caps reads at MaxAtFileBytes", func(t *testing.T) {
		big := filepath.Join(dir, "big.txt")
		buf := make([]byte, MaxAtFileBytes+1024)
		for i := range buf {
			buf[i] = 'a'
		}
		if err := os.WriteFile(big, buf, 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := ExpandAtFiles(context.Background(), "@"+big)
		if err != nil {
			t.Fatal(err)
		}
		// `a`-only file → fully readable up to the cap, no error.
		if len(got) > MaxAtFileBytes {
			t.Errorf("expansion exceeded cap: %d bytes (cap %d)", len(got), MaxAtFileBytes)
		}
	})
}

func TestApplyPlanShellPrefix(t *testing.T) {
	if got := ApplyPlanShellPrefix("hello"); !strings.Contains(got, "[plan-shell]") {
		t.Errorf("expected plan prefix, got %q", got)
	}
	if got := ApplyPlanShellPrefix("[plan-mode] already"); !strings.HasPrefix(got, "[plan-mode]") {
		t.Errorf("should not re-prefix: %q", got)
	}
}
