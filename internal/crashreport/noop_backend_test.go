package crashreport

import (
	"errors"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// crashreport.go — noopBackend safety-net contract.
//
// noopBackend is the fallback installed when SetBackend(nil) fires and
// the initial value before sentry_adapter.go's init() runs. The whole
// crashreport package's "fail open" promise — code can Capture(err)
// unconditionally without worrying about whether telemetry is set up —
// rests on every noopBackend method being a safe no-op. These tests
// pin each method so a regression that started panicking or returning
// non-nil would surface here before the production blast-radius of a
// silent crash on every error path.
// ---------------------------------------------------------------------------

func TestNoopBackend_Init_AlwaysReturnsNil(t *testing.T) {
	// Source: returns nil regardless of args. A non-nil error would
	// cause Init() in production to bail with "init crash backend" —
	// breaking the default-on opt-out flow for any operator who hasn't
	// installed Sentry credentials.
	b := noopBackend{}

	if err := b.Init("", "", ""); err != nil {
		t.Errorf("Init(empty args) = %v, want nil", err)
	}
	if err := b.Init("https://test@sentry.io/1", "install-1", "v1.0.0"); err != nil {
		t.Errorf("Init(real args) = %v, want nil", err)
	}
	// Long values must also not cause an allocation-sensitive failure.
	if err := b.Init("dsn", "install", string(make([]byte, 1024))); err != nil {
		t.Errorf("Init(long version) = %v, want nil", err)
	}
}

func TestNoopBackend_Capture_NoPanic_AnyInput(t *testing.T) {
	// Capture is called from any error path in the codebase. It must
	// accept nil err, real err, nil tags, and large tag maps without
	// panicking — the "fail open" contract for telemetry.
	b := noopBackend{}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Capture panicked: %v", r)
		}
	}()

	// nil error
	b.Capture(nil, nil)
	// real error, nil tags
	b.Capture(errors.New("boom"), nil)
	// real error, empty tags
	b.Capture(errors.New("boom"), map[string]string{})
	// real error, populated tags
	b.Capture(errors.New("boom"), map[string]string{
		"workspace_id": "ws-1",
		"feature":      "test",
		"version":      "v0.1.0",
	})
	// Large tag map (defensive — code paths that map every annotation
	// might end up here with many keys).
	big := make(map[string]string, 100)
	for i := 0; i < 100; i++ {
		big["tag-"+time.Now().Format("999999999")] = "value"
	}
	b.Capture(errors.New("boom"), big)
}

func TestNoopBackend_Flush_NeverBlocks(t *testing.T) {
	// Flush in the real backend blocks up to timeout for the SDK queue
	// to drain. The noop must return INSTANTLY regardless of the
	// timeout value — a regression that called time.Sleep(timeout)
	// would freeze shutdown for the full duration on every dev build
	// that never wired Sentry.
	b := noopBackend{}

	for _, timeout := range []time.Duration{
		0,
		1 * time.Millisecond,
		5 * time.Second, // would freeze the test if implemented as sleep
		1 * time.Hour,
	} {
		t.Run(timeout.String(), func(t *testing.T) {
			start := time.Now()
			b.Flush(timeout)
			if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
				t.Errorf("Flush(%v) took %v; expected near-instant return", timeout, elapsed)
			}
		})
	}
}

// ---- SetBackend(nil) restore-to-noop branch ----

func TestSetBackend_NilFallsBackToNoop(t *testing.T) {
	// SetBackend(nil) installs noopBackend so subsequent Capture calls
	// don't deref a nil interface. Pin the swap via CurrentBackend so
	// a regression that left the field nil would surface here.
	orig := CurrentBackend()
	t.Cleanup(func() { SetBackend(orig) })

	SetBackend(nil)
	got := CurrentBackend()
	if _, ok := got.(noopBackend); !ok {
		t.Errorf("CurrentBackend after SetBackend(nil) = %T, want noopBackend", got)
	}

	// Capture through the restored noop must not panic — the whole
	// reason the nil-replace exists.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Capture after SetBackend(nil) panicked: %v", r)
		}
	}()
	Capture(errors.New("post-nil-restore"), nil)
}

func TestSetBackend_RealBackend_ReplacesPriorValue(t *testing.T) {
	// SetBackend with a non-nil arg must store it (regression against
	// an accidental noop-swap that ignores the input).
	orig := CurrentBackend()
	t.Cleanup(func() { SetBackend(orig) })

	first := &fakeRecordingBackend{}
	SetBackend(first)
	if CurrentBackend() != first {
		t.Errorf("first SetBackend did not store the value")
	}
	second := &fakeRecordingBackend{}
	SetBackend(second)
	if CurrentBackend() != second {
		t.Errorf("second SetBackend did not replace the first")
	}
}

// fakeRecordingBackend is a tiny Backend impl whose only purpose is to
// be a non-noop pointer the test can identity-compare against.
type fakeRecordingBackend struct{}

func (*fakeRecordingBackend) Init(_, _, _ string) error            { return nil }
func (*fakeRecordingBackend) Capture(_ error, _ map[string]string) {}
func (*fakeRecordingBackend) Flush(_ time.Duration)                {}

// Compile-time assertion: catches a Backend interface drift that would
// make the test stubs (or noopBackend itself) silently stop satisfying it.
var (
	_ Backend = noopBackend{}
	_ Backend = (*fakeRecordingBackend)(nil)
)
