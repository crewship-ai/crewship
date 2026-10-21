//go:build unix

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"golang.org/x/net/websocket"
)

// This binary boundary proves the exact wire tuple, not server admission or
// model execution. Trusted bridge and actual draft mutation tests cover those.
func TestAcceptance_DocumentTargetBinarySend(t *testing.T) {
	cap := &wsCapture{}
	srv := newRunServerCov(t, cap, nil, 200, 200)
	original := srv.Config.Handler
	received := make(chan json.RawMessage, 1)
	handler := websocket.Handler(func(conn *websocket.Conn) {
		defer conn.Close()
		for {
			var raw []byte
			if websocket.Message.Receive(conn, &raw) != nil {
				return
			}
			var frame cli.WSMessage
			if json.Unmarshal(raw, &frame) != nil {
				return
			}
			if frame.Type == "send_message" {
				received <- frame.Payload
				_ = sendScriptedEvents(conn, []cli.ChatEventPayload{{Type: "text", Content: "Fixture target received."}, {Type: "done"}})
				return
			}
		}
	})
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ws" {
			handler.ServeHTTP(w, r)
			return
		}
		original.ServeHTTP(w, r)
	})
	dir := t.TempDir()
	cfg := filepath.Join(dir, "cli.yaml")
	target := filepath.Join(dir, "target.json")
	if err := os.WriteFile(cfg, []byte(fmt.Sprintf("server: %s\ntoken: fake-token\nworkspace: %s\n", srv.URL, covWorkspaceID)), 0600); err != nil {
		t.Fatal(err)
	}
	const packet = `{"submission_id":"original-send","page_id":"same-page","schema":3,"rev":7,"generation":2}`
	if err := os.WriteFile(target, []byte(packet), 0600); err != nil {
		t.Fatal(err)
	}
	binary := buildCrewshipBinary(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "run", "viktor", "--chat", "existing-chat", "--document-target", target, "--no-stream", "Change the introduction")
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "CREWSHIP_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "CREWSHIP_CONFIG="+cfg, "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("actual binary target send: %v %s", err, output)
	}
	select {
	case raw := <-received:
		var got struct {
			Session  string                     `json:"session_id"`
			Content  string                     `json:"content"`
			Metadata map[string]json.RawMessage `json:"metadata"`
		}
		if json.Unmarshal(raw, &got) != nil || got.Session != "existing-chat" || got.Content != "Change the introduction" {
			t.Fatalf("changed original message: %s", raw)
		}
		var actual, want any
		_ = json.Unmarshal(got.Metadata["document_iteration_intent"], &actual)
		_ = json.Unmarshal([]byte(packet), &want)
		a, _ := json.Marshal(actual)
		b, _ := json.Marshal(want)
		if string(a) != string(b) {
			t.Fatalf("target changed: %s", raw)
		}
	case <-ctx.Done():
		t.Fatal("no target sent")
	}
	if cap.ChatCreates() != 0 {
		t.Fatal("targeted send created replacement chat")
	}
}
