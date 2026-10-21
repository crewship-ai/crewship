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

func TestAcceptance_ChatProposalCommands(t *testing.T) {
	calls := make(chan string, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/workspaces" {
			_, _ = w.Write([]byte(`[{"id":"cworkspacetest000000001","slug":"ws_test","name":"Synthetic"}]`))
			return
		}
		var body map[string]any
		if r.Method != "GET" {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if r.Header.Get("Idempotency-Key") != "stable" {
				t.Error("missing stable key")
			}
		}
		calls <- r.Method + " " + r.URL.Path
		_, _ = w.Write([]byte(`{"proposal":{"version":2,"status":"draft"},"refs":[]}`))
	}))
	defer srv.Close()
	file := filepath.Join(t.TempDir(), "proposal.json")
	if err := os.WriteFile(file, []byte(`{"title":"Revised","items":[{"id":"one","title":"Work","description":"","crew_id":"crew","project_id":null,"assignee_id":null,"priority":"none","selected":true}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"list", "show", "edit", "confirm", "discard"} {
		t.Run(action, func(t *testing.T) {
			args := []string{"chat", "proposal", action, "chat-1"}
			method, path := "GET", "/api/v1/chats/chat-1/proposals"
			if action != "list" {
				args = append(args, "proposal-1")
				path += "/proposal-1"
			}
			if action == "edit" || action == "confirm" || action == "discard" {
				args = append(args, "--version", "1", "--idempotency-key", "stable")
				method = "POST"
				if action == "edit" {
					method = "PATCH"
					args = append(args, "--file", file)
				} else {
					path += "/" + action
				}
			}
			out, err := runChatListCLI(t, srv.URL, args...)
			if err != nil || !strings.Contains(out, "proposal") {
				t.Fatalf("%v %s", err, out)
			}
			if got := <-calls; got != method+" "+path {
				t.Fatalf("request %s", got)
			}
		})
	}
	out, err := runChatListCLI(t, srv.URL, "chat", "proposal", "confirm", "chat-1", "proposal-1", "--version", "1")
	if err == nil || !strings.Contains(out, "idempotency-key") {
		t.Fatalf("missing key: %v %s", err, out)
	}
}
