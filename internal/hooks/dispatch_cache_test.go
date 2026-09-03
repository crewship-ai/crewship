package hooks

// dispatch_cache_test.go covers #2154: Dispatch must not query hooks_config
// when it already knows — from a prior call — that this exact
// (workspace_id, crew_id, event) triple has nothing registered, and that
// cached "nothing registered" state must never survive a write that could
// have made it wrong.
//
// TestDispatch_NegativeCache_SkipsQueryOnSecondCall proves the "no query"
// half by closing the db between the first and second Dispatch call — the
// same technique TestDispatchDistinguishesInfraErrorFromBlock in
// hooks_test.go already uses to prove ListByEvent ran (there, on a closed
// db, it errors). Here it runs in reverse: if Dispatch queries at all on
// the second call, the closed db turns that into an error; observing nil
// is exactly the proof that zero queries happened.
//
// TestDispatch_NegativeCache_InvalidatedByRegister is the test #2154 asks
// for by name: register after a cached miss, and the very next dispatch
// must still reach the handler — i.e. the write-side invalidation in
// store.go actually fires.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestDispatch_NegativeCache_SkipsQueryOnSecondCall dispatches once against
// an event with zero registered hooks (populating the negative cache),
// closes the db, then dispatches the same triple again. Pre-#2154,
// Dispatch calls ListByEvent unconditionally, so the second call would hit
// the closed db and return a *DispatchError; post-#2154, the cache hit
// means Dispatch never touches the db at all and returns nil.
func TestDispatch_NegativeCache_SkipsQueryOnSecondCall(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	ec := EventContext{WorkspaceID: "ws_test"}

	// First call: nothing registered for (ws_test, "", pre_agent_start).
	// This is a real query against a working db, and it must succeed and
	// populate the cache.
	if err := Dispatch(ctx, db, nil, EventPreAgentStart, ec); err != nil {
		t.Fatalf("first dispatch (populating the cache) = %v, want nil", err)
	}

	// A DIFFERENT (workspace, event) pair must NOT be cached from the call
	// above — closing the db and dispatching it should still error, which
	// is the control proving the closed-db trick actually detects a query
	// when one happens.
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	if err := Dispatch(ctx, db, nil, EventPostAgentStop, ec); err == nil {
		t.Fatal("control: an uncached triple against a closed db should error " +
			"(if this is nil, the closed-db technique below proves nothing)")
	}

	// Second call, same triple as the first: must hit the negative cache
	// and never touch the (now closed) db.
	if err := Dispatch(ctx, db, nil, EventPreAgentStart, ec); err != nil {
		t.Fatalf("second dispatch on the same (workspace, crew, event) = %v, want nil "+
			"(a non-nil error here means Dispatch queried the closed db instead of hitting the cache)", err)
	}
}

// TestDispatch_NegativeCache_InvalidatedByRegister dispatches once with
// nothing registered (a cache miss that populates the negative cache),
// registers a hook for that exact event, then dispatches again. The
// second dispatch must reach the handler — proving Register's
// InvalidateCache call actually clears the stale negative entry, which is
// what keeps this from quietly regressing
// TestEveryOfferedEventActuallyReachesItsHandler's register-then-dispatch
// pattern in dispatch_site_test.go.
func TestDispatch_NegativeCache_InvalidatedByRegister(t *testing.T) {
	t.Setenv(allowPrivateEnvVar, "true") // httptest binds 127.0.0.1
	db := openTestDB(t)
	ctx := context.Background()
	ec := EventContext{WorkspaceID: "ws_test"}

	// Cache miss: nothing registered yet.
	if err := Dispatch(ctx, db, nil, EventPreAgentStart, ec); err != nil {
		t.Fatalf("dispatch before registering = %v, want nil", err)
	}

	hit := make(chan struct{}, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case hit <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	if _, err := Register(ctx, db, Hook{
		WorkspaceID:   "ws_test",
		Event:         EventPreAgentStart,
		HandlerKind:   HandlerKindHTTP,
		HandlerConfig: map[string]any{"url": ts.URL},
		Blocking:      true, // pre_agent_start supports blocking; run inline so no timing race with the goroutine pass
		Enabled:       true,
	}, false); err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := Dispatch(ctx, db, nil, EventPreAgentStart, ec); err != nil {
		t.Fatalf("dispatch after registering = %v, want nil", err)
	}
	select {
	case <-hit:
	case <-time.After(time.Second):
		t.Fatal("handler never ran — Register did not invalidate the negative cache entry from the earlier miss")
	}
}

// TestDispatch_NegativeCache_ConcurrentRegisterDuringLookupDoesNotStaleForever
// reproduces, deterministically, the TOCTOU dispatchWriteEpoch exists to
// close: a hook registered for the exact triple Dispatch is checking,
// landing in the narrow window between Dispatch's (already-empty)
// ListByEvent read and its cache Store. Without the epoch re-check,
// Dispatch would cache a negative that InvalidateCache already ran past —
// permanently masking the hook until an unrelated write to the same
// workspace happens to invalidate it. dispatchRaceHookForTest lands the
// write deterministically in that exact window instead of racing
// goroutines for it, the same technique store.go's
// updateEventRaceHookForTest uses for its own TOCTOU.
func TestDispatch_NegativeCache_ConcurrentRegisterDuringLookupDoesNotStaleForever(t *testing.T) {
	t.Setenv(allowPrivateEnvVar, "true")
	db := openTestDB(t)
	ctx := context.Background()
	ec := EventContext{WorkspaceID: "ws_test"}

	hit := make(chan struct{}, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case hit <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	dispatchRaceHookForTest = func() {
		// Fires after Dispatch's own ListByEvent already saw zero rows,
		// before its epoch re-check. Register a hook for the exact same
		// triple right here — the window the race needs.
		if _, err := Register(ctx, db, Hook{
			WorkspaceID:   "ws_test",
			Event:         EventPreAgentStart,
			HandlerKind:   HandlerKindHTTP,
			HandlerConfig: map[string]any{"url": ts.URL},
			Blocking:      true,
			Enabled:       true,
		}, false); err != nil {
			t.Fatalf("register during race window: %v", err)
		}
	}
	t.Cleanup(func() { dispatchRaceHookForTest = nil })

	if err := Dispatch(ctx, db, nil, EventPreAgentStart, ec); err != nil {
		t.Fatalf("first dispatch (racing the register) = %v, want nil", err)
	}
	dispatchRaceHookForTest = nil // only the first call's lookup should race a write

	// The hook registered mid-race must still be reachable — the whole
	// point of the epoch check is that Dispatch must not have cached a
	// negative for a triple that, by the time it cached, already had a
	// row.
	if err := Dispatch(ctx, db, nil, EventPreAgentStart, ec); err != nil {
		t.Fatalf("second dispatch = %v, want nil", err)
	}
	select {
	case <-hit:
	case <-time.After(time.Second):
		t.Fatal("handler never ran — the negative cache went stale from a write that raced the lookup")
	}
}
