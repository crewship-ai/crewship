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
	if !strings.Contains(stderr, "+ Schedule: Demo: extract emails") {
		t.Errorf("created log missing: %q", stderr)
	}
	if !strings.Contains(stderr, "Created 1/1 demo schedule(s)") {
		t.Errorf("summary line missing: %q", stderr)
	}
	calls := s.CallsFor("POST", covSchedulesPath)
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	body := string(calls[0].Body)
	for _, want := range []string{`"target_pipeline_slug":"eval-extract-emails"`, `"cron_expr":"*/10 * * * *"`, `"enabled":true`} {
		if !strings.Contains(body, want) {
			t.Errorf("schedule body missing %s: %s", want, body)
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
	if !strings.Contains(stderr, "Created 0/1") {
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
	if !strings.Contains(stderr, "! Schedule eval-extract-emails:") {
		t.Errorf("transport error log missing: %q", stderr)
	}
	if !strings.Contains(stderr, "Created 0/1") {
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
