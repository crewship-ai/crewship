package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

func TestContextCommandsPreserveExactScopedRequests(t *testing.T) {
	cases := []struct {
		command                    []string
		action, method, path, body string
	}{
		{[]string{"chat", "project-input-options", "chat"}, "project-input-options", "GET", "/api/v1/chats/chat/project-input-options", ""},
		{[]string{"chat", "profile", "chat"}, "profile", "GET", "/api/v1/chats/chat/execution-profile", ""},
		{[]string{"chat", "download", "chat", "file"}, "chat-download", "GET", "/api/v1/chats/chat/restricted-files/file/download", ""},
		{[]string{"execution-profile", "get", "agent"}, "profile-get", "GET", "/api/v1/agents/agent/restricted-execution", ""},
		{[]string{"project-files", "list", "project"}, "project-list", "GET", "/api/v1/workspaces/c0000000000000000000000000/projects/project/files", ""},
		{[]string{"project-files", "download", "project", "version"}, "project-download", "GET", "/api/v1/workspaces/c0000000000000000000000000/projects/project/files/version/download", ""},
		{[]string{"pages"}, "pages-list", "GET", "/api/v1/workspaces/c0000000000000000000000000/restricted-pages", ""},
		{[]string{"routines"}, "routines-list", "GET", "/api/v1/workspaces/c0000000000000000000000000/restricted-routines", ""},
		{[]string{"routine-runs", "list"}, "runs-list", "GET", "/api/v1/workspaces/c0000000000000000000000000/restricted-routine-runs", ""},

		{[]string{"chat", "inspect", "chat"}, "inspect", "GET", "/api/v1/chats/chat/restricted-context", ""},
		{[]string{"chat", "memory", "chat"}, "memory", "GET", "/api/v1/chats/chat/restricted-context", ""},
		{[]string{"chat", "files", "chat"}, "files", "GET", "/api/v1/chats/chat/restricted-files", ""},
		{[]string{"chat", "attempts", "chat"}, "attempts", "GET", "/api/v1/chats/chat/restricted-attempts", ""},
		{[]string{"chat", "memory-add", "chat", "own note"}, "memory-add", "POST", "/api/v1/chats/chat/restricted-memory", `{"content":"own note"}`},
		{[]string{"chat", "memory-delete", "chat", "entry"}, "memory-delete", "DELETE", "/api/v1/chats/chat/restricted-memory/entry", ""},
		{[]string{"execution-profile", "set", "agent", "native_api_key"}, "profile-set", "PUT", "/api/v1/agents/agent/restricted-execution", `{"profile":"native_api_key"}`},
		{[]string{"project-files", "retire", "project", "file", "--expected-revision", "7"}, "project-retire", "DELETE", "/api/v1/workspaces/c0000000000000000000000000/projects/project/files/file", `{"expected_revision":7}`},
		{[]string{"issue-preflight", "issue", "--claim", "--agent-id", "agent", "--routine-slug", "declared"}, "preflight", "POST", "/api/v1/workspaces/c0000000000000000000000000/issues/issue/private-preflight", `{"agent_id":"agent","routine_slug":"declared"}`},
		{[]string{"routine-runs", "result", "run"}, "runs-result", "GET", "/api/v1/workspaces/c0000000000000000000000000/restricted-routine-runs/run", ""},
	}
	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != tc.method || r.URL.Path != tc.path {
					t.Errorf("request %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("Authorization") != "Bearer synthetic-token" {
					t.Error("authentication absent")
				}
				if tc.action == "memory" && r.URL.Query().Get("kind") != "memory" {
					t.Error("note projection absent")
				}
				data, _ := io.ReadAll(r.Body)
				if tc.body != "" {
					var got, want any
					if json.Unmarshal(data, &got) != nil || json.Unmarshal([]byte(tc.body), &want) != nil {
						t.Fatal("invalid JSON")
					}
					g, _ := json.Marshal(got)
					e, _ := json.Marshal(want)
					if string(g) != string(e) {
						t.Errorf("body %s", g)
					}
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			root := newContextCommand()
			cmd, args, err := root.Find(tc.command)
			if err != nil {
				t.Fatal(err)
			}
			if err = cmd.ParseFlags(args); err != nil {
				t.Fatal(err)
			}
			client := cli.NewClient(server.URL, "synthetic-token", "c0000000000000000000000000")
			if err = executeContextCommand(cmd, client, tc.action, cmd.Flags().Args()); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("calls=%d", calls)
			}
		})
	}
}

func TestContextMutationsRequireExplicitClaimAndRevision(t *testing.T) {
	root := newContextCommand()
	for _, tc := range []struct {
		args   []string
		action string
	}{
		{[]string{"issue-preflight", "issue", "--agent-id", "a", "--routine-slug", "r"}, "preflight"},
		{[]string{"project-files", "retire", "p", "f"}, "project-retire"},
		{[]string{"execution-profile", "set", "a", "danger"}, "profile-set"},
		{[]string{"chat", "memory-delete", "../foreign", "entry"}, "memory-delete"},
	} {
		cmd, args, err := root.Find(tc.args)
		if err != nil {
			t.Fatal(err)
		}
		if err = cmd.ParseFlags(args); err != nil {
			t.Fatal(err)
		}
		client := cli.NewClient("http://127.0.0.1:1", "synthetic-token", "c0000000000000000000000000")
		if err = executeContextCommand(cmd, client, tc.action, cmd.Flags().Args()); err == nil {
			t.Fatalf("unsafe %s admitted", tc.action)
		}
	}
}

func TestContextProjectUploadUsesBoundedBase64AndExactRevision(t *testing.T) {
	file := filepath.Join(t.TempDir(), "source.bin")
	content := []byte{0, 1, 255}
	if err := os.WriteFile(file, content, 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			FileID   string `json:"file_id"`
			Name     string `json:"name"`
			Revision int64  `json:"expected_revision"`
			Content  []byte `json:"content_base64"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.FileID != "file" || body.Name != "src/input.bin" || body.Revision != 3 || !bytes.Equal(body.Content, content) {
			t.Errorf("wrong upload %#v", body)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	root := newContextCommand()
	cmd, args, err := root.Find([]string{"project-files", "upload", "project", file, "--file-id", "file", "--expected-revision", "3", "--name", "src/input.bin"})
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.ParseFlags(args); err != nil {
		t.Fatal(err)
	}
	if err = executeContextCommand(cmd, cli.NewClient(server.URL, "synthetic", "c0000000000000000000000000"), "project-upload", cmd.Flags().Args()); err != nil {
		t.Fatal(err)
	}
}

func TestContextDownloadErrorDoesNotReplaceExistingFile(t *testing.T) {
	out := filepath.Join(t.TempDir(), "existing")
	if err := os.WriteFile(out, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	root := newContextCommand()
	cmd, _, err := root.Find([]string{"chat", "download"})
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Flags().Set("out", out); err != nil {
		t.Fatal(err)
	}
	if err = saveContextDownload(cmd, io.MultiReader(strings.NewReader("partial"), contextFailReader{})); err == nil {
		t.Fatal("partial download succeeded")
	}
	data, err := os.ReadFile(out)
	if err != nil || string(data) != "keep" {
		t.Fatal("existing file replaced")
	}
}

type contextFailReader struct{}

func (contextFailReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestContextChatStreamRequiresSuccessfulTerminal(t *testing.T) {
	for _, tc := range []struct {
		stream string
		fails  bool
	}{
		{"data: {\"type\":\"text\",\"text\":\"answer\"}\n\ndata: {\"type\":\"done\"}\n\n", false},
		{"event: text\ndata: {}\n\n", true},
		{"event: error\ndata: {\"error\":\"denied\"}\n\n", true},
	} {
		cmd := newContextCommand()
		cmd.SetOut(io.Discard)
		if err := readContextSSE(cmd, strings.NewReader(tc.stream)); (err != nil) != tc.fails {
			t.Errorf("terminal result %v", err)
		}
	}
}
