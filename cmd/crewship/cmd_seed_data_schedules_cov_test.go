package main

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const covSchedulesPath = "/api/v1/workspaces/" + covWorkspaceIDCli10 + "/pipeline-schedules"

func TestSeedSchedules_RequiresWorkspaceID(t *testing.T) {
	client := cli.NewClient("http://127.0.0.1:0", "tok", "")
	err := seedSchedules(context.Background(), client)
	if err == nil || !strings.Contains(err.Error(), "workspace_id not set") {
		t.Errorf("expected workspace guard, got %v", err)
	}
}

func TestSeedSchedules_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := cli.NewClient("http://127.0.0.1:0", "tok", covWorkspaceIDCli10)
	if err := seedSchedules(ctx, client); err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("expected ctx cancellation, got %v", err)
	}
}

func TestSeedSchedules_Created(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost(covSchedulesPath, clitest.JSONResponse(201, map[string]string{"id": "sch1"}))
	client := cli.NewClient(s.URL(), "tok", covWorkspaceIDCli10)

	stderr, err := captureStderrCov(t, func() error {
		return seedSchedules(context.Background(), client)
	})
	if err != nil {
		t.Fatalf("seedSchedules: %v", err)
	}
	if !strings.Contains(stderr, "+ Schedule: Demo: classify ticket") {
		t.Errorf("created log missing: %q", stderr)
	}
	if !strings.Contains(stderr, "Created 3/3 demo schedule(s)") {
		t.Errorf("summary line missing: %q", stderr)
	}
	calls := s.CallsFor("POST", covSchedulesPath)
	if len(calls) != 3 {
		t.Fatalf("calls = %d", len(calls))
	}
	// Assert all three seeded payloads (in demoSchedules order), not just
	// calls[0] — otherwise the other two definitions could regress or
	// duplicate silently while the test still passes.
	wantPerCall := [][]string{
		{`"target_pipeline_slug":"classify-ticket"`, `"cron_expr":"*/30 * * * *"`, `"enabled":true`},
		{`"target_pipeline_slug":"daily-status-digest"`, `"cron_expr":"0 9 * * *"`, `"enabled":true`},
		{`"target_pipeline_slug":"consistency-sweep"`, `"cron_expr":"0 */6 * * *"`, `"enabled":true`},
	}
	for i, wants := range wantPerCall {
		body := string(calls[i].Body)
		for _, want := range wants {
			if !strings.Contains(body, want) {
				t.Errorf("schedule[%d] body missing %s: %s", i, want, body)
			}
		}
	}
}

func TestSeedSchedules_TargetMissing404(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost(covSchedulesPath, clitest.ErrorResponse(404, "pipeline not found"))
	client := cli.NewClient(s.URL(), "tok", covWorkspaceIDCli10)

	stderr, err := captureStderrCov(t, func() error {
		return seedSchedules(context.Background(), client)
	})
	if err != nil {
		t.Fatalf("seedSchedules: %v", err)
	}
	if !strings.Contains(stderr, "target pipeline not found — skipping") {
		t.Errorf("404 skip log missing: %q", stderr)
	}
	if !strings.Contains(stderr, "Created 0/3") {
		t.Errorf("summary should show zero created: %q", stderr)
	}
}

func TestSeedSchedules_DuplicateConflict409(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost(covSchedulesPath, clitest.ErrorResponse(409, "duplicate"))
	client := cli.NewClient(s.URL(), "tok", covWorkspaceIDCli10)

	stderr, err := captureStderrCov(t, func() error {
		return seedSchedules(context.Background(), client)
	})
	if err != nil {
		t.Fatalf("seedSchedules: %v", err)
	}
	if !strings.Contains(stderr, "already exists") {
		t.Errorf("409 idempotency log missing: %q", stderr)
	}
}

func TestSeedSchedules_TransportErrorTolerated(t *testing.T) {
	s := clitest.NewStubServer()
	s.Close() // connection refused → client.Post error branch
	client := cli.NewClient(s.URL(), "tok", covWorkspaceIDCli10)

	stderr, err := captureStderrCov(t, func() error {
		return seedSchedules(context.Background(), client)
	})
	if err != nil {
		t.Fatalf("transport failure must not abort the seed phase: %v", err)
	}
	if !strings.Contains(stderr, "! Schedule classify-ticket:") {
		t.Errorf("transport error log missing: %q", stderr)
	}
	if !strings.Contains(stderr, "Created 0/3") {
		t.Errorf("summary should report zero created: %q", stderr)
	}
}

func TestSeedSchedules_ServerError500(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost(covSchedulesPath, clitest.ErrorResponse(500, "boom"))
	client := cli.NewClient(s.URL(), "tok", covWorkspaceIDCli10)

	stderr, err := captureStderrCov(t, func() error {
		return seedSchedules(context.Background(), client)
	})
	if err != nil {
		t.Fatalf("seedSchedules must tolerate per-row failures: %v", err)
	}
	if !strings.Contains(stderr, "HTTP 500") {
		t.Errorf("500 log missing: %q", stderr)
	}
}
