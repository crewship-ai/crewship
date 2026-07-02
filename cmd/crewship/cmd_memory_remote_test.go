package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// Server-side memory surface: hybrid search + the versions audit chain
// over the API (the local `memory search`/`log`/`show`/`restore` commands
// read the filesystem/DB directly and only work on the server host).

func TestMemoryHybrid_SendsQueryAndParsesHits(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/memory/search/hybrid", clitest.JSONResponse(200, map[string]any{
		"query": "deploy runbook", "count": 1,
		"hits": []map[string]any{{"source": "fts", "score": 0.92, "snippet": "restart via dev.sh"}},
	}))
	covSetupCli10(t, s.URL())
	setFlagCovCli10(t, memoryHybridCmd, "limit", "5")
	memoryHybridCmd.SetContext(context.Background())

	out, err := captureStdoutCovCli10(t, func() error {
		return memoryHybridCmd.RunE(memoryHybridCmd, []string{"deploy runbook"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "restart via dev.sh") {
		t.Errorf("output missing hit snippet: %q", out)
	}

	calls := s.CallsFor("POST", "/api/v1/memory/search/hybrid")
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	var body struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body.Query != "deploy runbook" || body.Limit != 5 {
		t.Errorf("body = %+v", body)
	}
}

func TestMemoryVersionsList_JSONHasFullEntries(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/memory/versions", clitest.JSONResponse(200, map[string]any{
		"path": "crew:c1/learned-x.md", "count": 1,
		"entries": []map[string]any{{"sha256": "abc123", "created_at": "2026-07-03T00:00:00Z", "bytes": 42}},
	}))
	covSetupCli10(t, s.URL())
	flagFormat = "json"
	memoryVersionsListCmd.SetContext(context.Background())

	out, err := captureStdoutCovCli10(t, func() error {
		return memoryVersionsListCmd.RunE(memoryVersionsListCmd, []string{"crew:c1/learned-x.md"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	var decoded map[string]any
	if jerr := json.Unmarshal([]byte(out), &decoded); jerr != nil {
		t.Fatalf("json does not parse: %v\n%s", jerr, out)
	}
	if got := s.CallsFor("GET", "/api/v1/memory/versions"); len(got) != 1 || !strings.Contains(got[0].Query, "path=") {
		t.Errorf("list call = %+v", got)
	}
}

func TestMemoryVersionsShow_StreamsRawBlob(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/memory/versions/abc123", clitest.TextResponse(200, "raw historical content"))
	covSetupCli10(t, s.URL())
	memoryVersionsShowCmd.SetContext(context.Background())

	out, err := captureStdoutCovCli10(t, func() error {
		return memoryVersionsShowCmd.RunE(memoryVersionsShowCmd, []string{"crew:c1/learned-x.md", "abc123"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if out != "raw historical content" {
		t.Errorf("stdout = %q, want raw blob bytes only", out)
	}
}

func TestMemoryVersionsRestore_SendsBodyAndTier(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/memory/versions/abc123/restore", clitest.JSONResponse(200, map[string]any{
		"restored_sha": "abc123", "path": "crew:c1/learned-x.md",
	}))
	covSetupCli10(t, s.URL())
	setFlagCovCli10(t, memoryVersionsRestoreCmd, "tier", "learned")
	setFlagCovCli10(t, memoryVersionsRestoreCmd, "yes", "true")
	memoryVersionsRestoreCmd.SetContext(context.Background())

	_, err := captureStdoutCovCli10(t, func() error {
		return memoryVersionsRestoreCmd.RunE(memoryVersionsRestoreCmd,
			[]string{"crew:c1/learned-x.md", "abc123", "/data/memory/crew/c1/learned-x.md"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := s.CallsFor("POST", "/api/v1/memory/versions/abc123/restore")
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	var body struct {
		Path          string `json:"path"`
		CanonicalPath string `json:"canonical_path"`
		Tier          string `json:"tier"`
	}
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body.Tier != "learned" || body.Path == "" || body.CanonicalPath == "" {
		t.Errorf("body = %+v", body)
	}
}
