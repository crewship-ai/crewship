package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAcceptance_ChatProposalAgentCommands(t *testing.T) {
	t.Setenv("CREWSHIP_AGENT_TOKEN", "verified-test-token")
	calls := make(chan string, 2)
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer verified-test-token" {
			t.Error("agent token omitted")
		}
		if r.Header.Get("Idempotency-Key") != "stable-agent" {
			t.Error("agent key omitted")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		for _, key := range []string{"run_id", "chat_id", "agent_id", "workspace_id"} {
			if body[key] != nil {
				t.Errorf("model identity forwarded: %s", key)
			}
		}
		calls <- r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"proposal":{"status":"draft"}}`))
	}))
	defer host.Close()
	file := filepath.Join(t.TempDir(), "agent-proposal.json")
	if err := os.WriteFile(file, []byte(`{"title":"Plan","items":[],"agent_id":"forged","run_id":"forged"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"propose", "revise"} {
		args := []string{"chat", "proposal", action}
		path := "/chat/proposal/create"
		if action == "revise" {
			args = append(args, "proposal-1", "--version", "1")
			path = "/chat/proposal/proposal-1/edit"
		}
		args = append(args, "--sidecar", host.URL, "--file", file, "--idempotency-key", "stable-agent")
		out, err := runChatListCLI(t, host.URL, args...)
		if err != nil || !strings.Contains(out, "draft") {
			t.Fatalf("agent %s: %v %s", action, err, out)
		}
		if got := <-calls; got != path {
			t.Fatalf("agent path %s", got)
		}
	}
	out, err := runChatListCLI(t, host.URL, "chat", "proposal", "propose", "--sidecar", "https://example.invalid", "--file", file, "--idempotency-key", "stable-agent")
	if err == nil || !strings.Contains(out, "loopback") {
		t.Fatalf("remote credential forwarding: %v %s", err, out)
	}
}
