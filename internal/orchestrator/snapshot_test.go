package orchestrator

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// panickingContainer panics on every Exec call so tests can verify
// recordContainerSnapshot's defer-recover keeps the run completion path
// alive even when the provider is broken.
type panickingContainer struct{ snapshotStubContainer }

func (p *panickingContainer) Exec(_ context.Context, _ provider.ExecConfig) (*provider.ExecResult, error) {
	panic("simulated provider explosion")
}

// erroringContainer returns a hard error from Exec — Capture's three
// probes all fail, which the function reports as "no probes succeeded".
type erroringContainer struct{ snapshotStubContainer }

func (e *erroringContainer) Exec(_ context.Context, _ provider.ExecConfig) (*provider.ExecResult, error) {
	return nil, errors.New("docker daemon unavailable")
}

// ctxAwareStubContainer mirrors snapshotStubContainer but propagates ctx
// cancellation as Exec error so the test can verify graceful skip.
type ctxAwareStubContainer struct{ snapshotStubContainer }

func (c *ctxAwareStubContainer) Exec(ctx context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &provider.ExecResult{Reader: io.NopCloser(strings.NewReader(c.snapshotStubContainer.reply(cfg)))}, nil
}

// failingEmitter is a JournalEmitter whose Emit fails when fail=true.
// Lets a test exercise the dedup-cache-on-failure path without spinning
// up a real journal writer.
type failingEmitter struct {
	mu        sync.Mutex
	fail      bool
	attempted int
	succeeded int
}

func (f *failingEmitter) Emit(_ context.Context, _ JournalEntry) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempted++
	if f.fail {
		return "", errors.New("simulated journal failure")
	}
	f.succeeded++
	return "id", nil
}

func (f *failingEmitter) attempts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempted
}

func (f *failingEmitter) successes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.succeeded
}

// hangingContainer.Exec blocks on ctx.Done() so the test can verify the
// snapshot probe respects snapshotProbeTimeout instead of wedging the
// agent-run completion path forever.
type hangingContainer struct{ snapshotStubContainer }

func (h *hangingContainer) Exec(ctx context.Context, _ provider.ExecConfig) (*provider.ExecResult, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func payloadKeys(p map[string]any) []string {
	out := make([]string, 0, len(p))
	for k := range p {
		out = append(out, k)
	}
	return out
}

// snapshotStubContainer is a minimal ContainerProvider whose Exec method
// returns canned output for the four probe scripts containerstate fires.
// Each probe is independent so a test can flip individual sources between
// runs to assert "hash changed → emit" / "hash same → skip".
type snapshotStubContainer struct {
	apt string
	pip string
	npm string
	os  string
}

func (s *snapshotStubContainer) reply(cfg provider.ExecConfig) string {
	if len(cfg.Cmd) < 3 || cfg.Cmd[0] != "sh" {
		return ""
	}
	script := cfg.Cmd[2]
	switch {
	case strings.Contains(script, "dpkg-query"):
		return s.apt
	case strings.Contains(script, "pip freeze") || strings.Contains(script, "pip3 freeze"):
		return s.pip
	case strings.Contains(script, "npm ls -g"):
		return s.npm
	case strings.Contains(script, "/etc/os-release"):
		return s.os
	default:
		return ""
	}
}

func (s *snapshotStubContainer) Exec(_ context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	return &provider.ExecResult{Reader: io.NopCloser(strings.NewReader(s.reply(cfg)))}, nil
}

func (s *snapshotStubContainer) EnsureCrewRuntime(_ context.Context, _ provider.CrewConfig) (string, error) {
	return "", nil
}
func (s *snapshotStubContainer) StopCrewRuntime(_ context.Context, _ string) error   { return nil }
func (s *snapshotStubContainer) RemoveCrewRuntime(_ context.Context, _ string) error { return nil }
func (s *snapshotStubContainer) ContainerStatus(_ context.Context, _ string) (*provider.ContainerStatus, error) {
	return &provider.ContainerStatus{State: "running"}, nil
}
func (s *snapshotStubContainer) ContainerStats(_ context.Context, _ string) (*provider.ContainerMetrics, error) {
	return nil, nil
}
func (s *snapshotStubContainer) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	return false, 0, nil
}
func (s *snapshotStubContainer) CrewContainerName(_ string, slug string) string {
	return "test-" + slug
}
func (s *snapshotStubContainer) CopyToContainer(_ context.Context, _, _ string, _ io.Reader) error {
	return nil
}

// TestRecordContainerSnapshot_EmitsAndDedups exercises the post-run
// container snapshot path: first call emits a container.snapshot entry;
// a second call with identical state must skip the emit (hash dedup);
// a third call after a state change must emit again. Without dedup, the
// journal would gain a heartbeat row per agent run and the "what
// actually changed?" signal drowns in noise.
func TestRecordContainerSnapshot_EmitsAndDedups(t *testing.T) {
	t.Parallel()
	stub := &snapshotStubContainer{
		apt: "git\t2.43.0-1\n",
		os:  "Ubuntu 24.04 LTS",
	}
	o := New(stub, newMemState(), slog.Default())
	rec := &chunkRecorder{}
	o.SetJournal(rec)

	req := AgentRunRequest{
		AgentID: "a1", AgentSlug: "alice",
		WorkspaceID: "ws", CrewID: "c1", CrewSlug: "team",
		ChatID: "chat", ContainerID: "ctr-1",
	}

	// First snapshot — must emit.
	o.recordContainerSnapshot(context.Background(), req, "ctr-1")
	if got := countSnapshots(rec); got != 1 {
		t.Fatalf("first run: want 1 container.snapshot entry, got %d", got)
	}

	// Identical state on the second run — hash matches the cached one,
	// so the orchestrator must skip the emit.
	o.recordContainerSnapshot(context.Background(), req, "ctr-1")
	if got := countSnapshots(rec); got != 1 {
		t.Fatalf("second run with identical state: want 1 (no new emit), got %d", got)
	}

	// State changed (an agent installed php) — hash differs, must emit.
	stub.apt = "git\t2.43.0-1\nphp\t8.3.1\n"
	o.recordContainerSnapshot(context.Background(), req, "ctr-1")
	if got := countSnapshots(rec); got != 2 {
		t.Fatalf("after install: want 2 entries, got %d", got)
	}

	// And payload of the latest must include both packages. The Emitter
	// stores the typed []containerstate.Package directly, so the assert
	// uses reflect-friendly len() rather than re-marshalling JSON.
	last := lastSnapshot(rec)
	if last == nil {
		t.Fatal("expected a snapshot entry")
	}
	got := -1
	if pkgs, ok := last.Payload["apt"].([]any); ok {
		got = len(pkgs)
	} else {
		// Typed-slice path: probe via the counts payload field which
		// recordContainerSnapshot always populates.
		if counts, ok := last.Payload["counts"].(map[string]int); ok {
			got = counts["apt"]
		}
	}
	if got != 2 {
		t.Errorf("apt count after install: want 2, got %d (payload=%+v)", got, last.Payload)
	}
}

// TestRecordContainerSnapshot_PanicRecovery verifies a panicking container
// provider doesn't take down the agent run completion path. The probe
// is best-effort by design; whatever the snapshot would have captured
// is strictly less important than the run reporting success.
func TestRecordContainerSnapshot_PanicRecovery(t *testing.T) {
	t.Parallel()
	o := New(&panickingContainer{}, newMemState(), slog.Default())
	rec := &chunkRecorder{}
	o.SetJournal(rec)

	// Must not panic.
	o.recordContainerSnapshot(context.Background(), AgentRunRequest{
		WorkspaceID: "ws", CrewID: "c1", CrewSlug: "team",
		ContainerID: "ctr-1",
	}, "ctr-1")

	if got := countSnapshots(rec); got != 0 {
		t.Errorf("panicking probe must not produce a snapshot entry, got %d", got)
	}
}

// TestRecordContainerSnapshot_SurvivesCallerCancel verifies the snapshot
// probe completes even when the caller's context was already cancelled.
// recordContainerSnapshot uses context.WithoutCancel so a user clicking
// Stop / closing the browser at end-of-run can't deprive the journal of
// the post-run snapshot — the run already committed state to the
// container, capturing what happened is more useful than respecting a
// late cancellation.
func TestRecordContainerSnapshot_SurvivesCallerCancel(t *testing.T) {
	t.Parallel()
	o := New(&ctxAwareStubContainer{
		snapshotStubContainer: snapshotStubContainer{
			apt: "git\t2.43.0-1\n",
			os:  "Ubuntu 24.04 LTS",
		},
	}, newMemState(), slog.Default())
	rec := &chunkRecorder{}
	o.SetJournal(rec)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already done — but probe must still run

	o.recordContainerSnapshot(ctx, AgentRunRequest{
		WorkspaceID: "ws", CrewID: "c1", CrewSlug: "team",
		ContainerID: "ctr-1",
	}, "ctr-1")

	if got := countSnapshots(rec); got != 1 {
		t.Errorf("snapshot must survive a cancelled caller ctx, got %d entries", got)
	}
}

// TestRecordContainerSnapshot_ExecError verifies that when the container
// provider returns an error from every Exec call, Capture reports "no
// probes succeeded" and recordContainerSnapshot silently emits nothing.
// The snapshot is best-effort: a probe failure must not generate noise
// in the journal or the agent run logs.
func TestRecordContainerSnapshot_ExecError(t *testing.T) {
	t.Parallel()
	o := New(&erroringContainer{}, newMemState(), slog.Default())
	rec := &chunkRecorder{}
	o.SetJournal(rec)

	o.recordContainerSnapshot(context.Background(), AgentRunRequest{
		WorkspaceID: "ws", CrewID: "c1", CrewSlug: "team",
		ContainerID: "ctr-1",
	}, "ctr-1")

	if got := countSnapshots(rec); got != 0 {
		t.Errorf("Exec-erroring provider must skip emit, got %d", got)
	}
}

// TestRecordContainerSnapshot_ConcurrentDedup spawns N goroutines that
// all snapshot the same container at once with identical state. Without
// proper serialization, every goroutine probes, hashes, and emits — so
// the journal would gain N near-duplicate entries on every concurrent
// run-completion burst. The expected outcome is "exactly one entry": the
// first goroutine to acquire the cache slot writes the hash and emits;
// the rest find the cached hash and skip.
func TestRecordContainerSnapshot_ConcurrentDedup(t *testing.T) {
	t.Parallel()
	o := New(&snapshotStubContainer{
		apt: "git\t2.43.0-1\n",
		os:  "Ubuntu 24.04 LTS",
	}, newMemState(), slog.Default())
	rec := &chunkRecorder{}
	o.SetJournal(rec)

	const N = 12
	var wg sync.WaitGroup
	wg.Add(N)
	start := make(chan struct{})
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			<-start
			o.recordContainerSnapshot(context.Background(), AgentRunRequest{
				WorkspaceID: "ws", CrewID: "c1", CrewSlug: "team",
				ContainerID: "ctr-1",
			}, "ctr-1")
		}()
	}
	close(start)
	wg.Wait()

	if got := countSnapshots(rec); got != 1 {
		t.Errorf("concurrent identical snapshots: want exactly 1 entry, got %d (every goroutine emitted)", got)
	}
}

// TestRecordContainerSnapshot_DifferentHashFallsThrough verifies that the
// pending-claim guard only short-circuits same-hash callers — a caller
// whose probe produced a *different* hash from the in-flight one must
// fall through and emit its own snapshot. Otherwise a real state change
// detected mid-emit (e.g. the container ran `apt-get install` between
// two near-simultaneous run completions) would be silently dropped.
//
// Setup: pre-populate snapshotPending with a sentinel hash that does not
// match anything snap.Hash() will produce, then call recordContainerSnapshot
// against a real (non-conflicting) stub. The call must emit exactly one
// entry — proving the pending-different-hash branch fell through.
func TestRecordContainerSnapshot_DifferentHashFallsThrough(t *testing.T) {
	t.Parallel()
	o := New(&snapshotStubContainer{
		apt: "git\t2.43.0-1\n",
		os:  "Ubuntu 24.04 LTS",
	}, newMemState(), slog.Default())
	rec := &chunkRecorder{}
	o.SetJournal(rec)

	// Inject a sentinel pending hash for ctr-1 that the test stub will
	// never produce. A boolean pending flag would block this call; the
	// hash-keyed flag must let it through.
	o.snapshotHashMu.Lock()
	o.snapshotPending["ctr-1"] = "sentinel-hash-not-a-real-snapshot"
	o.snapshotHashMu.Unlock()

	o.recordContainerSnapshot(context.Background(), AgentRunRequest{
		WorkspaceID: "ws", CrewID: "c1", CrewSlug: "team",
		ContainerID: "ctr-1",
	}, "ctr-1")

	if got := countSnapshots(rec); got != 1 {
		t.Errorf("different-hash call must fall through and emit: want 1 entry, got %d", got)
	}

	// The deferred cleanup only deletes our hash, so the sentinel stays.
	o.snapshotHashMu.Lock()
	pending := o.snapshotPending["ctr-1"]
	o.snapshotHashMu.Unlock()
	if pending != "sentinel-hash-not-a-real-snapshot" {
		t.Errorf("deferred cleanup must only delete OUR pending hash, "+
			"sentinel was clobbered: got %q", pending)
	}
}

// TestRecordContainerSnapshot_HungProbeBoundedByTimeout verifies the
// snapshot path can't wedge run completion on a frozen container or a
// broken probe binary. Override snapshotProbeTimeout to a sub-second
// value, hand recordContainerSnapshot a stub that blocks until ctx
// cancellation, and assert the call returns within a few timeouts'
// worth of headroom.
func TestRecordContainerSnapshot_HungProbeBoundedByTimeout(t *testing.T) {
	// Not t.Parallel — we mutate package state (snapshotProbeTimeout)
	// and parallel tests would clobber each other.
	orig := snapshotProbeTimeout
	snapshotProbeTimeout = 100 * time.Millisecond
	defer func() { snapshotProbeTimeout = orig }()

	o := New(&hangingContainer{}, newMemState(), slog.Default())
	rec := &chunkRecorder{}
	o.SetJournal(rec)

	start := time.Now()
	o.recordContainerSnapshot(context.Background(), AgentRunRequest{
		WorkspaceID: "ws", CrewID: "c1", CrewSlug: "team",
		ContainerID: "ctr-1",
	}, "ctr-1")
	elapsed := time.Since(start)

	// Each of four probes runs with the same timeout. Sequential
	// execution × four sources = 4× the timeout in the worst case;
	// allow some slop for goroutine scheduling.
	maxAcceptable := 5 * snapshotProbeTimeout
	if elapsed > maxAcceptable {
		t.Errorf("hung probe must be bounded by snapshotProbeTimeout: elapsed=%s, max=%s", elapsed, maxAcceptable)
	}
	if got := countSnapshots(rec); got != 0 {
		t.Errorf("hung probe must skip emit (no probes succeeded), got %d", got)
	}
}

// TestRecordContainerSnapshot_PayloadShape pins down the journal entry
// shape so future refactors don't accidentally drop a field a UI
// consumer depends on. Hash, counts, OS, and the per-source slices must
// all be present even when individual probes are empty.
func TestRecordContainerSnapshot_PayloadShape(t *testing.T) {
	t.Parallel()
	o := New(&snapshotStubContainer{
		apt: "git\t2.43.0-1\n",
		os:  "Ubuntu 24.04 LTS",
	}, newMemState(), slog.Default())
	rec := &chunkRecorder{}
	o.SetJournal(rec)

	o.recordContainerSnapshot(context.Background(), AgentRunRequest{
		WorkspaceID: "ws", CrewID: "c1", CrewSlug: "team", AgentID: "a1",
		ChatID: "chat", ContainerID: "ctr-1",
	}, "ctr-1")

	last := lastSnapshot(rec)
	if last == nil {
		t.Fatal("expected a snapshot entry")
	}
	for _, key := range []string{"hash", "apt", "pip", "npm", "os", "errs", "counts"} {
		if _, ok := last.Payload[key]; !ok {
			t.Errorf("payload missing field %q (got keys %v)", key, payloadKeys(last.Payload))
		}
	}
	hash, _ := last.Payload["hash"].(string)
	if len(hash) != 64 {
		t.Errorf("hash must be a 64-hex-char sha256 digest, got %q", hash)
	}
	counts, _ := last.Payload["counts"].(map[string]int)
	if counts["apt"] != 1 {
		t.Errorf("counts.apt: want 1, got %v", counts)
	}
	// Refs links the entry back to the originating chat + container so
	// the UI can correlate snapshots with the run that produced them.
	if last.Refs["chat_id"] != "chat" || last.Refs["container_id"] != "ctr-1" {
		t.Errorf("refs missing chat/container correlation: %+v", last.Refs)
	}
}

// TestRecordContainerSnapshot_FailedEmitDoesNotPoisonCache verifies the
// dedup cache is only updated on a *successful* journal write. If Emit
// fails, the next snapshot of identical state must retry the emit
// instead of being short-circuited by a stale cache entry — otherwise
// a transient journal-writer hiccup permanently blackholes one
// container's snapshots until something installs.
func TestRecordContainerSnapshot_FailedEmitDoesNotPoisonCache(t *testing.T) {
	t.Parallel()
	stub := &snapshotStubContainer{
		apt: "git\t2.43.0-1\n",
		os:  "Ubuntu 24.04 LTS",
	}
	o := New(stub, newMemState(), slog.Default())
	rec := &failingEmitter{}
	o.SetJournal(rec)

	req := AgentRunRequest{
		WorkspaceID: "ws", CrewID: "c1", CrewSlug: "team",
		ChatID: "chat", ContainerID: "ctr-1",
	}

	// First call — emit fails. Cache must NOT be updated.
	rec.fail = true
	o.recordContainerSnapshot(context.Background(), req, "ctr-1")
	if got := rec.attempts(); got != 1 {
		t.Fatalf("first call should have attempted one emit, got %d", got)
	}
	o.snapshotHashMu.Lock()
	if _, cached := o.snapshotHashCache["ctr-1"]; cached {
		o.snapshotHashMu.Unlock()
		t.Fatal("failed emit must not populate dedup cache")
	}
	o.snapshotHashMu.Unlock()

	// Second call — same state, but emitter has recovered. Because the
	// cache wasn't poisoned by the previous failure, this must retry
	// the emit (and now succeed).
	rec.fail = false
	o.recordContainerSnapshot(context.Background(), req, "ctr-1")
	if got := rec.attempts(); got != 2 {
		t.Errorf("second call must retry emit after prior failure, total attempts: want 2, got %d", got)
	}
	if got := rec.successes(); got != 1 {
		t.Errorf("second call should have one successful emit, got %d", got)
	}

	// Third call — identical state, this time cache IS warm. Must skip.
	o.recordContainerSnapshot(context.Background(), req, "ctr-1")
	if got := rec.attempts(); got != 2 {
		t.Errorf("third call with warm cache must skip emit, total attempts: want 2, got %d", got)
	}
}

// TestRecordContainerSnapshot_NoContainer is a no-op when containerID is
// empty (e.g. coordinator agents that don't hold a container handle).
func TestRecordContainerSnapshot_NoContainer(t *testing.T) {
	t.Parallel()
	o := New(&snapshotStubContainer{}, newMemState(), slog.Default())
	rec := &chunkRecorder{}
	o.SetJournal(rec)

	o.recordContainerSnapshot(context.Background(), AgentRunRequest{}, "")
	if got := countSnapshots(rec); got != 0 {
		t.Errorf("empty containerID must skip emit, got %d entries", got)
	}
}

func countSnapshots(rec *chunkRecorder) int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	n := 0
	for _, e := range rec.entries {
		if e.Type == "container.snapshot" {
			n++
		}
	}
	return n
}

func lastSnapshot(rec *chunkRecorder) *JournalEntry {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for i := len(rec.entries) - 1; i >= 0; i-- {
		if rec.entries[i].Type == "container.snapshot" {
			return &rec.entries[i]
		}
	}
	return nil
}
