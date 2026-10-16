package main

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestPageBoardCommentsCLIRequests(t *testing.T) {
	for _, tc := range []struct {
		name, method, path string
		args               []string
		body               map[string]any
	}{
		{"edit", "PATCH", "/api/v1/pages/report/board/threads/thread-1/comments/comment-1", []string{"edit", "report", "thread-1", "comment-1", "--base-version", "3", "--body", "Corrected", "--mutation-id", "edit-1"}, map[string]any{"base_version": float64(3), "body": "Corrected", "mutation_id": "edit-1"}},
		{"delete", "DELETE", "/api/v1/pages/report/board/threads/thread-1/comments/comment-1", []string{"delete", "report", "thread-1", "comment-1", "--base-version", "4", "--mutation-id", "delete-1"}, map[string]any{"base_version": float64(4), "mutation_id": "delete-1"}},
		{"react add", "PUT", "/api/v1/pages/report/board/threads/thread-1/comments/comment-1/reactions/👀", []string{"react", "report", "thread-1", "comment-1", "--base-version", "5", "--emoji", "👀", "--active=true", "--mutation-id", "react-1"}, map[string]any{"base_version": float64(5), "active": true, "mutation_id": "react-1"}},
		{"react remove", "PUT", "/api/v1/pages/report/board/threads/thread-1/comments/comment-1/reactions/❤️", []string{"react", "report", "thread-1", "comment-1", "--base-version", "6", "--emoji", "❤️", "--active=false", "--mutation-id", "react-2"}, map[string]any{"base_version": float64(6), "active": false, "mutation_id": "react-2"}},
		{"history", "GET", "/api/v1/pages/report/board/threads/thread-1/comments/comment-1/history", []string{"history", "report", "thread-1", "comment-1"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := pageStub(t)
			resetPageArtifactCommand(t)
			stub.On(tc.method, tc.path, func(r *http.Request, raw []byte) (int, []byte, string) {
				if tc.body != nil {
					var got map[string]any
					if err := json.Unmarshal(raw, &got); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(got, tc.body) {
						t.Errorf("body=%#v want=%#v", got, tc.body)
					}
				} else if len(raw) != 0 {
					t.Errorf("history sent body: %q", raw)
				}
				return 200, []byte(`{"body":"\u001b[31m<script>private</script>","applied_version":7}`), "application/json"
			})
			out, err := runPageCLI(t, "", append([]string{"page", "board", "comments"}, tc.args...)...)
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

func TestPageBoardCommentsCLIRejectsMissingOrInvalidPreconditions(t *testing.T) {
	for _, args := range [][]string{
		{"edit", "report", "thread", "comment", "--body", "text"},
		{"edit", "report", "thread", "comment", "--base-version", "1", "--body", " "},
		{"edit", "report", "thread", "comment", "--base-version", "1", "--body", strings.Repeat("é", 2049)},
		{"delete", "report", "thread", "comment", "--base-version", "0"},
		{"delete", "report", "thread", "comment", "--base-version", "1", "--mutation-id", ""},
		{"react", "report", "thread", "comment", "--base-version", "1", "--emoji", "👀"},
		{"react", "report", "thread", "comment", "--base-version", "1", "--emoji", "🔥", "--active=true"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			cmd := newPageBoardCommentsCommand()
			cmd.SetArgs(args)
			if err := cmd.Execute(); err == nil {
				t.Fatal("accepted missing/invalid precondition")
			}
		})
	}
}

func TestPageBoardCommentsCLIConflictDoesNotRetryOrLoseReason(t *testing.T) {
	stub := pageStub(t)
	resetPageArtifactCommand(t)
	const endpoint = "/api/v1/pages/report/board/threads/thread-1/comments/comment-1"
	stub.On("DELETE", endpoint, func(_ *http.Request, _ []byte) (int, []byte, string) {
		return 409, []byte(`{"error":"thread changed; reload before deleting","code":"stale_thread"}`), "application/json"
	})
	_, err := runPageCLI(t, "", "page", "board", "comments", "delete", "report", "thread-1", "comment-1", "--base-version", "2", "--mutation-id", "delete-retry")
	if err == nil || !strings.Contains(err.Error(), "thread changed") {
		t.Fatalf("lost conflict: %v", err)
	}
	if len(stub.CallsFor("DELETE", endpoint)) != 1 {
		t.Fatal("CAS conflict was automatically retried")
	}
}
