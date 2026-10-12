package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestSafePathSegment(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		wantErr bool
	}{
		{"crew_abc123", false},
		{"viktor", false},
		{"with-dash.and.dot", false},
		{"", true},
		{".", true},
		{"..", true},
		{"a/b", true},
		{`a\b`, true},
		{"nul\x00byte", true},
	}
	for _, tt := range tests {
		got, err := safePathSegment(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("safePathSegment(%q) should fail", tt.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("safePathSegment(%q): %v", tt.in, err)
		}
		if got != tt.in {
			t.Errorf("safePathSegment(%q) = %q, want identity", tt.in, got)
		}
	}
}

func TestDemoMarkdownContents(t *testing.T) {
	t.Parallel()
	if got := demoAgentMD("viktor", "backend"); !strings.Contains(got, "AGENT.md — viktor") || !strings.Contains(got, "My crew: backend") {
		t.Errorf("demoAgentMD missing slug/crew:\n%s", got)
	}
	if got := demoCrewMD("backend"); !strings.Contains(got, "CREW.md — backend") || !strings.Contains(got, "Crew slug: backend") {
		t.Errorf("demoCrewMD missing crew slug:\n%s", got)
	}
	if got := demoPersonaMD("eva"); !strings.Contains(got, "PERSONA.md — eva voice") || !strings.Contains(got, "⛵ eva") {
		t.Errorf("demoPersonaMD missing slug:\n%s", got)
	}
	if got := demoPinsMD(); !strings.Contains(got, "PINNED-1") || !strings.Contains(got, "PINNED-4") {
		t.Errorf("demoPinsMD missing pins:\n%s", got)
	}
	if got := demoDailyMD("2026-05-12", "viktor"); !strings.Contains(got, "daily/2026-05-12.md — viktor") {
		t.Errorf("demoDailyMD missing header:\n%s", got)
	}
	if got := demoLearnedMD(); !strings.Contains(got, "LESSON-001") || !strings.Contains(got, "LESSON-003") {
		t.Errorf("demoLearnedMD missing lessons:\n%s", got)
	}
}

func TestSeedAgentMemoryRemoteLeavesClientFilesUntouched(t *testing.T) {
	home, storage, root := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CREWSHIP_STORAGE_BASE_PATH", storage)
	t.Setenv("CREWSHIP_HOME", root)
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{{"slug": "viktor", "crew_id": "crew1"}, {"slug": "stranger", "crew_id": "other"}}))
	scopes := map[string]map[string]string{}
	stub.OnPost("/api/v1/memory/initialize", func(r *http.Request, b []byte) (int, []byte, string) {
		var payload struct {
			CrewID    string               `json:"crew_id"`
			AgentSlug string               `json:"agent_slug"`
			Documents []seedMemoryDocument `json:"documents"`
		}
		if err := json.Unmarshal(b, &payload); err != nil {
			t.Error(err)
			return 400, nil, "application/json"
		}
		if payload.CrewID != "crew1" {
			t.Errorf("foreign crew payload: %#v", payload)
		}
		docs := map[string]string{}
		for _, d := range payload.Documents {
			docs[d.Path] = d.Body
		}
		scopes[payload.AgentSlug] = docs
		result, _ := json.Marshal(map[string]int{"written": len(docs), "existing": 0})
		return 200, result, "application/json"
	})
	client := cli.NewClient(stub.URL(), "tok", "ws")
	if err := seedAgentMemory(context.Background(), client, map[string]string{"backend": "crew1"}); err != nil {
		t.Fatal(err)
	}
	if len(scopes[""]) != 2 || len(scopes["viktor"]) != 4 || !strings.Contains(scopes["viktor"]["AGENT.md"], "My crew: backend") {
		t.Fatalf("missing full server memory tiers: %#v", scopes)
	}
	if _, ok := scopes[""]["learned.md"]; !ok {
		t.Fatal("missing learned initialization")
	}
	for _, dir := range []string{home, storage, root} {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Fatalf("client wrote server files in %s: %v %v", dir, entries, err)
		}
	}
}

func TestSeedAgentMemoryRemoteErrors(t *testing.T) {
	for _, scenario := range []string{"cancelled", "invalid-crew", "invalid-agent", "server-refused", "incomplete-ack", "list-failed"} {
		t.Run(scenario, func(t *testing.T) {
			stub := clitest.NewStubServer()
			defer stub.Close()
			slug := "alex"
			if scenario == "invalid-agent" {
				slug = "../../outside"
			}
			stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{{"slug": slug, "crew_id": "crew1"}}))
			stub.OnPost("/api/v1/memory/initialize", clitest.ErrorResponse(409, "memory refused"))
			if scenario == "incomplete-ack" {
				stub.OnPost("/api/v1/memory/initialize", clitest.JSONResponse(200, map[string]int{"written": 0, "existing": 0}))
			}
			if scenario == "list-failed" {
				stub.OnGet("/api/v1/agents", clitest.ErrorResponse(500, "list failed"))
			}
			crews := map[string]string{"backend": "crew1"}
			if scenario == "invalid-crew" {
				crews["backend"] = "../outside"
			}
			ctx := context.Background()
			if scenario == "cancelled" {
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
			}
			err := seedAgentMemory(ctx, cli.NewClient(stub.URL(), "tok", "ws"), crews)
			if err == nil {
				t.Fatalf("%s should fail", scenario)
			}
			if scenario == "invalid-crew" || scenario == "invalid-agent" || scenario == "cancelled" {
				if n := len(stub.CallsFor("POST", "/api/v1/memory/initialize")); n != 0 {
					t.Fatalf("invalid preflight made %d writes", n)
				}
			}
		})
	}
}

func TestSeedMemoryDoesNotDependOnClientHome(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("CREWSHIP_STORAGE_BASE_PATH", filepath.Join(t.TempDir(), "not-created"))
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []any{}))
	if err := seedAgentMemory(context.Background(), cli.NewClient(stub.URL(), "tok", "ws"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(os.Getenv("CREWSHIP_STORAGE_BASE_PATH")); !os.IsNotExist(err) {
		t.Fatalf("created local storage: %v", err)
	}
}
