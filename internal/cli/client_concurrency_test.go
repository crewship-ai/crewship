package cli

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// One Client, many goroutines. This is not a hypothetical: `crewship diff`
// fans out two goroutines over one client (cmd_diff.go), and `crewship me`,
// `today` and `now` each fan out three (cmd_quickactions.go). Every request
// funnels through NewRequest → resolveWorkspaceID, which memoises into the
// client, so a shared client means concurrent writes to that memo.
//
// These tests are the -race regression net for that. They assert ordinary
// success too, but their real job is to be run with -race: against the
// pre-fix client, where the memo lived in two unguarded Client fields, all
// three report a data race — and the two slug tests additionally fail on
// the duplicate-preflight assertion (8 preflights for an 8-way fan-out).

// clientFanout runs n concurrent Gets on c and returns the errors, in no
// particular order.
func clientFanout(t *testing.T, c *Client, n int) []error {
	t.Helper()
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func() {
			defer wg.Done()
			resp, err := c.Get("/api/v1/agents")
			if err != nil {
				errs[i] = err
				return
			}
			resp.Body.Close()
		}()
	}
	wg.Wait()
	return errs
}

// A CUID workspace takes the short-circuit branch of resolveWorkspaceID —
// no preflight, but it still writes the memo, so it races just as hard as
// the slug path. This is the common case: most configured workspaces are
// already CUIDs, so this is what a real `crewship diff` hits.
func TestClientConcurrentRequests_CUIDWorkspace(t *testing.T) {
	t.Setenv("CREWSHIP_NO_SLUG_CACHE", "1")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/workspaces" {
			t.Errorf("a CUID workspace must not trigger the slug preflight")
		}
		if got := r.URL.Query().Get("workspace_id"); got != "cabcdefghijklmnopqrst" {
			t.Errorf("workspace_id = %q, want the configured CUID", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "cabcdefghijklmnopqrst")
	for i, err := range clientFanout(t, c, 8) {
		if err != nil {
			t.Errorf("goroutine %d: %v", i, err)
		}
	}
}

// A slug workspace takes the preflight branch. Beyond not racing, the
// resolve must happen once: without the lock every goroutine that arrives
// before the memo is populated fires its own GET /api/v1/workspaces, so a
// three-way fan-out pays three preflights instead of one.
func TestClientConcurrentRequests_SlugResolvedOnce(t *testing.T) {
	t.Setenv("CREWSHIP_NO_SLUG_CACHE", "1")

	var preflights atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/workspaces" {
			preflights.Add(1)
			_, _ = w.Write([]byte(`[{"id":"cabcdefghijklmnopqrst","slug":"alpha"}]`))
			return
		}
		if got := r.URL.Query().Get("workspace_id"); got != "cabcdefghijklmnopqrst" {
			t.Errorf("workspace_id = %q, want the resolved CUID", got)
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "alpha")
	for i, err := range clientFanout(t, c, 8) {
		if err != nil {
			t.Errorf("goroutine %d: %v", i, err)
		}
	}
	if n := preflights.Load(); n != 1 {
		t.Errorf("slug preflights = %d, want exactly 1 for a fan-out over one client", n)
	}
}

// The definitive-miss memo (wsNotFound) is a second field on the same code
// path, written on the failure branch. Every goroutine must see the same
// typed error, and the preflight must not be re-run per goroutine.
func TestClientConcurrentRequests_DefinitiveMiss(t *testing.T) {
	t.Setenv("CREWSHIP_NO_SLUG_CACHE", "1")

	var preflights atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/workspaces" {
			preflights.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"id":"cabcdefghijklmnopqrst","slug":"alpha"}]`))
			return
		}
		t.Errorf("unexpected request to %s — a definitive slug miss must not reach the API", r.URL.Path)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "beta") // not in the list
	for i, err := range clientFanout(t, c, 8) {
		var wsErr *WorkspaceNotFoundError
		if !errors.As(err, &wsErr) {
			t.Errorf("goroutine %d: err = %v (%T), want *WorkspaceNotFoundError", i, err, err)
		}
	}
	if n := preflights.Load(); n != 1 {
		t.Errorf("slug preflights = %d, want exactly 1 for a fan-out over one client", n)
	}
}

// A waiter must not be trapped behind someone else's preflight.
//
// This is the assertion that distinguishes the two ways of collapsing the
// fan-out. Holding the mutex across the round-trip also yields one preflight,
// so the tests above pass either way — but then a caller whose context is
// already cancelled cannot leave, because you cannot select on a mutex. It
// blocks for as long as the request takes, which for the TUI (one client, a
// tea.Batch re-arming every 5s for the whole session) means a stalled
// /workspaces call freezes the UI.
//
// Against the lock-across-IO shape this test hangs until the stall clears
// rather than returning; the bound below is what turns that into a failure
// instead of a hung suite.
func TestClientResolve_CancelledWaiterDoesNotBlockOnPreflight(t *testing.T) {
	release := make(chan struct{})
	var preflights atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/workspaces" {
			preflights.Add(1)
			<-release // stall the resolver until the assertions are done
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"id":"cabcdefghijklmnopqrst","slug":"alpha"}]`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	defer close(release)

	c := NewClient(srv.URL, "", "alpha")

	// Resolver: parks inside the preflight until release is closed.
	resolverIn := make(chan struct{})
	go func() {
		close(resolverIn)
		if resp, err := c.Get("/api/v1/agents"); err == nil {
			resp.Body.Close()
		}
	}()
	<-resolverIn
	// Give the resolver time to actually reach the server, so the waiter below
	// is genuinely second. Polling the counter beats sleeping a fixed span.
	for range 200 {
		if preflights.Load() > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if preflights.Load() == 0 {
		t.Fatal("resolver never reached the preflight; test cannot distinguish waiter behaviour")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before the waiter asks for anything

	done := make(chan error, 1)
	go func() {
		_, err := c.WithContext(ctx).Get("/api/v1/agents")
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("waiter err = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("waiter blocked behind the in-flight preflight — the lock is being held across the round-trip")
	}

	if n := preflights.Load(); n != 1 {
		t.Errorf("preflights = %d, want 1 — the waiter must not start a second one", n)
	}
}

// Clones share the memo, so a fan-out over clones still costs one preflight.
//
// WithTimeout/WithHeader/WithContext all do `clone := *c`, which is why the
// state lives behind a pointer rather than inline. Nothing above exercises
// that: the fan-outs share one *Client. Without this, a future clone that
// allocates a fresh memo would reintroduce N preflights — and, before the
// pointer, N racing writers — with every existing test still green.
func TestClientConcurrentRequests_ClonesShareTheMemo(t *testing.T) {
	var preflights atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/workspaces" {
			preflights.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"id":"cabcdefghijklmnopqrst","slug":"alpha"}]`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	base := NewClient(srv.URL, "", "alpha")

	const n = 8
	var wg sync.WaitGroup
	wg.Add(n)
	errs := make([]error, n)
	for i := range n {
		go func() {
			defer wg.Done()
			// A different clone shape per goroutine, so all three clone paths
			// are covered rather than just whichever one happens to be first.
			var cl *Client
			switch i % 3 {
			case 0:
				cl = base.WithTimeout(10 * time.Second)
			case 1:
				cl = base.WithHeader("X-Test", "1")
			default:
				cl = base.WithContext(context.Background())
			}
			resp, err := cl.Get("/api/v1/agents")
			if err != nil {
				errs[i] = err
				return
			}
			resp.Body.Close()
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: %v", i, err)
		}
	}
	if got := preflights.Load(); got != 1 {
		t.Errorf("slug preflights = %d, want exactly 1 — clones must share the resolved memo, not each allocate one", got)
	}
}
