package main

import (
	"net/http"
	"strings"
	"testing"
)

func resetDocCommand(t *testing.T) {
	t.Helper()
	old := findSubcommand(rootCmd.Commands(), "doc")
	fresh := newDocCommand()
	rootCmd.RemoveCommand(old)
	rootCmd.AddCommand(fresh)
	t.Cleanup(func() { rootCmd.RemoveCommand(fresh); rootCmd.AddCommand(old) })
}
func TestDocumentCLIPreservesSemanticPreconditions(t *testing.T) {
	stub := pageStub(t)
	resetDocCommand(t)
	payload := `{"id":"retry","schema":1,"epoch":7,"ifRev":3,"target":"block","ifHash":"digest","text":"changed"}`
	stub.OnPost("/api/v1/pages/report/document/patch", func(r *http.Request, body []byte) (int, []byte, string) {
		for _, field := range []string{`"id":"retry"`, `"epoch":7`, `"ifRev":3`, `"target":"block"`, `"ifHash":"digest"`} {
			if !strings.Contains(string(body), field) {
				t.Errorf("lost precondition %s in %s", field, body)
			}
		}
		return 409, []byte(`{"error":"semantic precondition conflict"}`), "application/json"
	})
	_, err := runPageCLI(t, payload, "doc", "patch", "report", "--file", "-")
	if err == nil || !strings.Contains(err.Error(), "semantic precondition conflict") {
		t.Fatalf("conflict hidden: %v", err)
	}
	if len(stub.CallsFor("POST", "/api/v1/pages/report/document/patch")) != 1 {
		t.Fatal("CLI automatically retried/rebased conflicting edit")
	}
}

func TestDocumentCLIReadParityRoutes(t *testing.T) {
	for _, tc := range []struct {
		args []string
		path string
	}{
		{[]string{"status"}, "/api/v1/documents/status"},
		{[]string{"checkpoint-read", "report", "pin-1"}, "/api/v1/pages/report/document/checkpoints/pin-1"},
		{[]string{"checkpoints", "report"}, "/api/v1/pages/report/document/checkpoints"},
		{[]string{"comments", "report"}, "/api/v1/pages/report/document/comments"},
		{[]string{"request-list", "report"}, "/api/v1/pages/report/document/requests"},
	} {
		t.Run(tc.args[0], func(t *testing.T) {
			stub := pageStub(t)
			resetDocCommand(t)
			stub.OnGet(tc.path, func(_ *http.Request, _ []byte) (int, []byte, string) {
				return 200, []byte(`{"enabled":true}`), "application/json"
			})
			_, err := runPageCLI(t, "", append([]string{"doc"}, tc.args...)...)
			if err != nil {
				t.Fatal(err)
			}
			if len(stub.CallsFor("GET", tc.path)) != 1 {
				t.Fatal("command did not call exact declared endpoint")
			}
		})
	}
}
