package cli

import (
	"bytes"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
)

// stderrCaptureMu serializes captureStderr calls so the os.Stderr swap
// doesn't cross-contaminate when tests run in parallel. The Print*
// helpers all write to os.Stderr; without this lock a concurrent test
// would capture output from a sibling test.
var stderrCaptureMu sync.Mutex

// ---------------------------------------------------------------------------
// formatter.go — NewFormatter, glamourStyleForEnv, PrintSuccess/Error/Warning.
//
// The formatting + output paths (Table, Detail, JSON, YAML, NDJSON, Auto)
// are covered by other tests; this file fills in the constructor, the
// env-driven glamour style picker, and the three top-level Print*
// helpers (which write to os.Stderr).
// ---------------------------------------------------------------------------

func TestNewFormatter_StoresFormatAndDefaultsWriterToStdout(t *testing.T) {
	for _, format := range []string{"table", "json", "yaml", "ndjson", "quiet", ""} {
		t.Run(format, func(t *testing.T) {
			f := NewFormatter(format)
			if f == nil {
				t.Fatal("NewFormatter returned nil")
			}
			if f.Format != format {
				t.Errorf("Format = %q, want %q", f.Format, format)
			}
			if f.Writer != os.Stdout {
				t.Errorf("Writer = %v, want os.Stdout (the default for terminal commands)", f.Writer)
			}
		})
	}
}

func TestNewFormatter_WriterIsMutable(t *testing.T) {
	// Tests + CLI commands routinely swap in a buffer after construction
	// (the Table/Detail tests do this). Pin that the Writer field is
	// publicly assignable so a refactor that hides it would break those
	// tests at the same time as this one.
	f := NewFormatter("json")
	var buf bytes.Buffer
	f.Writer = &buf
	if err := f.JSON(map[string]string{"k": "v"}); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if !strings.Contains(buf.String(), `"k": "v"`) {
		t.Errorf("buffer = %q, want JSON output routed there", buf.String())
	}
}

// ---- glamourStyleForEnv ----

func TestGlamourStyleForEnv_NoColorEnv_ReturnsNotty(t *testing.T) {
	// Source comment: "Uses 'dark' as default since most terminals use
	// dark backgrounds." The NO_COLOR convention from no-color.org must
	// downgrade to the non-styled "notty" output.
	t.Setenv("NO_COLOR", "1")
	if got := glamourStyleForEnv(); got != "notty" {
		t.Errorf("got %q, want \"notty\"", got)
	}
}

func TestGlamourStyleForEnv_NoColorEnvAnyTruthyValue(t *testing.T) {
	// Source checks for non-empty only, not "1"/"true" — matches the
	// no-color.org spec: "all command-line software which adds ANSI
	// color to its output should check for the presence of a
	// NO_COLOR environment variable that, when present (regardless of
	// its value), prevents the addition of ANSI color".
	for _, v := range []string{"1", "true", "yes", "anything"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv("NO_COLOR", v)
			if got := glamourStyleForEnv(); got != "notty" {
				t.Errorf("NO_COLOR=%q → %q, want \"notty\"", v, got)
			}
		})
	}
}

func TestGlamourStyleForEnv_NoColorUnset_ReturnsDark(t *testing.T) {
	// os.Unsetenv via t.Setenv("", "") doesn't unset — use direct unset.
	t.Setenv("NO_COLOR", "") // clobber first so any prior value is gone
	if err := os.Unsetenv("NO_COLOR"); err != nil {
		t.Fatalf("unset NO_COLOR: %v", err)
	}
	if got := glamourStyleForEnv(); got != "dark" {
		t.Errorf("got %q, want \"dark\"", got)
	}
}

// captureStderr swaps os.Stderr to a pipe for the duration of fn, returning
// whatever was written. Used by the Print* tests below.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	stderrCaptureMu.Lock()
	defer stderrCaptureMu.Unlock()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer func() { _ = r.Close() }() // explicit close prevents fd leak across repeated calls

	old := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = old }()

	done := make(chan []byte, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.Bytes()
	}()

	fn()
	_ = w.Close()
	return string(<-done)
}

// ---- PrintSuccess / PrintError / PrintWarning ----

func TestPrintSuccess_GoesToStderrAndIncludesMessage(t *testing.T) {
	got := captureStderr(t, func() { PrintSuccess("backup ok") })
	if !strings.Contains(got, "backup ok") {
		t.Errorf("output = %q, want substring \"backup ok\"", got)
	}
	// Should NOT be prefixed with "Error:" or "Warning:" — those are
	// the other helpers. A regression that aliased the helpers would
	// fail here.
	if strings.Contains(got, "Error:") || strings.Contains(got, "Warning:") {
		t.Errorf("PrintSuccess output contains wrong prefix: %q", got)
	}
}

func TestPrintError_GoesToStderrWithErrorPrefix(t *testing.T) {
	got := captureStderr(t, func() { PrintError("disk full") })
	if !strings.Contains(got, "disk full") {
		t.Errorf("output = %q, want substring \"disk full\"", got)
	}
	if !strings.Contains(got, "Error:") {
		t.Errorf("output = %q, want \"Error:\" prefix", got)
	}
}

func TestPrintWarning_GoesToStderrWithWarningPrefix(t *testing.T) {
	got := captureStderr(t, func() { PrintWarning("nearing quota") })
	if !strings.Contains(got, "nearing quota") {
		t.Errorf("output = %q, want substring \"nearing quota\"", got)
	}
	if !strings.Contains(got, "Warning:") {
		t.Errorf("output = %q, want \"Warning:\" prefix", got)
	}
}

func TestPrintHelpers_DoNotPanicOnEmptyMessage(t *testing.T) {
	// Defensive: an empty msg must still produce a one-line output
	// (just the prefix + reset). A panic here would crash any caller
	// that happened to fmt.Sprintf("") into the helper.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Print helper panicked on empty msg: %v", r)
		}
	}()
	_ = captureStderr(t, func() {
		PrintSuccess("")
		PrintError("")
		PrintWarning("")
	})
}

func TestPrintHelpers_AllEndWithNewline(t *testing.T) {
	// Each Print* writes one message terminated by "\n". When two are
	// called back-to-back, the buffer must split into exactly two lines.
	got := captureStderr(t, func() {
		PrintSuccess("first")
		PrintError("second")
		PrintWarning("third")
	})
	// strings.Count handles the trailing newline ambiguity safely:
	// exactly 3 "\n" separators means 3 distinct logical lines were emitted.
	if n := strings.Count(got, "\n"); n != 3 {
		t.Errorf("newline count = %d, want 3 — each Print* must terminate with \"\\n\"", n)
	}
}
