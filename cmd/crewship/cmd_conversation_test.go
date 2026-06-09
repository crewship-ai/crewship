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

// TestConversationCmdStructure locks the command tree: `conversation search`
// must exist with the documented flags. Fast, no network.
func TestConversationCmdStructure(t *testing.T) {
	t.Parallel()
	if conversationCmd.Use != "conversation" {
		t.Errorf("conversation Use = %q, want conversation", conversationCmd.Use)
	}
	have := map[string]bool{}
	for _, sub := range conversationCmd.Commands() {
		have[sub.Name()] = true
	}
	if !have["search"] {
		t.Errorf("conversation missing 'search' subcommand; have %v", have)
	}
	if conversationSearchCmd.Flags().Lookup("limit") == nil {
		t.Error("conversation search missing --limit flag")
	}
}

// buildConversationBinaryOnce compiles the crewship binary once for the
// acceptance test so the exec drives the SAME wiring (cobra parsing, config
// resolution, client.Post, response rendering) a real operator would hit —
// not a hand-rolled HTTP request.
var (
	convBinOnce sync.Once
	convBinPath string
	convBinErr  error
)

func buildConversationBinary(t *testing.T) string {
	t.Helper()
	convBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "crewship-conv-bin")
		if err != nil {
			convBinErr = err
			return
		}
		convBinPath = filepath.Join(dir, "crewship")
		cmd := exec.Command("go", "build", "-o", convBinPath, ".")
		out, err := cmd.CombinedOutput()
		if err != nil {
			convBinErr = err
			t.Logf("build output: %s", out)
		}
	})
	if convBinErr != nil {
		t.Fatalf("build crewship binary: %v", convBinErr)
	}
	return convBinPath
}

// TestConversationSearchAcceptance drives the built crewship binary against
// a mock crewshipd. It asserts the binary POSTs to
// /api/v1/conversations/search with the resolved agent_id, query, and limit,
// and renders the returned hits. This is the API↔CLI parity contract for
// POST /api/v1/conversations/search.
func TestConversationSearchAcceptance(t *testing.T) {
	bin := buildConversationBinary(t)

	// A CUID-shaped agent id so resolveAgentID short-circuits without a
	// second GET /api/v1/agents round-trip (keeps the mock focused on the
	// search endpoint).
	const agentID = "cabcdefghijklmnopqrstuv"

	var (
		mu       sync.Mutex
		called   bool
		gotBody  map[string]any
		gotPath  string
		gotToken string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		gotPath = r.URL.Path
		gotToken = r.Header.Get("Authorization")
		if r.URL.Path == "/api/v1/conversations/search" {
			called = true
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &gotBody)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"count":1,"query":"deploy","hits":[
				{"id":"m1","session_id":"sess-42","agent_id":"` + agentID + `","role":"user","content":"please deploy the staging pipeline","ts":"2026-06-01T10:00:00Z"}
			]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	// Write a CLI config so requireAuth + requireWorkspace pass and the
	// binary targets the mock server.
	cfgDir := t.TempDir()
	cfgPath := filepath.Join(cfgDir, "cli-config.yaml")
	cfg := "server: " + srv.URL + "\nworkspace: ws_test\ntoken: fake-token\nformat: table\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cmd := exec.Command(bin, "conversation", "search", agentID, "deploy", "--limit", "13")
	cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfgPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run conversation search: %v\noutput: %s", err, out)
	}

	mu.Lock()
	defer mu.Unlock()
	if !called {
		t.Fatalf("search endpoint not called (path hit: %q); output: %s", gotPath, out)
	}
	if gotToken != "Bearer fake-token" {
		t.Errorf("auth header = %q, want Bearer fake-token", gotToken)
	}
	if gotBody["agent_id"] != agentID {
		t.Errorf("agent_id in body = %v, want %s", gotBody["agent_id"], agentID)
	}
	if gotBody["query"] != "deploy" {
		t.Errorf("query in body = %v, want deploy", gotBody["query"])
	}
	if v, ok := gotBody["limit"].(float64); !ok || int(v) != 13 {
		t.Errorf("limit in body = %v, want 13", gotBody["limit"])
	}
	if !strings.Contains(string(out), "sess-42") || !strings.Contains(string(out), "staging pipeline") {
		t.Errorf("rendered output missing hit details:\n%s", out)
	}
}

// TestConversationSearchAcceptance_JSON confirms --format json emits the raw
// envelope (so agents can pipe to jq).
func TestConversationSearchAcceptance_JSON(t *testing.T) {
	bin := buildConversationBinary(t)
	const agentID = "cabcdefghijklmnopqrstuv"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/conversations/search" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"count":0,"query":"nope","hits":[]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	if err := os.WriteFile(cfgPath, []byte("server: "+srv.URL+"\nworkspace: ws_test\ntoken: fake-token\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cmd := exec.Command(bin, "conversation", "search", agentID, "nope", "--format", "json")
	cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfgPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run: %v\noutput: %s", err, out)
	}
	var parsed struct {
		Count int `json:"count"`
		Hits  []struct {
			ID string `json:"id"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("output not JSON: %v\n%s", err, out)
	}
	if parsed.Count != 0 || len(parsed.Hits) != 0 {
		t.Errorf("unexpected JSON envelope: %+v", parsed)
	}
}
