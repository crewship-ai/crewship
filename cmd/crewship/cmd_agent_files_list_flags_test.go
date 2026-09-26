package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// `agent files --recursive / --subdir` — GET /agents/{id}/files grew both
// parameters (proxied to the crewshipd listing), and these tests pin the CLI
// half: each flag reaches the server's query string, composed and escaped,
// and neither is sent when not asked. Same hazard as every server-side
// filter: a flag the binary parses and then drops looks identical in a unit
// test and is broken against every real server.

func TestAgentFilesCmd_RecursiveFlagSent(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, agentFilesCmd)
	stub.OnGet("/api/v1/agents/"+covAgentIDCli3+"/files",
		clitest.JSONResponse(200, []map[string]any{{"name": "nested/out.txt"}}))

	if err := agentFilesCmd.Flags().Set("recursive", "true"); err != nil {
		t.Fatal(err)
	}
	out := covCaptureStdoutCli3(t, func() {
		if err := agentFilesCmd.RunE(agentFilesCmd, []string{covAgentIDCli3}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "nested/out.txt") {
		t.Errorf("table output missing row: %q", out)
	}
	calls := stub.CallsFor("GET", "/api/v1/agents/"+covAgentIDCli3+"/files")
	if len(calls) != 1 {
		t.Fatalf("expected 1 list GET, got %d", len(calls))
	}
	// The literal "true", not "1": the proxy compares the query value
	// against exactly "true", so any other rendering silently lists one
	// level while looking like it walked the tree.
	if !strings.Contains(calls[0].Query, "recursive=true") {
		t.Errorf("query = %q, want recursive=true", calls[0].Query)
	}
}

func TestAgentFilesCmd_SubdirFlagSent(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, agentFilesCmd)
	stub.OnGet("/api/v1/agents/"+covAgentIDCli3+"/files",
		clitest.JSONResponse(200, []map[string]any{{"name": "demo/config.toml"}}))

	if err := agentFilesCmd.Flags().Set("subdir", "workspace/demo"); err != nil {
		t.Fatal(err)
	}
	out := covCaptureStdoutCli3(t, func() {
		if err := agentFilesCmd.RunE(agentFilesCmd, []string{covAgentIDCli3}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "demo/config.toml") {
		t.Errorf("table output missing row: %q", out)
	}
	calls := stub.CallsFor("GET", "/api/v1/agents/"+covAgentIDCli3+"/files")
	if len(calls) != 1 {
		t.Fatalf("expected 1 list GET, got %d", len(calls))
	}
	// Escaped, not raw: the slash must go through url.Values so a subdir
	// with a space or ampersand cannot splice a second parameter in.
	if !strings.Contains(calls[0].Query, "subdir=workspace%2Fdemo") {
		t.Errorf("query = %q, want the escaped subdir", calls[0].Query)
	}
}

func TestAgentFilesCmd_ListFiltersCompose(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, agentFilesCmd)
	stub.OnGet("/api/v1/agents/"+covAgentIDCli3+"/files",
		clitest.JSONResponse(200, []map[string]any{{"name": "deep/report.md"}}))

	if err := agentFilesCmd.Flags().Set("recursive", "true"); err != nil {
		t.Fatal(err)
	}
	if err := agentFilesCmd.Flags().Set("subdir", "reports/2026"); err != nil {
		t.Fatal(err)
	}
	if err := agentFilesCmd.RunE(agentFilesCmd, []string{covAgentIDCli3}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := stub.CallsFor("GET", "/api/v1/agents/"+covAgentIDCli3+"/files")
	if len(calls) != 1 {
		t.Fatalf("expected 1 list GET, got %d", len(calls))
	}
	q := calls[0].Query
	if !strings.Contains(q, "recursive=true") || !strings.Contains(q, "subdir=reports%2F2026") {
		t.Errorf("query = %q, want both recursive and subdir", q)
	}
}

func TestAgentFilesCmd_NoListFiltersWhenNotAsked(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, agentFilesCmd)
	stub.OnGet("/api/v1/agents/"+covAgentIDCli3+"/files",
		clitest.JSONResponse(200, []map[string]any{{"name": "out.txt"}}))

	if err := agentFilesCmd.RunE(agentFilesCmd, []string{covAgentIDCli3}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := stub.CallsFor("GET", "/api/v1/agents/"+covAgentIDCli3+"/files")
	if len(calls) != 1 {
		t.Fatalf("expected 1 list GET, got %d", len(calls))
	}
	// workspace_id is the client's own injection; the listing filters must
	// not appear beside it unless asked for.
	q := calls[0].Query
	if strings.Contains(q, "recursive") || strings.Contains(q, "subdir") {
		t.Errorf("query = %q, want no listing filters at all", q)
	}
}
