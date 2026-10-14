package main

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestPageBoardThreadsCLIRequests(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, revision string
		args                         []string
		body                         map[string]any
	}{
		{"list all", "GET", "/api/v1/pages/report/board/threads", "", []string{"show", "report"}, nil},
		{"comment board", "POST", "/api/v1/pages/report/board/threads", "", []string{"create", "report", "--board-seq", "2", "--body", "Please clarify", "--mutation-id", "create-1"}, map[string]any{"anchor": map[string]any{"schema_version": float64(1), "type": "board", "board_seq": float64(2)}, "body": "Please clarify", "mutation_id": "create-1"}},
		{"element anchor", "POST", "/api/v1/pages/report/board/threads", "", []string{"create", "report", "--board-seq", "2", "--element-id", "node", "--element-rev", "3", "--body", "Clarify", "--mutation-id", "node-comment"}, map[string]any{"anchor": map[string]any{"schema_version": float64(1), "type": "board-element", "board_seq": float64(2), "element_id": "node", "element_rev": float64(3)}, "body": "Clarify", "mutation_id": "node-comment"}},
		{"reply CAS", "POST", "/api/v1/pages/report/board/threads/thread-1/comments", "", []string{"reply", "report", "thread-1", "--base-version", "3", "--body", "Done", "--mutation-id", "reply-1"}, map[string]any{"base_version": float64(3), "body": "Done", "mutation_id": "reply-1"}},
		{"resolve CAS", "POST", "/api/v1/pages/report/board/threads/thread-1/resolve", "", []string{"resolve", "report", "thread-1", "--base-version", "4", "--mutation-id", "resolve-1"}, map[string]any{"base_version": float64(4), "resolved": true, "mutation_id": "resolve-1"}},
		{"reopen CAS", "POST", "/api/v1/pages/report/board/threads/thread-1/resolve", "", []string{"reopen", "report", "thread-1", "--base-version", "5", "--mutation-id", "reopen-1"}, map[string]any{"base_version": float64(5), "resolved": false, "mutation_id": "reopen-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := pageStub(t)
			resetPageArtifactCommand(t)
			stub.On(tc.method, tc.path, func(r *http.Request, raw []byte) (int, []byte, string) {
				if r.URL.Query().Get("revision") != tc.revision {
					t.Errorf("unexpected revision %q", r.URL.Query().Get("revision"))
				}
				if tc.body != nil {
					var got map[string]any
					if err := json.Unmarshal(raw, &got); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(got, tc.body) {
						t.Errorf("body=%#v want=%#v", got, tc.body)
					}
				}
				return 200, []byte(`{"body":"\u001b[31m<script>hi</script>","version":6}`), "application/json"
			})
			out, err := runPageCLI(t, "", append([]string{"page", "board", "threads"}, tc.args...)...)
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if len(stub.CallsFor(tc.method, tc.path)) != 1 {
				t.Fatal("expected exactly one request")
			}
			if strings.Contains(out, "\x1b") || strings.Contains(out, "<script>") {
				t.Fatalf("unsafe output %q", out)
			}
		})
	}
}

func TestPageBoardThreadsCLIRequiresExplicitPreconditions(t *testing.T) {
	for _, args := range [][]string{
		{"create", "report", "--body", "hello"},
		{"create", "report", "--board-seq", "0", "--body", "hello", "--element-id", "node"},
		{"create", "report", "--board-seq", "1", "--body", " "},
		{"create", "report", "--board-seq", "1", "--body", strings.Repeat("x", 4097)},
		{"reply", "report", "thread-1", "--body", "hello"},
		{"reply", "report", "thread-1", "--base-version", "0", "--body", "hello"},
		{"resolve", "report", "thread-1"},
		{"reopen", "report", "thread-1", "--base-version", "1", "--mutation-id", ""},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			cmd := newPageBoardThreadsCommand()
			cmd.SetArgs(args)
			if err := cmd.Execute(); err == nil {
				t.Fatal("accepted missing or invalid precondition")
			}
		})
	}
}

func TestPageBoardThreadsCLIConflictKeepsServerReason(t *testing.T) {
	stub := pageStub(t)
	resetPageArtifactCommand(t)
	stub.OnPost("/api/v1/pages/report/board/threads/thread-1/resolve", func(_ *http.Request, _ []byte) (int, []byte, string) {
		return 409, []byte(`{"error":"thread changed; reload before resolving"}`), "application/json"
	})
	_, err := runPageCLI(t, "", "page", "board", "threads", "resolve", "report", "thread-1", "--base-version", "2")
	if err == nil || !strings.Contains(err.Error(), "thread changed") {
		t.Fatalf("lost conflict: %v", err)
	}
}
