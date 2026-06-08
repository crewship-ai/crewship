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
	// @file expansion is now contained to the working directory (see
	// readAtFileBounded), so the referenced files must live under cwd.
	// chdir into a temp dir and reference files by their basename.
	dir := t.TempDir()
	t.Chdir(dir) // isolated, auto-restored — no process-global cwd mutation

	if err := os.WriteFile(filepath.Join(dir, "note.md"), []byte("FOO BAR\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("expands existing file", func(t *testing.T) {
		got, err := ExpandAtFiles(context.Background(), "look at @note.md and decide")
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
		got, _ := ExpandAtFiles(context.Background(), "nope @does-not-exist done")
		if !strings.Contains(got, "@does-not-exist") {
			t.Errorf("missing-file token should be preserved: %q", got)
		}
	})

	t.Run("preserves @- stdin marker", func(t *testing.T) {
		got, _ := ExpandAtFiles(context.Background(), "read @- now")
		if !strings.Contains(got, "@-") {
			t.Errorf("@- should be preserved: %q", got)
		}
	})

	t.Run("traversal token preserved (not inlined)", func(t *testing.T) {
		// A `..`-escaping token must be left verbatim, never expanded —
		// the containment guard in readAtFileBounded refuses the read
		// and ExpandAtFiles falls back to the literal token.
		got, _ := ExpandAtFiles(context.Background(), "sneaky @../../../etc/hosts end")
		if !strings.Contains(got, "@../../../etc/hosts") {
			t.Errorf("traversal token should be preserved verbatim: %q", got)
		}
	})

	t.Run("caps reads at MaxAtFileBytes", func(t *testing.T) {
		buf := make([]byte, MaxAtFileBytes+1024)
		for i := range buf {
			buf[i] = 'a'
		}
		if err := os.WriteFile(filepath.Join(dir, "big.txt"), buf, 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := ExpandAtFiles(context.Background(), "@big.txt")
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
