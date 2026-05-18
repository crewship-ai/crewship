package orchestrator

import (
	"strings"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// orchestrator_exec_env.go — tmux cache + session-name helper.
//
// Covers four small methods on Orchestrator:
//   - TmuxSessionName  : pure helper, "agent-" + slug
//   - tmuxCacheLookup  : read cache with double-bool (value, present)
//   - tmuxCacheStore   : write cache with size-cap flush on overflow
//   - InvalidateTmuxCache : delete-by-id (called on container removal)
//
// The cache is per-container memoization of `command -v tmux` probe
// results (~50ms per probe). Without proper bounds + invalidation,
// the map would grow forever as a long-running crewshipd churns
// containers (each ID is 64 hex chars; a busy workspace easily blows
// past MB-scale leakage over weeks).
//
// Concurrent access is real (every agent message hits the cache);
// the test exercises -race to catch lock-discipline regressions.
// ---------------------------------------------------------------------------

func TestTmuxSessionName_Format(t *testing.T) {
	// Pure helper. Pin the literal "agent-" prefix so a tmux-side
	// regex / grep that depends on the prefix doesn't drift silently.
	cases := []struct {
		in, want string
	}{
		{"alice", "agent-alice"},
		{"", "agent-"},                       // empty slug still produces a deterministic name
		{"with-dashes", "agent-with-dashes"}, // dashes pass through
		{"a", "agent-a"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := TmuxSessionName(tc.in); got != tc.want {
				t.Errorf("TmuxSessionName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// newTmuxCacheOrchestrator wires the minimum Orchestrator fields the
// cache methods touch. The full New() constructor pulls in container
// providers / loggers we don't need here.
func newTmuxCacheOrchestrator() *Orchestrator {
	return &Orchestrator{
		tmuxCache: make(map[string]bool),
	}
}

// ---- tmuxCacheLookup ----

func TestTmuxCacheLookup_EmptyCache_NotPresent(t *testing.T) {
	o := newTmuxCacheOrchestrator()
	v, ok := o.tmuxCacheLookup("ct-unknown")
	if ok {
		t.Errorf("ok = true on empty cache (got value %v); want false", v)
	}
	if v {
		t.Errorf("v = true on empty cache; zero-value should be false")
	}
}

func TestTmuxCacheLookup_ReturnsStoredValue_BothBranches(t *testing.T) {
	// The double-bool API (value, present) is load-bearing: callers
	// distinguish "cached false (no tmux)" from "not cached at all".
	// A regression collapsing the two would re-probe every call on
	// no-tmux containers, defeating the cache.
	o := newTmuxCacheOrchestrator()
	o.tmuxCacheStore("ct-yes", true)
	o.tmuxCacheStore("ct-no", false)

	if v, ok := o.tmuxCacheLookup("ct-yes"); !ok || !v {
		t.Errorf("lookup ct-yes = (v=%v, ok=%v), want (true, true)", v, ok)
	}
	if v, ok := o.tmuxCacheLookup("ct-no"); !ok || v {
		t.Errorf("lookup ct-no = (v=%v, ok=%v), want (false, true) — cached negative must NOT report as absent", v, ok)
	}
	if v, ok := o.tmuxCacheLookup("ct-not-stored"); ok {
		t.Errorf("lookup ct-not-stored = (v=%v, ok=%v), want (_, false)", v, ok)
	}
}

// ---- tmuxCacheStore ----

func TestTmuxCacheStore_OverwritesExistingEntry(t *testing.T) {
	// A container can transition from "no tmux" → "tmux installed"
	// across image rebuilds (same ID would NOT survive that, but
	// defensively pin overwrite semantics anyway).
	o := newTmuxCacheOrchestrator()
	o.tmuxCacheStore("ct-1", false)
	o.tmuxCacheStore("ct-1", true)

	v, ok := o.tmuxCacheLookup("ct-1")
	if !ok || !v {
		t.Errorf("after overwrite, lookup = (v=%v, ok=%v), want (true, true)", v, ok)
	}
}

func TestTmuxCacheStore_OverflowFlushesEntireCache(t *testing.T) {
	// Source: "Reset rather than evict-oldest" — at tmuxCacheMaxEntries
	// the map is reallocated empty before the new entry lands. Pin this
	// because the alternative (do-nothing-on-full) would let any single
	// busy workspace cap the cache forever, while evict-oldest would
	// need LRU tracking we explicitly don't have.
	o := newTmuxCacheOrchestrator()

	// Fill to exactly tmuxCacheMaxEntries.
	for i := 0; i < tmuxCacheMaxEntries; i++ {
		o.tmuxCacheStore("ct-pre-"+itoaForCache(i), true)
	}
	if len(o.tmuxCache) != tmuxCacheMaxEntries {
		t.Fatalf("seed: len = %d, want %d", len(o.tmuxCache), tmuxCacheMaxEntries)
	}

	// One more store → triggers reset + insert. Cache size drops to 1.
	o.tmuxCacheStore("ct-overflow", true)
	if got := len(o.tmuxCache); got != 1 {
		t.Errorf("after overflow, len = %d, want 1 (reset then insert)", got)
	}

	// The overflow entry must be present.
	if v, ok := o.tmuxCacheLookup("ct-overflow"); !ok || !v {
		t.Errorf("overflow entry lookup = (v=%v, ok=%v), want (true, true)", v, ok)
	}
	// Any of the pre-fill entries must NOT be present (the flush
	// dropped them all).
	if _, ok := o.tmuxCacheLookup("ct-pre-0"); ok {
		t.Errorf("pre-fill entry survived overflow flush; reset must drop ALL prior entries")
	}
	if _, ok := o.tmuxCacheLookup("ct-pre-" + itoaForCache(tmuxCacheMaxEntries/2)); ok {
		t.Errorf("middle pre-fill entry survived overflow flush; reset must drop ALL prior entries")
	}
}

// ---- InvalidateTmuxCache ----

func TestInvalidateTmuxCache_RemovesEntry(t *testing.T) {
	o := newTmuxCacheOrchestrator()
	o.tmuxCacheStore("ct-going-away", true)
	if _, ok := o.tmuxCacheLookup("ct-going-away"); !ok {
		t.Fatal("setup failed: store didn't land")
	}

	o.InvalidateTmuxCache("ct-going-away")
	if _, ok := o.tmuxCacheLookup("ct-going-away"); ok {
		t.Error("entry still present after Invalidate; subsequent ConfigCheck would re-use stale cached value")
	}
}

func TestInvalidateTmuxCache_UnknownID_NoOp(t *testing.T) {
	// Source comment: "Safe to call for unknown IDs". Pin so a
	// regression to "panic on missing key" surfaces — InvalidateTmux-
	// Cache is called from container-removal cleanup paths where the
	// cache may or may not have been populated.
	o := newTmuxCacheOrchestrator()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Invalidate on unknown ID panicked: %v", r)
		}
	}()
	o.InvalidateTmuxCache("ct-never-stored")
}

func TestInvalidateTmuxCache_LeavesOtherEntriesAlone(t *testing.T) {
	// Targeted delete — must not nuke neighbours.
	o := newTmuxCacheOrchestrator()
	o.tmuxCacheStore("ct-keep-1", true)
	o.tmuxCacheStore("ct-doomed", true)
	o.tmuxCacheStore("ct-keep-2", false)

	o.InvalidateTmuxCache("ct-doomed")

	if _, ok := o.tmuxCacheLookup("ct-keep-1"); !ok {
		t.Errorf("ct-keep-1 evicted by neighbour invalidation")
	}
	if _, ok := o.tmuxCacheLookup("ct-keep-2"); !ok {
		t.Errorf("ct-keep-2 evicted by neighbour invalidation")
	}
	if _, ok := o.tmuxCacheLookup("ct-doomed"); ok {
		t.Errorf("ct-doomed still present after Invalidate")
	}
}

// ---- Concurrent access (run under -race) ----

func TestTmuxCache_ConcurrentReadersAndWriters_NoRace(t *testing.T) {
	// All three methods take tmuxCacheMu under either RLock or Lock.
	// Pin lock discipline with a write+read+invalidate stampede. Under
	// -race a missing Lock anywhere would surface as a data-race
	// failure in the runtime detector.
	o := newTmuxCacheOrchestrator()
	const workers = 16
	const iters = 200

	var wg sync.WaitGroup
	wg.Add(workers * 3)

	// Writers.
	for w := 0; w < workers; w++ {
		w := w
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				o.tmuxCacheStore("ct-"+itoaForCache(w)+"-"+itoaForCache(i), i%2 == 0)
			}
		}()
	}
	// Readers.
	for w := 0; w < workers; w++ {
		w := w
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				_, _ = o.tmuxCacheLookup("ct-" + itoaForCache(w) + "-" + itoaForCache(i))
			}
		}()
	}
	// Invalidators.
	for w := 0; w < workers; w++ {
		w := w
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				o.InvalidateTmuxCache("ct-" + itoaForCache(w) + "-" + itoaForCache(i))
			}
		}()
	}
	wg.Wait()
}

// ---- TmuxSessionName ⇆ slug regex consistency check ----

func TestTmuxSessionName_OutputIsTmuxSessionSafe(t *testing.T) {
	// tmux session names can't contain ":" or "." — the resulting
	// "agent-<slug>" must avoid both. Pin so a future slug format
	// that allowed e.g. "alice.dev" would surface here before
	// breaking `tmux attach -t agent-alice.dev`.
	// Limited to common slug shapes — a future broader slug regex
	// would need to update both this test and the slug validator.
	for _, slug := range []string{"alice", "bob-1", "team_alpha", "x"} {
		name := TmuxSessionName(slug)
		for _, bad := range []string{":", "."} {
			if strings.Contains(name, bad) {
				t.Errorf("TmuxSessionName(%q) = %q, contains forbidden tmux char %q", slug, name, bad)
			}
		}
	}
}

// itoaForCache: tiny strconv-free int-to-string helper. Kept local
// to avoid a name collision with the package's existing itoa helper
// in waitpoints_sweep_once_test.go (same package).
func itoaForCache(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	neg := i < 0
	if neg {
		i = -i
	}
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
