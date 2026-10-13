package main

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestPageArtifactRequestCLITransport(t *testing.T) {
	for _, tc := range []struct {
		name, method, path string
		args               []string
		body               map[string]any
		status             int
	}{
		{"list", "GET", "/api/v1/pages/report/artifact/requests", []string{"requests", "list", "report"}, nil, 200},
		{"creation status", "GET", "/api/v1/chats/chat-1/artifact-creation-authorization", []string{"creation-status", "chat-1"}, nil, 200},
		{"explicit snapshot", "POST", "/api/v1/pages/report/artifact/requests", []string{"requests", "send", "report", "--chat", "chat-1", "--instruction", "Clarify totals", "--base-rev", "2", "--thread", "thread-1", "--submission-id", "stable-retry"}, map[string]any{"chat_id": "chat-1", "instruction": "Clarify totals", "base_rev": float64(2), "thread_id": "thread-1", "submission_id": "stable-retry"}, 202},
		{"cancel", "POST", "/api/v1/pages/report/artifact/requests/request-1/cancel", []string{"requests", "cancel", "report", "request-1"}, map[string]any{}, 200},
		{"authorize creation", "POST", "/api/v1/artifact-creation-authorizations/run-1", []string{"authorize-creation", "run-1"}, map[string]any{}, 201},
		{"revoke creation", "DELETE", "/api/v1/artifact-creation-authorizations/run-1", []string{"revoke-creation", "run-1"}, nil, 204},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := pageStub(t)
			resetPageArtifactCommand(t)
			stub.On(tc.method, tc.path, func(_ *http.Request, raw []byte) (int, []byte, string) {
				if tc.body != nil {
					var body map[string]any
					if err := json.Unmarshal(raw, &body); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(body, tc.body) {
						t.Errorf("body=%#v want=%#v", body, tc.body)
					}
				}
				if tc.status == 204 {
					return 204, nil, ""
				}
				return tc.status, []byte(`{"id":"request-1","instruction":"\u001b[31m<script>"}`), "application/json"
			})
			out, err := runPageCLI(t, "", append([]string{"page", "artifact"}, tc.args...)...)
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if len(stub.CallsFor(tc.method, tc.path)) != 1 {
				t.Fatal("expected one request")
			}
			if strings.Contains(out, "\x1b") || strings.Contains(out, "<script>") {
				t.Fatalf("unsafe output %q", out)
			}
		})
	}
}

func TestPageArtifactRequestCLIExplicitPreconditions(t *testing.T) {
	for _, flags := range [][]string{
		{"--instruction", "do it", "--base-rev", "1"},
		{"--chat", "chat-1", "--instruction", "do it"},
		{"--chat", "chat-1", "--instruction", "do it", "--base-rev", "0"},
		{"--chat", "chat-1", "--instruction", " ", "--base-rev", "1"},
		{"--chat", "chat-1", "--instruction", strings.Repeat("x", 4097), "--base-rev", "1"},
		{"--chat", "chat-1", "--instruction", "do it", "--base-rev", "1", "--submission-id", ""},
	} {
		cmd := newPageArtifactRequestsCommand()
		cmd.SetArgs(append([]string{"send", "report"}, flags...))
		if err := cmd.Execute(); err == nil {
			t.Fatalf("accepted invalid request flags %v", flags)
		}
	}
}

func TestPageArtifactRequestCLIConflictIsNotRetried(t *testing.T) {
	stub := pageStub(t)
	resetPageArtifactCommand(t)
	path := "/api/v1/pages/report/artifact/requests"
	stub.OnPost(path, func(_ *http.Request, _ []byte) (int, []byte, string) {
		return 409, []byte(`{"error":"artifact revision changed"}`), "application/json"
	})
	_, err := runPageCLI(t, "", "page", "artifact", "requests", "send", "report", "--chat", "chat-1", "--instruction", "do it", "--base-rev", "1", "--submission-id", "retry-1")
	if err == nil || !strings.Contains(err.Error(), "artifact revision changed") {
		t.Fatalf("lost conflict: %v", err)
	}
	if len(stub.CallsFor("POST", path)) != 1 {
		t.Fatal("CLI retried a potentially billable dispatch")
	}
}
