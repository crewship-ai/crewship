package main

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const covPipelineSavePath = "/api/v1/workspaces/" + covWorkspaceIDCli10 + "/pipelines/save"

func covTestDefs() []seeddata.RoutineDef {
	return []seeddata.RoutineDef{
		{Slug: "r-one", Name: "One", CrewSlug: "backend", Definition: map[string]interface{}{"name": "r-one"}},
		{Slug: "r-two", Name: "Two", CrewSlug: "backend", Definition: map[string]interface{}{"name": "r-two"}},
		{Slug: "r-orphan", Name: "Orphan", CrewSlug: "missing-crew", Definition: map[string]interface{}{}},
	}
}

func TestSeedRoutineSlice_CreatedConflictAndSkip(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	// First save → 201 created, second → 409 conflict.
	statuses := []int{201, 409}
	i := 0
	s.OnPost(covPipelineSavePath, func(_ *http.Request, _ []byte) (int, []byte, string) {
		st := statuses[i%len(statuses)]
		i++
		return st, []byte(`{}`), "application/json"
	})
	client := cli.NewClient(s.URL(), "tok", covWorkspaceIDCli10)

	stderr, err := captureStderrCov(t, func() error {
		stats, err := seedRoutineSlice(context.Background(), client, covWorkspaceIDCli10,
			map[string]string{"backend": covCrewIDCli10}, "Routine", covTestDefs())
		if err != nil {
			return err
		}
		if stats.eligible != 2 || stats.ok != 1 || stats.conflict != 1 || stats.failed != 0 {
			t.Errorf("stats = %+v", stats)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seedRoutineSlice: %v", err)
	}
	if !strings.Contains(stderr, "+ Routine: r-one") {
		t.Errorf("created log missing: %q", stderr)
	}
	if !strings.Contains(stderr, "= Routine exists: r-two") {
		t.Errorf("conflict log missing: %q", stderr)
	}
	if !strings.Contains(stderr, `skipped (crew "missing-crew" not seeded)`) {
		t.Errorf("skip log missing: %q", stderr)
	}
	// Body contract: both OWNER escape hatches + author_crew_id present.
	// skip_governance_gate keeps risky starter routines out of the
	// maker-checker 'proposed' queue so a fresh workspace is runnable.
	calls := s.CallsFor("POST", covPipelineSavePath)
	if len(calls) != 2 {
		t.Fatalf("save calls = %d", len(calls))
	}
	if !strings.Contains(string(calls[0].Body), `"skip_test_gate":true`) ||
		!strings.Contains(string(calls[0].Body), `"skip_governance_gate":true`) ||
		!strings.Contains(string(calls[0].Body), `"author_crew_id":"`+covCrewIDCli10+`"`) {
		t.Errorf("save body wrong: %s", calls[0].Body)
	}
}

func TestSeedRoutineSlice_AllFailedErrors(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost(covPipelineSavePath, clitest.ErrorResponse(500, "save broken"))
	client := cli.NewClient(s.URL(), "tok", covWorkspaceIDCli10)

	_, err := captureStderrCov(t, func() error {
		_, err := seedRoutineSlice(context.Background(), client, covWorkspaceIDCli10,
			map[string]string{"backend": covCrewIDCli10}, "Routine", covTestDefs())
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "all 2 eligible routines failed") {
		t.Errorf("expected all-failed error, got %v", err)
	}
}

func TestSeedRoutineSlice_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := cli.NewClient("http://127.0.0.1:0", "tok", covWorkspaceIDCli10)
	_, err := seedRoutineSlice(ctx, client, covWorkspaceIDCli10,
		map[string]string{"backend": covCrewIDCli10}, "Routine", covTestDefs())
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("expected ctx cancellation, got %v", err)
	}
}

func TestSeedRoutineSlice_TransportErrorCountsFailed(t *testing.T) {
	s := clitest.NewStubServer()
	s.Close() // connection refused → client.Post error branch per row
	client := cli.NewClient(s.URL(), "tok", covWorkspaceIDCli10)

	_, err := captureStderrCov(t, func() error {
		_, err := seedRoutineSlice(context.Background(), client, covWorkspaceIDCli10,
			map[string]string{"backend": covCrewIDCli10}, "Routine", covTestDefs())
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "all 2 eligible routines failed") {
		t.Errorf("transport failures should aggregate to all-failed, got %v", err)
	}
}

func TestSeedRoutines_RequiresWorkspaceID(t *testing.T) {
	client := cli.NewClient("http://127.0.0.1:0", "tok", "")
	err := seedRoutines(context.Background(), client, map[string]string{})
	if err == nil || !strings.Contains(err.Error(), "workspace_id not set") {
		t.Errorf("expected workspace guard, got %v", err)
	}
}

func TestSeedRoutines_SeedsBothBatches(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost(covPipelineSavePath, clitest.JSONResponse(201, map[string]string{"id": "p1"}))
	client := cli.NewClient(s.URL(), "tok", covWorkspaceIDCli10)

	// crewIDs covering every slug the embedded seed data references so
	// nothing is skipped and both batches POST.
	crewIDs := map[string]string{}
	for _, r := range seeddata.Routines {
		crewIDs[r.CrewSlug] = covCrewIDCli10
	}
	for _, r := range seeddata.EvalScenarios {
		crewIDs[r.CrewSlug] = covCrewIDCli10
	}

	stderr, err := captureStderrCov(t, func() error {
		return seedRoutines(context.Background(), client, crewIDs)
	})
	if err != nil {
		t.Fatalf("seedRoutines: %v", err)
	}
	if !strings.Contains(stderr, "Creating routines...") || !strings.Contains(stderr, "Creating eval scenarios...") {
		t.Errorf("batch banners missing: %q", stderr)
	}
	wantCalls := len(seeddata.Routines) + len(seeddata.EvalScenarios)
	if got := len(s.CallsFor("POST", covPipelineSavePath)); got != wantCalls {
		t.Errorf("save calls = %d, want %d", got, wantCalls)
	}
}
