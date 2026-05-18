package orchestrator

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// orchestrator.go — setter wiring, no-op-fallback getters, and the small
// truncate helpers. These are server-bootstrap surfaces that are wired
// once and then read on every assignment — a regression that swaps the
// target field (e.g. SetMemoryMetrics → o.presence = m) compiles cleanly
// and silently disables the wrong subsystem.
// ---------------------------------------------------------------------------

func newOrchTestInstance(t *testing.T) *Orchestrator {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	// nil container/state providers are fine for setter tests — we never
	// dispatch a run.
	return New(nil, nil, logger)
}

// ---- HookDispatcher / ApprovalGate / EpisodicRecaller / Presence / MemoryMetrics ----

type fakeHookDispatcher struct{ calls int }

func (f *fakeHookDispatcher) Dispatch(_ context.Context, _ string, _ HookEventContext) error {
	f.calls++
	return nil
}

type fakeApprovalGate struct{ calls int }

func (f *fakeApprovalGate) Check(_ context.Context, _ ApprovalCheckInput) (ApprovalDecision, error) {
	f.calls++
	return ApprovalDecision{Required: true, Pending: true}, nil
}

type fakeEpisodicRecaller struct{ calls int }

func (f *fakeEpisodicRecaller) Recall(_ context.Context, _ EpisodicRecallInput) (string, error) {
	f.calls++
	return "recalled-snippet", nil
}

type fakePresenceTracker struct{ calls int }

func (f *fakePresenceTracker) Track(_ context.Context, _ PresenceInput) error {
	f.calls++
	return nil
}

type fakeMemoryMetrics struct{ calls int }

func (f *fakeMemoryMetrics) EntriesSinceLastMemoryUpdate(_ context.Context, _, _ string) (int64, error) {
	f.calls++
	return 7, nil
}

func (f *fakeMemoryMetrics) AgentSpendLast24h(_ context.Context, _, _ string) (float64, int64, int64, error) {
	f.calls++
	return 1.23, 4567, 8, nil
}

func TestOrchestrator_SetHooksDispatcher_StoresAndGetterRoutes(t *testing.T) {
	o := newOrchTestInstance(t)
	fake := &fakeHookDispatcher{}
	o.SetHooksDispatcher(fake)
	if _, ok := o.getHooks().(*fakeHookDispatcher); !ok {
		t.Fatal("getHooks did not return the fake dispatcher")
	}
	if err := o.getHooks().Dispatch(context.Background(), "pre_agent", HookEventContext{}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if fake.calls != 1 {
		t.Errorf("fake.calls = %d, want 1", fake.calls)
	}

	// nil reset → getHooks must hand back a noopHooks (not nil) so
	// emit sites stay nil-free.
	o.SetHooksDispatcher(nil)
	if _, ok := o.getHooks().(noopHooks); !ok {
		t.Errorf("getHooks after nil reset = %T, want noopHooks", o.getHooks())
	}
	if err := o.getHooks().Dispatch(context.Background(), "x", HookEventContext{}); err != nil {
		t.Errorf("noop Dispatch returned err: %v", err)
	}
}

func TestOrchestrator_SetApprovalGate_StoresAndGetterRoutes(t *testing.T) {
	o := newOrchTestInstance(t)
	fake := &fakeApprovalGate{}
	o.SetApprovalGate(fake)
	if _, ok := o.getApprovalGate().(*fakeApprovalGate); !ok {
		t.Fatal("getApprovalGate did not return the fake")
	}
	dec, err := o.getApprovalGate().Check(context.Background(), ApprovalCheckInput{})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !dec.Required {
		t.Errorf("fake should have returned Required=true, got %+v", dec)
	}

	// nil reset → noopGate (always Approved=true, never blocks).
	o.SetApprovalGate(nil)
	noop := o.getApprovalGate()
	if _, ok := noop.(noopGate); !ok {
		t.Errorf("getApprovalGate after nil reset = %T, want noopGate", noop)
	}
	dec, _ = noop.Check(context.Background(), ApprovalCheckInput{})
	if dec.Required || dec.Denied || !dec.Approved {
		t.Errorf("noop decision = %+v, want {Required:false, Approved:true}", dec)
	}
}

func TestOrchestrator_SetEpisodicRecall_NilReturnsNilFromGetter(t *testing.T) {
	// EpisodicRecaller is the only setter whose getter returns nil
	// (callers explicitly check). Pin that contract so a future refactor
	// that adds a noop fallback doesn't silently change behavior — the
	// "nil → skip injection" branch is the documented production path.
	o := newOrchTestInstance(t)
	if o.getEpisodicRecall() != nil {
		t.Error("getEpisodicRecall on fresh orchestrator = non-nil, want nil")
	}
	fake := &fakeEpisodicRecaller{}
	o.SetEpisodicRecall(fake)
	if o.getEpisodicRecall() == nil {
		t.Fatal("getEpisodicRecall after SetEpisodicRecall(fake) = nil")
	}
	if _, err := o.getEpisodicRecall().Recall(context.Background(), EpisodicRecallInput{}); err != nil {
		t.Fatalf("recall: %v", err)
	}
	if fake.calls != 1 {
		t.Errorf("fake.calls = %d, want 1", fake.calls)
	}

	// nil reset must restore the nil sentinel; getter returns nil.
	o.SetEpisodicRecall(nil)
	if o.getEpisodicRecall() != nil {
		t.Error("getEpisodicRecall after nil reset = non-nil; the nil-sentinel contract was lost")
	}
}

func TestOrchestrator_SetPresenceTracker_NilFallsBackToNoop(t *testing.T) {
	o := newOrchTestInstance(t)
	// Fresh orchestrator: getPresence must return noopPresence (not nil).
	if _, ok := o.getPresence().(noopPresence); !ok {
		t.Errorf("getPresence on fresh = %T, want noopPresence", o.getPresence())
	}
	fake := &fakePresenceTracker{}
	o.SetPresenceTracker(fake)
	if err := o.getPresence().Track(context.Background(), PresenceInput{Status: "online"}); err != nil {
		t.Fatalf("track: %v", err)
	}
	if fake.calls != 1 {
		t.Errorf("fake.calls = %d, want 1", fake.calls)
	}
	o.SetPresenceTracker(nil)
	if _, ok := o.getPresence().(noopPresence); !ok {
		t.Errorf("getPresence after nil reset = %T, want noopPresence", o.getPresence())
	}
}

func TestOrchestrator_SetMemoryMetrics_NilFallsBackToNoop(t *testing.T) {
	o := newOrchTestInstance(t)
	// Fresh: noop returns 0 / 0 / 0 — caller skips the block.
	mm := o.getMemoryMetrics()
	n, err := mm.EntriesSinceLastMemoryUpdate(context.Background(), "ws", "ag")
	if err != nil || n != 0 {
		t.Errorf("noop EntriesSince = (%d, %v), want (0, nil)", n, err)
	}
	usd, tokens, calls, err := mm.AgentSpendLast24h(context.Background(), "ws", "ag")
	if err != nil || usd != 0 || tokens != 0 || calls != 0 {
		t.Errorf("noop AgentSpendLast24h = (%v, %d, %d, %v), want all zero", usd, tokens, calls, err)
	}

	fake := &fakeMemoryMetrics{}
	o.SetMemoryMetrics(fake)
	if n, _ := o.getMemoryMetrics().EntriesSinceLastMemoryUpdate(context.Background(), "ws", "ag"); n != 7 {
		t.Errorf("fake routed EntriesSince = %d, want 7", n)
	}
	o.SetMemoryMetrics(nil)
	if _, ok := o.getMemoryMetrics().(noopMemoryMetrics); !ok {
		t.Errorf("getMemoryMetrics after nil reset = %T, want noopMemoryMetrics", o.getMemoryMetrics())
	}
}

// ---- SetJournal / getJournal nil branches ----

type fakeJournal struct{ calls int }

func (f *fakeJournal) Emit(_ context.Context, _ JournalEntry) (string, error) {
	f.calls++
	return "j1", nil
}

func TestOrchestrator_SetJournal_NilFallsBackToNoop(t *testing.T) {
	o := newOrchTestInstance(t)
	// Fresh: getJournal returns noopJournal.
	if _, ok := o.getJournal().(noopJournal); !ok {
		t.Errorf("getJournal on fresh = %T, want noopJournal", o.getJournal())
	}
	fake := &fakeJournal{}
	o.SetJournal(fake)
	if _, err := o.getJournal().Emit(context.Background(), JournalEntry{}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if fake.calls != 1 {
		t.Errorf("fake.calls = %d, want 1", fake.calls)
	}
	o.SetJournal(nil)
	if _, ok := o.getJournal().(noopJournal); !ok {
		t.Errorf("getJournal after nil reset = %T, want noopJournal", o.getJournal())
	}
}

// ---- Keeper / IPC / Container ----

func TestOrchestrator_KeeperToggle(t *testing.T) {
	o := newOrchTestInstance(t)
	if o.KeeperEnabled() {
		t.Error("KeeperEnabled on fresh = true, want false")
	}
	o.SetKeeperEnabled(true)
	if !o.KeeperEnabled() {
		t.Error("KeeperEnabled after SetKeeperEnabled(true) = false")
	}
	o.SetKeeperEnabled(false)
	if o.KeeperEnabled() {
		t.Error("KeeperEnabled after SetKeeperEnabled(false) = true")
	}
}

func TestOrchestrator_SetIPCConfig_StoresBothFields(t *testing.T) {
	o := newOrchTestInstance(t)
	o.SetIPCConfig("http://host.docker.internal:8080", "secret-token")
	if o.ipcBaseURL != "http://host.docker.internal:8080" {
		t.Errorf("ipcBaseURL = %q", o.ipcBaseURL)
	}
	if o.ipcToken != "secret-token" {
		t.Errorf("ipcToken = %q", o.ipcToken)
	}
	// Second call overwrites both — no partial-update path.
	o.SetIPCConfig("http://other:9090", "other-token")
	if o.ipcBaseURL != "http://other:9090" || o.ipcToken != "other-token" {
		t.Errorf("second SetIPCConfig did not overwrite both fields: url=%q token=%q",
			o.ipcBaseURL, o.ipcToken)
	}
}

func TestOrchestrator_ContainerProvider_ReturnsConstructorArg(t *testing.T) {
	o := newOrchTestInstance(t)
	// New(nil, nil, ...) was called — must hand the nil back, not panic.
	if cp := o.ContainerProvider(); cp != nil {
		t.Errorf("ContainerProvider = %v, want nil (constructor was passed nil)", cp)
	}
}

// ---- truncateStr / truncateCmd edge cases ----

func TestTruncateStr_EdgeCases(t *testing.T) {
	cases := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"zero-n-returns-original", "hello world", 0, "hello world"},
		{"negative-n-returns-original", "hello world", -1, "hello world"},
		{"under-limit", "hi", 10, "hi"},
		{"at-limit", "hello", 5, "hello"},
		{"over-limit", "hello world", 5, "hello…"},
		{"empty", "", 5, ""},
		{"multibyte-counted-by-rune-not-byte", "ěšč", 3, "ěšč"}, // 3 runes, 6 bytes
		{"multibyte-truncated-by-rune", "ěščř", 2, "ěš…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := truncateStr(tc.in, tc.n); got != tc.want {
				t.Errorf("truncateStr(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
			}
		})
	}
}

func TestTruncateCmd_EdgeCases(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		n    int
		want string
	}{
		{"empty-argv", nil, 10, ""},
		{"single-arg", []string{"ls"}, 10, "ls"},
		{"joined-under-limit", []string{"go", "test", "./..."}, 50, "go test ./..."},
		{"joined-over-limit", []string{"go", "test", "-run", "TestSomethingVeryLong"}, 10, "go test -r…"},
		{"zero-n-returns-joined", []string{"go", "test"}, 0, "go test"},
		{"negative-n-returns-joined", []string{"go", "test"}, -1, "go test"},
		// Multibyte: joined value is "ěš čř" (5 runes / 9 bytes). At n=7
		// the rune count is under the limit so the whole string passes
		// through verbatim — the byte length (9 > 7) doesn't trip the
		// truncate. This pins that the helper uses rune-count, not
		// byte-count, on the early-return path.
		{"multibyte-rune-under-limit-passes-through", []string{"ěš", "čř"}, 7, "ěš čř"},
		// At n=4 the rune count (5) exceeds the limit, so the helper
		// truncates by rune and appends an ellipsis.
		{"multibyte-rune-truncated", []string{"ěš", "čř"}, 4, "ěš č…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := truncateCmd(tc.argv, tc.n); got != tc.want {
				t.Errorf("truncateCmd(%v, %d) = %q, want %q", tc.argv, tc.n, got, tc.want)
			}
		})
	}
}

func TestTruncateStr_NeverReturnsLongerThanInput(t *testing.T) {
	// Defensive: the function must never produce output longer than the
	// input in rune-length (the ellipsis costs 1 rune, balanced by the
	// dropped tail). A regression that off-by-ones the slice bound would
	// duplicate runes.
	for _, in := range []string{"short", "this is a moderately long string", "ěščřžýáíé"} {
		for n := 1; n <= len([]rune(in))*2; n++ {
			got := truncateStr(in, n)
			gotRunes := len([]rune(got))
			inRunes := len([]rune(in))
			// Output is either the input verbatim (n >= inRunes or n <= 0)
			// or n+1 runes (n runes + ellipsis). Both are <= inRunes+1.
			if gotRunes > inRunes+1 {
				t.Errorf("truncateStr(%q, %d) = %q (%d runes) exceeds input+1 (%d)", in, n, got, gotRunes, inRunes+1)
			}
			// If truncated, output must end with ellipsis.
			if gotRunes < inRunes && !strings.HasSuffix(got, "…") {
				t.Errorf("truncateStr(%q, %d) = %q — truncated output missing ellipsis", in, n, got)
			}
		}
	}
}
