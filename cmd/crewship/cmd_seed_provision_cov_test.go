package main

// Coverage tests for cmd_seed_provision.go. All timeouts are kept in the
// tens-of-milliseconds range so the retry/poll loops exercise their
// ctx.Done branches without real sleeps.

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestCollectProvisionTargets(t *testing.T) {
	// Every builtin crew ships a devcontainer_config, so any seeded slug
	// present in crewIDs must come back; unknown slugs must not.
	if len(seeddata.Crews) == 0 {
		t.Fatal("seeddata.Crews is empty")
	}
	s0 := seeddata.Crews[0].Slug
	s1 := seeddata.Crews[len(seeddata.Crews)-1].Slug
	if seeddata.Crews[0].DevcontainerConfig == "" {
		t.Fatalf("builtin crew %q unexpectedly has no devcontainer config", s0)
	}

	crewIDs := map[string]string{
		s1:           "id-last",
		"not-a-crew": "id-x",
		s0:           "id-first",
	}
	targets := collectProvisionTargets(crewIDs)
	if len(targets) != 2 {
		t.Fatalf("targets = %+v, want 2 entries", targets)
	}
	// Sorted by slug.
	if targets[0].slug > targets[1].slug {
		t.Errorf("targets not sorted: %+v", targets)
	}
	for _, tg := range targets {
		if tg.slug == "not-a-crew" {
			t.Errorf("unknown slug leaked into targets: %+v", targets)
		}
		if tg.id != crewIDs[tg.slug] {
			t.Errorf("target %s id = %q, want %q", tg.slug, tg.id, crewIDs[tg.slug])
		}
	}

	if got := collectProvisionTargets(map[string]string{}); len(got) != 0 {
		t.Errorf("empty crewIDs should yield no targets, got %+v", got)
	}
}

func TestTriggerProvisionOnce(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		wantErr string
	}{
		{"202 accepted", http.StatusAccepted, ""},
		{"200 ok", http.StatusOK, ""},
		{"409 already running", http.StatusConflict, ""},
		{"500 hard error", http.StatusInternalServerError, "HTTP 500"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := clitest.NewStubServer()
			defer s.Close()
			s.OnPost("/api/v1/crews/c1/provision", clitest.JSONResponse(tc.status, map[string]string{"status": "x"}))

			err := triggerProvisionOnce(context.Background(), covStubClient(s), "c1", time.Second)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
			if n := len(s.CallsFor("POST", "/api/v1/crews/c1/provision")); n != 1 {
				t.Errorf("expected exactly 1 trigger POST, got %d", n)
			}
		})
	}
}

func TestTriggerProvisionOnce_RateLimitTimesOut(t *testing.T) {
	// Server keeps answering 429; the per-target timeout (50ms) expires
	// inside the backoff select, producing the slot-timeout error.
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/crews/c1/provision", clitest.ErrorResponse(http.StatusTooManyRequests, "slow down"))

	err := triggerProvisionOnce(context.Background(), covStubClient(s), "c1", 50*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timeout waiting for provision slot") {
		t.Fatalf("want slot timeout, got %v", err)
	}
}

func TestTriggerProvisions(t *testing.T) {
	t.Run("empty targets is a no-op", func(t *testing.T) {
		started, err := triggerProvisions(context.Background(), nil, nil, time.Second)
		if started != nil || err != nil {
			t.Fatalf("got (%v, %v)", started, err)
		}
	})

	t.Run("partial failure returns started subset and error", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		s.OnPost("/api/v1/crews/cgood/provision", clitest.JSONResponse(202, map[string]string{}))
		s.OnPost("/api/v1/crews/cbad/provision", clitest.ErrorResponse(500, "boom"))

		targets := []provisionTarget{{slug: "good", id: "cgood"}, {slug: "bad", id: "cbad"}}
		started, err := triggerProvisions(context.Background(), covStubClient(s), targets, time.Second)
		if err == nil || !strings.Contains(err.Error(), "failed for 1/2 crews: bad") {
			t.Fatalf("want aggregated trigger failure, got %v", err)
		}
		if len(started) != 1 || started[0].slug != "good" {
			t.Errorf("started = %+v, want only 'good'", started)
		}
	})

	t.Run("all started", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		s.OnPost("/api/v1/crews/c1/provision", clitest.JSONResponse(202, map[string]string{}))
		s.OnPost("/api/v1/crews/c2/provision", clitest.JSONResponse(202, map[string]string{}))

		targets := []provisionTarget{{slug: "b", id: "c2"}, {slug: "a", id: "c1"}}
		started, err := triggerProvisions(context.Background(), covStubClient(s), targets, time.Second)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(started) != 2 || started[0].slug != "a" || started[1].slug != "b" {
			t.Errorf("started should be sorted by slug, got %+v", started)
		}
	})
}

func TestWaitForProvisions(t *testing.T) {
	t.Run("empty targets is a no-op", func(t *testing.T) {
		if err := waitForProvisions(context.Background(), nil, nil, time.Second); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("mixed completion lists failed slugs", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		s.OnGet("/api/v1/crews/cok/provision", clitest.JSONResponse(200, map[string]string{"status": "completed"}))
		s.OnGet("/api/v1/crews/cbad/provision", clitest.JSONResponse(200, map[string]string{"status": "failed", "error": "image pull"}))

		targets := []provisionTarget{{slug: "ok", id: "cok"}, {slug: "broken", id: "cbad"}}
		err := waitForProvisions(context.Background(), covStubClient(s), targets, time.Second)
		if err == nil || !strings.Contains(err.Error(), "1/2 crews failed to provision: broken") {
			t.Fatalf("want failure listing 'broken', got %v", err)
		}
	})

	t.Run("all completed", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		s.OnGet("/api/v1/crews/c1/provision", clitest.JSONResponse(200, map[string]string{"status": "completed"}))
		if err := waitForProvisions(context.Background(), covStubClient(s),
			[]provisionTarget{{slug: "a", id: "c1"}}, time.Second); err != nil {
			t.Fatal(err)
		}
	})
}

func TestPollProvisionStatus(t *testing.T) {
	t.Run("completed", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		s.OnGet("/api/v1/crews/c1/provision", clitest.JSONResponse(200, map[string]string{"status": "completed"}))
		if err := pollProvisionStatus(context.Background(), covStubClient(s), "c1", time.Second); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("failed with server error message", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		s.OnGet("/api/v1/crews/c1/provision", clitest.JSONResponse(200, map[string]string{"status": "failed", "error": "no disk"}))
		err := pollProvisionStatus(context.Background(), covStubClient(s), "c1", time.Second)
		if err == nil || !strings.Contains(err.Error(), "provision failed: no disk") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("failed without message", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		s.OnGet("/api/v1/crews/c1/provision", clitest.JSONResponse(200, map[string]string{"status": "failed"}))
		err := pollProvisionStatus(context.Background(), covStubClient(s), "c1", time.Second)
		if err == nil || err.Error() != "provision failed" {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("pending until timeout", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		s.OnGet("/api/v1/crews/c1/provision", clitest.JSONResponse(200, map[string]string{"status": "running"}))
		err := pollProvisionStatus(context.Background(), covStubClient(s), "c1", 50*time.Millisecond)
		if err == nil || !strings.Contains(err.Error(), "timeout after") || !strings.Contains(err.Error(), "last status: running") {
			t.Fatalf("want timeout with last status, got %v", err)
		}
	})

	t.Run("hard status fetch error aborts", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		s.OnGet("/api/v1/crews/c1/provision", clitest.ErrorResponse(500, "db down"))
		err := pollProvisionStatus(context.Background(), covStubClient(s), "c1", time.Second)
		if err == nil || !strings.Contains(err.Error(), "poll status") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("429 is retried until timeout, not fatal", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		s.OnGet("/api/v1/crews/c1/provision", clitest.ErrorResponse(http.StatusTooManyRequests, "Too Many Requests"))
		err := pollProvisionStatus(context.Background(), covStubClient(s), "c1", 50*time.Millisecond)
		if err == nil || !strings.Contains(err.Error(), "timeout after") {
			t.Fatalf("429 should soft-retry into timeout, got %v", err)
		}
		if strings.Contains(err.Error(), "poll status") {
			t.Errorf("429 must not be treated as a hard poll error: %v", err)
		}
	})
}
