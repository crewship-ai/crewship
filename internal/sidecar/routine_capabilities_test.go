package sidecar

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCrewCapabilities_ProxiesToOwnCrew(t *testing.T) {
	var gotPath string
	var gotToken string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotToken = r.Header.Get("X-Internal-Token")
		if r.URL.Query().Get("workspace_id") != "ws-1" {
			t.Errorf("workspace_id query = %q, want ws-1", r.URL.Query().Get("workspace_id"))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"crew_slug":"acct","agents":[{"slug":"parse"}]}`))
	}))
	defer mock.Close()

	s := newPipelineTestServer(t, &IPCConfig{BaseURL: mock.URL, Token: "sekret", WorkspaceID: "ws-1", CrewID: "crew-9", AgentID: "a"})
	status, body := s.crewCapabilities(context.Background())

	if status != http.StatusOK {
		t.Fatalf("status = %d, body=%s", status, body)
	}
	// Crew comes from IPC, not the caller — the agent can only see its own crew.
	if gotPath != "/api/v1/crews/crew-9/capabilities" {
		t.Errorf("path = %q, want /api/v1/crews/crew-9/capabilities", gotPath)
	}
	if gotToken != "sekret" {
		t.Errorf("internal token not forwarded: %q", gotToken)
	}
	if !strings.Contains(string(body), `"crew_slug":"acct"`) {
		t.Errorf("body not passed through: %s", body)
	}
}

func TestCrewCapabilities_NoIPC(t *testing.T) {
	s := newPipelineTestServer(t, nil)
	status, _ := s.crewCapabilities(context.Background())
	if status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 without IPC", status)
	}
}

func TestRoutineMCPCatalog_IncludesDiscoverCapabilities(t *testing.T) {
	var found bool
	for _, tool := range routineMCPTools {
		if tool.Name == "discover_capabilities" {
			found = true
			if len(tool.InputSchema) == 0 {
				t.Error("discover_capabilities has no input schema")
			}
		}
	}
	if !found {
		t.Error("discover_capabilities missing from the routine MCP tool catalog")
	}
}
