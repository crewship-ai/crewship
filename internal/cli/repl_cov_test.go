package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
)

func TestREPL_RegisterInitializesNilMap(t *testing.T) {
	r := &REPL{} // zero value: Slash is nil
	r.Register("ping", func(context.Context, []string) (bool, error) { return true, nil })
	if r.Slash == nil {
		t.Fatal("Register should lazily allocate Slash map")
	}
	if _, ok := r.Slash["ping"]; !ok {
		t.Error("handler not registered under its name")
	}
}

func TestREPL_RunReturnsOnContextCancel(t *testing.T) {
	pr, pw := io.Pipe() // a stdin that never produces a line
	defer pw.Close()    // unblocks the producer goroutine after Run returns

	var out, errOut bytes.Buffer
	r := NewREPL()
	r.In = pr
	r.Out = &out
	r.Err = &errOut

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := r.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v, want context.Canceled", err)
	}
}

func TestREPL_RunSurfacesScannerError(t *testing.T) {
	boom := errors.New("tty exploded")
	var out, errOut bytes.Buffer
	r := NewREPL()
	r.In = iotest.ErrReader(boom)
	r.Out = &out
	r.Err = &errOut

	if err := r.Run(context.Background()); !errors.Is(err, boom) {
		t.Errorf("Run = %v, want scanner error %v", err, boom)
	}
}

func TestREPL_RunDefaultsNilOutErr(t *testing.T) {
	// Out/Err left nil must fall back to os.Stdout/os.Stderr without
	// panicking. Empty input → immediate EOF → clean exit.
	r := &REPL{In: strings.NewReader(""), Prompt: ""}
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run with nil Out/Err: %v", err)
	}
	if r.Out != os.Stdout || r.Err != os.Stderr {
		t.Error("nil Out/Err should be defaulted to os.Stdout/os.Stderr")
	}
}

func TestREPL_ExitSentinelIsClean(t *testing.T) {
	var out, errOut bytes.Buffer
	r := NewREPL()
	r.In = strings.NewReader("/exit\n")
	r.Out = &out
	r.Err = &errOut
	r.Register("exit", func(context.Context, []string) (bool, error) { return false, ErrREPLExit })

	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run = %v, want nil for ErrREPLExit", err)
	}
	if strings.Contains(errOut.String(), "[err]") {
		t.Errorf("ErrREPLExit must not print an error line, got: %s", errOut.String())
	}
}

func TestREPL_StopErrorIsReturned(t *testing.T) {
	boom := errors.New("fatal handler problem")
	var out, errOut bytes.Buffer
	r := NewREPL()
	r.In = strings.NewReader("/boom\n")
	r.Out = &out
	r.Err = &errOut
	r.Register("boom", func(context.Context, []string) (bool, error) { return false, boom })

	if err := r.Run(context.Background()); !errors.Is(err, boom) {
		t.Errorf("Run = %v, want %v returned to caller", err, boom)
	}
	if !strings.Contains(errOut.String(), "[err]") {
		t.Errorf("stop error should also be printed, got: %s", errOut.String())
	}
}

func TestREPL_HandlerErrorContinuesLoop(t *testing.T) {
	var out, errOut bytes.Buffer
	r := NewREPL()
	r.In = strings.NewReader("/warn\n/exit\n")
	r.Out = &out
	r.Err = &errOut
	r.Register("warn", func(context.Context, []string) (bool, error) {
		return true, errors.New("soft failure")
	})
	exited := false
	r.Register("exit", func(context.Context, []string) (bool, error) {
		exited = true
		return false, ErrREPLExit
	})

	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(errOut.String(), "soft failure") {
		t.Errorf("handler error not surfaced: %s", errOut.String())
	}
	if !exited {
		t.Error("loop should have continued to /exit after the soft failure")
	}
}

func TestREPL_BareHandlerErrorPrinted(t *testing.T) {
	var out, errOut bytes.Buffer
	r := NewREPL()
	r.In = strings.NewReader("do something\n")
	r.Out = &out
	r.Err = &errOut
	r.BareHandler = func(context.Context, string) error { return errors.New("bare boom") }

	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(errOut.String(), "bare boom") {
		t.Errorf("bare handler error not printed: %s", errOut.String())
	}
}

func TestREPL_NoBareHandlerEchoes(t *testing.T) {
	var out, errOut bytes.Buffer
	r := NewREPL()
	r.In = strings.NewReader("hello world\n")
	r.Out = &out
	r.Err = &errOut

	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "[no handler] hello world") {
		t.Errorf("missing [no handler] echo: %s", out.String())
	}
}

func TestREPL_EmptyLinesReprintPrompt(t *testing.T) {
	var out, errOut bytes.Buffer
	r := NewREPL()
	r.Prompt = "P> "
	r.In = strings.NewReader("\n\n")
	r.Out = &out
	r.Err = &errOut

	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Initial prompt + one reprint per empty line.
	if got := strings.Count(out.String(), "P> "); got != 3 {
		t.Errorf("prompt printed %d times, want 3: %q", got, out.String())
	}
}

func TestREPL_DispatchSlashEmptyCommand(t *testing.T) {
	var errOut bytes.Buffer
	r := NewREPL()
	r.Err = &errOut
	cont, err := r.dispatchSlash(context.Background(), "/")
	if !cont || err != nil {
		t.Errorf("dispatchSlash(\"/\") = (%v, %v), want (true, nil)", cont, err)
	}
	if errOut.Len() != 0 {
		t.Errorf("bare slash should be silent, got %q", errOut.String())
	}
}

func TestREPL_OnUnknownCallback(t *testing.T) {
	var errOut bytes.Buffer
	r := NewREPL()
	r.Err = &errOut
	var unknown string
	r.OnUnknown = func(name string) { unknown = name }

	cont, err := r.dispatchSlash(context.Background(), "/nope arg1")
	if !cont || err != nil {
		t.Fatalf("dispatchSlash = (%v, %v), want (true, nil)", cont, err)
	}
	if unknown != "nope" {
		t.Errorf("OnUnknown got %q, want nope", unknown)
	}
	if strings.Contains(errOut.String(), "[unknown]") {
		t.Errorf("default warning should be suppressed when OnUnknown set: %s", errOut.String())
	}
}

func TestExpandAtFiles_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ExpandAtFiles(ctx, "expand @file.txt please")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestExpandAtFiles_ReservedTokensPreserved(t *testing.T) {
	got, err := ExpandAtFiles(context.Background(), "pipe @- and bare @ stay")
	if err != nil {
		t.Fatal(err)
	}
	if got != "pipe @- and bare @ stay" {
		t.Errorf("reserved tokens mangled: %q", got)
	}
}

func TestExpandAtFiles_HomeExpansion(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "note.txt"), []byte("HOMEDATA\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	got, err := ExpandAtFiles(context.Background(), "see @~/note.txt end")
	if err != nil {
		t.Fatal(err)
	}
	if got != "see HOMEDATA end" {
		t.Errorf("got %q, want home-expanded content", got)
	}
}

func TestExpandAtFiles_HomeUnresolvablePreservesToken(t *testing.T) {
	t.Setenv("HOME", "") // os.UserHomeDir errors on empty $HOME (unix)
	got, err := ExpandAtFiles(context.Background(), "read @~/secret.txt now")
	if err != nil {
		t.Fatal(err)
	}
	if got != "read @~/secret.txt now" {
		t.Errorf("token should be preserved verbatim, got %q", got)
	}
}

func TestAtFilePathContained_EmptyPath(t *testing.T) {
	err := atFilePathContained("  ")
	if err == nil || !strings.Contains(err.Error(), "empty @file path") {
		t.Errorf("err = %v, want empty path error", err)
	}
}

func TestAtFilePathContained_DotDotSegment(t *testing.T) {
	for _, p := range []string{"../up.txt", "a/../../b.txt"} {
		err := atFilePathContained(p)
		if err == nil || !strings.Contains(err.Error(), "traversal") {
			t.Errorf("atFilePathContained(%q) = %v, want traversal rejection", p, err)
		}
	}
}

func TestAtFilePathContained_DoubleDotInNameAllowed(t *testing.T) {
	dir := t.TempDir()
	restore := chdir(t, dir)
	defer restore()
	if err := os.WriteFile(filepath.Join(dir, "release..md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := atFilePathContained("release..md"); err != nil {
		t.Errorf("'..' inside a file NAME should be allowed, got %v", err)
	}
}

func TestAtFilePathContained_MissingFileInCwdAllowed(t *testing.T) {
	dir := t.TempDir()
	restore := chdir(t, dir)
	defer restore()
	// File doesn't exist: containment resolves the parent and re-attaches
	// the base name — still inside cwd, so no error.
	if err := atFilePathContained("not-yet-created.txt"); err != nil {
		t.Errorf("missing in-tree file should pass containment, got %v", err)
	}
}

func TestAtFilePathContained_MissingParentRejected(t *testing.T) {
	dir := t.TempDir()
	restore := chdir(t, dir)
	defer restore()
	err := atFilePathContained("no-such-dir/sub/file.txt")
	if err == nil {
		t.Error("nonexistent parent dir should fail containment resolution")
	}
}
