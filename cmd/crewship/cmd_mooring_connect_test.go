package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/gorilla/websocket"
)

func TestMooringConnectOriginalFramesAndCredentialGuard(t *testing.T) {
	original := `{ "type":"session_join", "request_id":"original-join", "ticket":"synthetic-one-use" }`
	for _, mode := range []string{"valid", "oversized-server", "oversized-input", "host-mismatch", "missing-file"} {
		t.Run(mode, func(t *testing.T) {
			var hits atomic.Int32
			completed := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				defer func() { completed <- struct{}{} }()
				if r.Method != http.MethodGet || r.URL.EscapedPath() != "/api/v1/pages/page%2Fraw/mooring/ws" || r.URL.Query().Get("workspace_id") != "cmooringworkspace000000001" || r.URL.Query().Get("connection_ref") != "original-connection" || r.Header.Get("Authorization") != "Bearer synthetic-human" || r.Header.Get("Origin") != "http://"+r.Host {
					t.Error("wrong credential, origin, workspace or escaped endpoint")
				}
				conn, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.Close()
				_, frame, err := conn.ReadMessage()
				if mode == "oversized-input" {
					if err == nil {
						t.Error("oversized input was sent")
					}
					return
				}
				if err != nil || string(frame) != original {
					t.Errorf("original packet changed: %q / %v", frame, err)
					return
				}
				output := []byte(`{ "type":"session_joined", "request_id":"original-join", "counter":"9007199254740991" }`)
				if mode == "oversized-server" {
					output = []byte(`{"data":"` + strings.Repeat("x", mooringCLIFrameLimit) + `"}`)
				}
				_ = conn.WriteMessage(websocket.TextMessage, output)
				_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
			}))
			defer server.Close()
			client := cli.NewClient(server.URL, "synthetic-human", "cmooringworkspace000000001")
			if mode == "host-mismatch" {
				client.TokenHost = "other.example.test"
			}
			cmd := newMooringConnectCommand()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			cmd.SetContext(ctx)
			_ = cmd.Flags().Set("connection", "original-connection")
			_ = cmd.Flags().Set("file", "-")
			if mode == "missing-file" {
				_ = cmd.Flags().Set("file", filepath.Join(t.TempDir(), "missing"))
			}
			input := original + "\n"
			if mode == "oversized-input" {
				input = `{"x":"` + strings.Repeat("x", mooringCLIFrameLimit) + `"}` + "\n"
			}
			cmd.SetIn(strings.NewReader(input))
			var output bytes.Buffer
			cmd.SetOut(&output)
			err := runMooringConnect(cmd, "page/raw", client)
			if mode == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				var result map[string]string
				if json.Unmarshal(output.Bytes(), &result) != nil || result["counter"] != "9007199254740991" || result["request_id"] != "original-join" {
					t.Fatalf("server JSON changed: %s", output.String())
				}
			} else if err == nil {
				t.Fatal("invalid stream accepted")
			}
			if mode == "host-mismatch" || mode == "missing-file" {
				if hits.Load() != 0 {
					t.Fatal("credential/filesystem rejection happened after network request")
				}
			} else {
				select {
				case <-completed:
				case <-ctx.Done():
					t.Fatal("server did not finish")
				}
			}
			if mode != "valid" && output.Len() != 0 {
				t.Fatal("invalid frame reached output")
			}
		})
	}
}

func TestMooringSessionBinaryPreservesOriginalPacket(t *testing.T) {
	packet := "{ \"write\":false, \"checkpoint_base64\":\"AA==\" }"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/pages/@original-page/mooring/session" || r.Header.Get("Authorization") != "Bearer synthetic-human" || r.URL.Query().Get("workspace_id") != "cmooringworkspace000000001" {
			t.Error("incorrect human session request")
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil || string(raw) != packet {
			t.Errorf("session packet changed: %q / %v", raw, err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"session_id":"actual-server-response","origin":"17"}`)
	}))
	defer server.Close()
	cfg := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(cfg, []byte(fmt.Sprintf("server: %s\ntoken: synthetic-human\nworkspace: cmooringworkspace000000001\nformat: json\n", server.URL)), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), buildCrewshipBinary(t), "mooring", "session", "@original-page", "--file", "-")
	cmd.Stdin = strings.NewReader(packet)
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "CREWSHIP_") {
			cmd.Env = append(cmd.Env, v)
		}
	}
	cmd.Env = append(cmd.Env, "CREWSHIP_CONFIG="+cfg, "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1")
	out, err := cmd.CombinedOutput()
	if err != nil || !bytes.Contains(out, []byte("actual-server-response")) {
		t.Fatalf("session binary: %s / %v", out, err)
	}
}
