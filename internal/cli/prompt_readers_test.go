package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// prompt.go — readFileBounded + readClipboard.
//
// These two are the I/O legs of the prompt-construction pipeline.
// readFileBounded is what --with-file dispatches to; readClipboard is
// the --with-clipboard helper. Both share the bounded-read invariant
// that a 1 GB input doesn't blow up the CLI.
// ---------------------------------------------------------------------------

// ---- readFileBounded ----

func TestReadFileBounded_HappyPath(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "small.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := readFileBounded(context.Background(), path, 100)
	if err != nil {
		t.Fatalf("readFileBounded: %v", err)
	}
	if string(got) != "hello world" {
		t.Errorf("got %q, want \"hello world\"", got)
	}
}

func TestReadFileBounded_TruncatesOversizedFile(t *testing.T) {
	// Source: "appending a '(truncated)' marker if more remained.
	// Mirrors os.ReadFile's API but bounds memory use — a 1 GB
	// --with-file no longer crashes the CLI." Pin both the cap and
	// the truncation marker so a refactor can't silently lift the
	// limit.
	tmp := t.TempDir()
	path := filepath.Join(tmp, "big.txt")
	// 200 bytes of 'a' content.
	if err := os.WriteFile(path, []byte(strings.Repeat("a", 200)), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := readFileBounded(context.Background(), path, 50)
	if err != nil {
		t.Fatalf("readFileBounded: %v", err)
	}
	if !strings.Contains(string(got), "(truncated)") {
		t.Errorf("output missing \"(truncated)\" marker: %q", got)
	}
	// Body should be the first 50 bytes + the marker. Don't pin the
	// exact byte count (marker length isn't part of the contract) but
	// the prefix must be `aaa...` (50 a's).
	if !strings.HasPrefix(string(got), strings.Repeat("a", 50)) {
		t.Errorf("output prefix wrong: %q", got)
	}
}

func TestReadFileBounded_ExactlyAtLimit_NoTruncationMarker(t *testing.T) {
	// Boundary case: content length == max → no marker added. Pin
	// the "max+1 bytes to detect overflow" idiom: if the file is
	// exactly `max` bytes, no truncation flag.
	tmp := t.TempDir()
	path := filepath.Join(tmp, "exact.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("b", 50)), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := readFileBounded(context.Background(), path, 50)
	if err != nil {
		t.Fatalf("readFileBounded: %v", err)
	}
	if strings.Contains(string(got), "(truncated)") {
		t.Errorf("exactly-at-limit output contains \"(truncated)\": %q", got)
	}
	if string(got) != strings.Repeat("b", 50) {
		t.Errorf("got %q, want %q", got, strings.Repeat("b", 50))
	}
}

func TestReadFileBounded_MissingFile_Errors(t *testing.T) {
	_, err := readFileBounded(context.Background(), filepath.Join(t.TempDir(), "does-not-exist"), 100)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	// Underlying os.Open wraps a *PathError; verify it's not silently
	// swallowed.
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) {
		t.Errorf("err = %v (%T), want *os.PathError", err, err)
	}
}

func TestReadFileBounded_ZeroMaxFallsBackToDefault(t *testing.T) {
	// Source's readBounded defaults max to 64 KiB when max <= 0. Pin
	// that the file path inherits the same default; a small file
	// passes through unchanged.
	tmp := t.TempDir()
	path := filepath.Join(tmp, "tiny.txt")
	if err := os.WriteFile(path, []byte("small"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := readFileBounded(context.Background(), path, 0)
	if err != nil {
		t.Fatalf("readFileBounded(max=0): %v", err)
	}
	if string(got) != "small" {
		t.Errorf("got %q, want \"small\" (default 64KiB cap should pass small file through)", got)
	}
}

func TestReadFileBounded_NilContext_Tolerated(t *testing.T) {
	// readBounded checks `ctx != nil` before reading ctx.Err. Pin that
	// nil ctx doesn't panic — defensive against a caller that passes
	// context.TODO and then nils it for testability.
	tmp := t.TempDir()
	path := filepath.Join(tmp, "nilctx.txt")
	if err := os.WriteFile(path, []byte("ok"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil ctx panicked: %v", r)
		}
	}()
	got, err := readFileBounded(nil, path, 100) //nolint:staticcheck // testing nil-ctx tolerance
	if err != nil {
		t.Errorf("err = %v, want nil", err)
	}
	if string(got) != "ok" {
		t.Errorf("got %q, want \"ok\"", got)
	}
}

func TestReadFileBounded_CtxCanceled_AfterRead_Errors(t *testing.T) {
	// readBounded checks ctx.Err AFTER io.ReadAll completes; a
	// pre-cancelled ctx surfaces as ctx.Err. Pin so callers can rely
	// on the cancellation signal even though *os.File.Read isn't
	// itself context-aware.
	tmp := t.TempDir()
	path := filepath.Join(tmp, "cancel.txt")
	if err := os.WriteFile(path, []byte("ignore me"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := readFileBounded(ctx, path, 100)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// ---- readClipboard ----

// fakeShellPATH returns a temp dir suitable for use as PATH that
// contains NONE of the clipboard-helper binaries. Used to exercise
// the "no helper available" error path deterministically.
func fakeShellPATH(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func TestReadClipboard_NoHelperAvailable_Errors(t *testing.T) {
	// Source contract: "If none of the helpers are available (typical
	// for headless servers), a clear error is returned so the user
	// knows what to install." Set PATH to an empty dir so the
	// exec.LookPath probes all fail; readClipboard must surface a
	// message listing the candidate binary names.
	t.Setenv("PATH", fakeShellPATH(t))
	_, err := readClipboard(context.Background(), 1024)
	if err == nil {
		t.Fatal("expected error when no clipboard helper is available")
	}
	msg := err.Error()
	// Source lists all four candidates by name in the failure message
	// so the user knows what to install. Pin each.
	for _, helper := range []string{"pbpaste", "wl-paste", "xclip", "xsel"} {
		if !strings.Contains(msg, helper) {
			t.Errorf("err = %q, missing helper name %q (operator needs the full install hint)", msg, helper)
		}
	}
}

func TestReadClipboard_HelperFound_RespectsBoundedMax(t *testing.T) {
	// If a helper IS present (the dev machine usually has pbpaste on
	// macOS) the bounded-truncation contract applies. We don't want
	// to depend on the test machine's clipboard state, so skip when
	// no helper exists.
	for _, h := range []string{"pbpaste", "wl-paste", "xclip", "xsel"} {
		if _, err := exec.LookPath(h); err == nil {
			t.Logf("found %q on PATH; verifying readClipboard doesn't crash and respects max cap", h)
			// Call with a tiny cap; the result is whatever's in the
			// clipboard, possibly truncated. We just verify no panic
			// and that if truncation kicked in, the marker is present.
			got, err := readClipboard(context.Background(), 1)
			if err != nil {
				// pbpaste returning empty isn't an error; other helpers
				// may fail without a display. Tolerate either.
				t.Logf("readClipboard returned err = %v (acceptable in test environments)", err)
				return
			}
			// If we got more than the cap, marker must be present.
			if len(got) > 1 && !strings.Contains(string(got), "(truncated)") {
				t.Errorf("output > cap but missing truncation marker: %q", got)
			}
			return
		}
	}
	t.Skip("no clipboard helper on PATH; skip the helper-found path")
}

// ---- readBounded direct edge cases ----

func TestReadBounded_DirectReader(t *testing.T) {
	// readFileBounded is a thin wrapper; the underlying readBounded
	// works with any io.Reader. Verify with a strings.Reader so the
	// io.Reader contract is exercised independently of the file
	// open path.
	r := strings.NewReader(strings.Repeat("x", 100))
	got, err := readBounded(context.Background(), r, 30)
	if err != nil {
		t.Fatalf("readBounded: %v", err)
	}
	if !strings.Contains(string(got), "(truncated)") {
		t.Errorf("missing truncation marker: %q", got)
	}
	if !strings.HasPrefix(string(got), strings.Repeat("x", 30)) {
		t.Errorf("prefix wrong: %q", got)
	}
}

func TestReadBounded_EmptyReader_ReturnsEmpty(t *testing.T) {
	got, err := readBounded(context.Background(), strings.NewReader(""), 100)
	if err != nil {
		t.Fatalf("readBounded: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("empty reader produced %q; want empty", got)
	}
}

func TestReadBounded_ReadErrorBubbles(t *testing.T) {
	// A reader that returns a non-EOF error must surface; the bounded
	// wrapper must not swallow it to an empty-result false success.
	want := errors.New("disk gone")
	got, err := readBounded(context.Background(), &errorReader{err: want}, 100)
	if err == nil {
		t.Fatalf("expected error, got bytes %q", got)
	}
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want errors.Is(err, %v)", err, want)
	}
}

type errorReader struct{ err error }

func (e *errorReader) Read([]byte) (int, error) { return 0, e.err }

var _ io.Reader = (*errorReader)(nil)
