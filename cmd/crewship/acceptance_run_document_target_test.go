package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/crewship-ai/crewship/internal/documents"
	wsproto "github.com/crewship-ai/crewship/internal/ws"
)

// This exercises the built command and its real HTTP/WS transport, not Cobra's
// in-process command state. The model/host authority is deliberately a fixture.
func TestAcceptance_RunDocumentTargetBinary(t *testing.T) {
	const body = `{"submission_id":"stable-submission","page_id":"original-page","schema":3,"rev":7,"generation":2}`
	const instruction = "Revise only the introduction."
	intent, err := documents.DecodeIterationIntent([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, input                          string
		args                                 []string
		noChat, restricted, denied, ordinary bool
	}{
		{name: "stream", input: body},
		{name: "wait", input: body, args: []string{"--wait"}},
		{name: "no stream", input: body, args: []string{"--no-stream"}},
		{name: "ordinary compatibility", ordinary: true},
		{name: "missing chat", input: body, noChat: true, denied: true},
		{name: "interactive", input: body, args: []string{"--interactive"}, denied: true},
		{name: "project version", input: body, args: []string{"--project-file-version", "immutable-input"}, denied: true},
		{name: "restricted", input: body, restricted: true, denied: true},
		{name: "duplicate", input: strings.Replace(body, `"rev":7`, `"rev":7,"rev":8`, 1), denied: true},
		{name: "unknown mode", input: strings.Replace(body, `"rev":7`, `"rev":7,"mode":"DIRECT"`, 1), denied: true},
		{name: "trailing object", input: body + `{}`, denied: true},
		{name: "invalid schema", input: strings.Replace(body, `"schema":3`, `"schema":2`, 1), denied: true},
		{name: "missing generation", input: strings.Replace(body, `,"generation":2`, ``, 1), denied: true},
		{name: "null revision", input: strings.Replace(body, `"rev":7`, `"rev":null`, 1), denied: true},
		{name: "oversize", input: body + strings.Repeat(" ", 1025), denied: true},
		{name: "missing file", denied: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sends, writes atomic.Int32
			captured := make(chan map[string]json.RawMessage, 1)
			mux := http.NewServeMux()
			mux.HandleFunc("/api/v1/agents", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, `[{"id":%q,"slug":"ada"}]`, covAgentID) })
			mux.HandleFunc("/api/v1/chats/original-chat/execution-profile", func(w http.ResponseWriter, r *http.Request) {
				mode := "trusted"
				if tc.restricted {
					mode = "restricted"
				}
				fmt.Fprintf(w, `{"mode":%q}`, mode)
			})
			mux.HandleFunc("/api/v1/ws-token", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"token":"synthetic-ws-ticket"}`) })
			mux.Handle("/ws", websocket.Handler(func(conn *websocket.Conn) {
				defer conn.Close()
				for {
					var frame struct {
						Type, Channel string
						Payload       json.RawMessage
					}
					if err := websocket.JSON.Receive(conn, &frame); err != nil {
						return
					}
					if frame.Type != "send_message" {
						continue
					}
					sends.Add(1)
					var payload map[string]json.RawMessage
					if err := json.Unmarshal(frame.Payload, &payload); err != nil {
						t.Error(err)
						return
					}
					if frame.Channel != "agent:"+covAgentID {
						t.Errorf("wrong channel %q", frame.Channel)
					}
					captured <- payload
					for _, event := range []wsproto.ChatEvent{{Type: "text", Content: "fixture answer"}, {Type: "done"}} {
						if err := websocket.JSON.Send(conn, wsproto.ServerMessage{Type: "chat_event", Channel: "session:original-chat", Payload: event}); err != nil {
							t.Error(err)
							return
						}
					}
					return
				}
			}))
			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					writes.Add(1)
				}
				http.NotFound(w, r)
			})
			server := httptest.NewServer(mux)
			defer server.Close()
			dir := t.TempDir()
			target := filepath.Join(dir, "intent.json")
			if tc.input != "" {
				if err := os.WriteFile(target, []byte(tc.input), 0600); err != nil {
					t.Fatal(err)
				}
			}
			cfg := filepath.Join(dir, "cli.yaml")
			if err := os.WriteFile(cfg, []byte(fmt.Sprintf("server: %s\nworkspace: ws_fixture\ntoken: synthetic-cli-token\n", server.URL)), 0600); err != nil {
				t.Fatal(err)
			}
			args := []string{"run", "ada", "--quiet", "--no-markdown", "--max-turns", "3"}
			if !tc.noChat {
				args = append(args, "--chat", "original-chat")
			}
			if !tc.ordinary {
				args = append(args, "--document-target", target)
			}
			args = append(args, tc.args...)
			args = append(args, instruction)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, buildCrewshipBinary(t), args...)
			command.Dir = dir
			for _, v := range os.Environ() {
				if !strings.HasPrefix(v, "CREWSHIP_") {
					command.Env = append(command.Env, v)
				}
			}
			command.Env = append(command.Env, "CREWSHIP_CONFIG="+cfg, "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1")
			output, err := command.CombinedOutput()
			if tc.denied {
				if err == nil {
					t.Fatalf("invalid target accepted: %s", output)
				}
				if sends.Load() != 0 || writes.Load() != 0 {
					t.Fatalf("invalid input caused sends=%d writes=%d", sends.Load(), writes.Load())
				}
				if strings.Contains(string(output), "unknown flag") {
					t.Fatalf("target flag unavailable rather than validated: %s", output)
				}
				return
			}
			if err != nil {
				t.Fatalf("actual CLI failed: %v %s", err, output)
			}
			if sends.Load() != 1 || writes.Load() != 0 || !strings.Contains(string(output), "fixture answer") {
				t.Fatalf("delivery/output mismatch: sends=%d writes=%d output=%s", sends.Load(), writes.Load(), output)
			}
			payload := <-captured
			if string(payload["content"]) != fmt.Sprintf("%q", instruction) || string(payload["session_id"]) != `"original-chat"` || string(payload["max_turns"]) != "3" {
				t.Fatalf("original send changed: %s", payload)
			}
			if tc.ordinary {
				if _, ok := payload["metadata"]; ok {
					t.Fatal("ordinary send gained metadata")
				}
				return
			}
			var metadata map[string]json.RawMessage
			if err := json.Unmarshal(payload["metadata"], &metadata); err != nil || len(metadata) != 1 {
				t.Fatalf("wrong metadata: %s %v", payload["metadata"], err)
			}
			got, err := documents.DecodeIterationIntent(metadata[documents.IterationMetadataKey])
			if err != nil || !reflect.DeepEqual(got, intent) {
				t.Fatalf("lost original typed target: %+v %v", got, err)
			}
		})
	}
}
