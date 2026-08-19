package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestConversationSearchCmd_HasAgentFlag locks the new surface: the agent is
// now an OPTIONAL flag, because the endpoint's default scope is the whole
// workspace.
func TestConversationSearchCmd_HasAgentFlag(t *testing.T) {
	t.Parallel()
	if conversationSearchCmd.Flags().Lookup("agent") == nil {
		t.Error("conversation search missing --agent flag")
	}
}

// convSearchStubServer answers the two endpoints the command touches and
// records the search body.
type convSearchStubServer struct {
	mu      sync.Mutex
	called  bool
	body    map[string]any
	hasKey  bool
	agentID string
}

func (s *convSearchStubServer) start(t *testing.T, agentSlug, agentID string) *httptest.Server {
	t.Helper()
	s.agentID = agentID
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/agents":
			_, _ = w.Write([]byte(`[{"id":"` + agentID + `","slug":"` + agentSlug + `"}]`))
		case r.URL.Path == "/api/v1/agents/"+agentID:
			_, _ = w.Write([]byte(`{"id":"` + agentID + `"}`))
		case r.URL.Path == "/api/v1/conversations/search":
			s.mu.Lock()
			s.called = true
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &s.body)
			_, s.hasKey = s.body["agent_id"]
			s.mu.Unlock()
			_, _ = w.Write([]byte(`{"count":2,"query":"deploy","scope":"workspace","hits":[
				{"id":"m1","session_id":"sess-42","agent_id":"` + agentID + `","agent_slug":"backend-bot","agent_name":"Backend Bot","role":"user","content":"please deploy the staging pipeline","ts":"2026-06-01T10:00:00Z"},
				{"id":"m2","session_id":"sess-77","agent_id":"agent-other","agent_slug":"scout","agent_name":"Scout","role":"assistant","content":"the deploy is green","ts":"2026-06-02T10:00:00Z"}
			]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func convSearchConfig(t *testing.T, serverURL string) string {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	cfg := "server: " + serverURL + "\nworkspace: ws_test\ntoken: fake-token\nformat: table\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath
}

func runConvSearch(t *testing.T, cfgPath string, args ...string) string {
	t.Helper()
	bin := buildConversationBinary(t)
	cmd := exec.Command(bin, append([]string{"conversation", "search"}, args...)...)
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run conversation search %v: %v\noutput: %s", args, err, out)
	}
	return string(out)
}

// TestConversationSearchAcceptance_WorkspaceScope drives the BUILT binary:
// with no agent named, the command must POST a body with NO agent_id — the
// server then scopes to the caller's workspace — and must show which agent
// each hit came from, since they no longer all come from one.
func TestConversationSearchAcceptance_WorkspaceScope(t *testing.T) {
	stub := &convSearchStubServer{}
	srv := stub.start(t, "backend-bot", "cabcdefghijklmnopqrstuv")
	out := runConvSearch(t, convSearchConfig(t, srv.URL), "deploy", "--limit", "5")

	stub.mu.Lock()
	defer stub.mu.Unlock()
	if !stub.called {
		t.Fatalf("search endpoint not called; output: %s", out)
	}
	if stub.hasKey && stub.body["agent_id"] != "" {
		t.Errorf("agent_id = %v in a workspace-wide search; want it absent", stub.body["agent_id"])
	}
	if stub.body["query"] != "deploy" {
		t.Errorf("query = %v, want deploy", stub.body["query"])
	}
	if v, ok := stub.body["limit"].(float64); !ok || int(v) != 5 {
		t.Errorf("limit = %v, want 5", stub.body["limit"])
	}
	for _, want := range []string{"Backend Bot", "Scout", "sess-42", "staging pipeline"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// TestConversationSearchAcceptance_AgentFlag: --agent narrows back to one
// agent, resolved from its slug.
func TestConversationSearchAcceptance_AgentFlag(t *testing.T) {
	const agentID = "cabcdefghijklmnopqrstuv"
	stub := &convSearchStubServer{}
	srv := stub.start(t, "backend-bot", agentID)
	out := runConvSearch(t, convSearchConfig(t, srv.URL), "deploy", "--agent", "backend-bot")

	stub.mu.Lock()
	defer stub.mu.Unlock()
	if !stub.called {
		t.Fatalf("search endpoint not called; output: %s", out)
	}
	if stub.body["agent_id"] != agentID {
		t.Errorf("agent_id = %v, want %s", stub.body["agent_id"], agentID)
	}
	if stub.body["query"] != "deploy" {
		t.Errorf("query = %v, want deploy", stub.body["query"])
	}
}

// TestConversationSearchAcceptance_MultiWordWorkspaceQuery: without an
// agent, every positional argument is part of the query — none of them is
// mistaken for an agent slug.
func TestConversationSearchAcceptance_MultiWordWorkspaceQuery(t *testing.T) {
	stub := &convSearchStubServer{}
	srv := stub.start(t, "backend-bot", "cabcdefghijklmnopqrstuv")
	runConvSearch(t, convSearchConfig(t, srv.URL), "--agent", "backend-bot", "deploy", "the", "pipeline")

	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.body["query"] != "deploy the pipeline" {
		t.Errorf("query = %v, want %q", stub.body["query"], "deploy the pipeline")
	}
}

// TestConversationSearchAcceptance_LegacyPositionalMiss: two bare words still
// read the first as an agent (the form this command shipped with). When that
// agent does not exist, the error has to say so — "agent not found: deploy"
// alone would leave the caller with no idea why their phrase was rejected.
func TestConversationSearchAcceptance_LegacyPositionalMiss(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/agents" {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	bin := buildConversationBinary(t)
	cmd := exec.Command(bin, "conversation", "search", "deploy", "pipeline")
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+convSearchConfig(t, srv.URL),
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected a failure resolving the agent; output: %s", out)
	}
	if !strings.Contains(string(out), "quote the phrase") {
		t.Errorf("error does not explain the positional form:\n%s", out)
	}
}
