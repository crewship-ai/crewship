package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"gopkg.in/yaml.v3"
)

func TestChatAttachCmd_OutputFormats(t *testing.T) {
	for _, format := range []string{"json", "yaml", "ndjson", "quiet", "table"} {
		t.Run(format, func(t *testing.T) {
			stub := covStub(t)
			covResetFlags(t, chatAttachCmd)
			flagFormat = format
			// Include the API's canonical relative path; scripts need it for download
			// and deletion, while the agent consumes the separate absolute path.
			want := map[string]any{"filename": "evidence.txt", "size": 11,
				"path":       "attachments/chat/attachment/evidence.txt",
				"agent_path": "/output/ava/attachments/chat/attachment/evidence.txt"}
			path := "/api/v1/agents/" + covAgentIDCli3 + "/chats/" + covChatID + "/attachments"
			stub.OnPost(path, clitest.JSONResponse(201, want))
			src := filepath.Join(t.TempDir(), "evidence.txt")
			if err := os.WriteFile(src, []byte("test upload"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := chatAttachCmd.Flags().Set("agent", covAgentIDCli3); err != nil {
				t.Fatal(err)
			}
			out := captureStdoutCovCli2(t, func() {
				if err := chatAttachCmd.RunE(chatAttachCmd, []string{covChatID, src}); err != nil {
					t.Fatal(err)
				}
			})
			if format == "quiet" {
				if out != want["path"].(string)+"\n" {
					t.Fatalf("quiet output = %q", out)
				}
				return
			}
			if format == "table" {
				if !strings.Contains(out, "Uploaded evidence.txt (11 bytes)") || !strings.Contains(out, want["agent_path"].(string)) {
					t.Fatalf("human output = %q", out)
				}
				return
			}
			var got map[string]any
			if format == "yaml" {
				if err := yaml.Unmarshal([]byte(out), &got); err != nil {
					t.Fatalf("YAML: %v; %s", err, out)
				}
			} else {
				if err := json.Unmarshal([]byte(out), &got); err != nil {
					t.Fatalf("JSON: %v; %s", err, out)
				}
				want["size"] = float64(11)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %#v, want %#v", got, want)
			}
			if format == "ndjson" && strings.Count(out, "\n") != 1 {
				t.Fatalf("NDJSON not one record: %q", out)
			}
		})
	}
}

func TestChatAttachCmd_InvalidResponseIsNotSuccess(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, chatAttachCmd)
	flagFormat = "json"
	path := "/api/v1/agents/" + covAgentIDCli3 + "/chats/" + covChatID + "/attachments"
	stub.OnPost(path, clitest.JSONResponse(201, "not an attachment object"))
	src := filepath.Join(t.TempDir(), "evidence.txt")
	if err := os.WriteFile(src, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := chatAttachCmd.Flags().Set("agent", covAgentIDCli3); err != nil {
		t.Fatal(err)
	}
	out := captureStdoutCovCli2(t, func() {
		if err := chatAttachCmd.RunE(chatAttachCmd, []string{covChatID, src}); err == nil {
			t.Error("invalid response reported success")
		}
	})
	if out != "" {
		t.Fatalf("invalid response produced success output: %q", out)
	}
}
