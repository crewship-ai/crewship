package cli

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
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
