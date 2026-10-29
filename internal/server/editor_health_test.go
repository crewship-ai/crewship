package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/editororigin"
)

// /healthz is public. It reports only a coarse editor state; the redirect
// target, error strings and origins of a failed self-check stay with the
// server log and the instance-admin detail endpoint.
func TestHealthzReportsOnlyCoarseEditorState(t *testing.T) {
	_, editor, _ := sameHostEditor(t, "https://studio.example.test")
	editor.RecordCheck("https://studio.example.test", editororigin.ProxyCheck{Status: editororigin.ProxyCheckFailed, Isolation: true, Cause: editororigin.CauseRedirect,
		Problems: []string{`/_editor/b/x is redirected (HTTP 302 to "https://sso.corp.example:8443/oauth2/start?rd=https%3A%2F%2F10.20.30.40")`}})
	rec := httptest.NewRecorder()
	(&Server{editor: editor}).handleHealthz(rec, httptest.NewRequest("GET", "/healthz", nil))
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if got := string(body["workbook_editor"]); got != `"disabled"` {
		t.Fatalf("workbook_editor = %s, want \"disabled\"", got)
	}
	for _, leak := range []string{"sso.corp", "10.20.30.40", "oauth2", "studio.example.test", "302"} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Fatalf("public /healthz leaks %q: %s", leak, rec.Body.String())
		}
	}
}
