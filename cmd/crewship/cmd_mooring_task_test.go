package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

func TestMooringTaskPreservesOriginalMessageAndUsesHumanHostGuard(t *testing.T) {
	for _, mode := range []string{"accepted", "missing-original", "foreign-host"} {
		t.Run(mode, func(t *testing.T) {
			hits := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits++
				if r.Method != "POST" || r.URL.Path != "/api/v1/pages/@original-page/mooring/tasks" || r.URL.Query().Get("workspace_id") != "cmooringworkspace000000001" || r.Header.Get("Authorization") != "Bearer synthetic-human" {
					t.Error("original host/credential/page changed")
				}
				var body map[string]string
				if json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 2 || body["chat_id"] != "original-chat" || body["message_id"] != "original-message" {
					t.Error("CLI changed original action or supplied instruction/authority")
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(`{"work_id":"host-work","status":"queued"}`))
			}))
			defer server.Close()
			client := cli.NewClient(server.URL, "synthetic-human", "cmooringworkspace000000001")
			if mode == "foreign-host" {
				client.TokenHost = "another.example.invalid"
			}
			cmd := newMooringTaskCommand()
			cmd.SetContext(t.Context())
			_ = cmd.Flags().Set("chat", "original-chat")
			if mode != "missing-original" {
				_ = cmd.Flags().Set("message", "original-message")
			}
			var output bytes.Buffer
			cmd.SetOut(&output)
			err := runMooringTask(cmd, "@original-page", client)
			if mode == "accepted" {
				if err != nil || hits != 1 || !bytes.Contains(output.Bytes(), []byte("queued")) || bytes.Contains(output.Bytes(), []byte("Saved")) {
					t.Fatal("acceptance or output changed", err)
				}
			} else if err == nil || hits != 0 {
				t.Fatal("invalid original/host reached API", err)
			}
		})
	}
}
