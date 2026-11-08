package api

import (
	"encoding/json"
	"net/url"
	"testing"
)

// #5129: a created document links to its one canonical address,
// /artifacts/<immutable page ID>?workspace_id=…, on the first answer and on an
// exact retry that replays it. The web app still opens old /pages?ref= links.
func TestDocumentCreationReturnsTheCanonicalArtifactsURL(t *testing.T) {
	h, workspace, _, _, call := documentCreationFixture(t)
	h.db.SetMaxOpenConns(1)
	for _, attempt := range []string{"create", "exact retry"} {
		response := call(attempt, documentCreationBody)
		if response.Code != 201 && response.Code != 200 {
			t.Fatalf("%s: %d %s", attempt, response.Code, response.Body.String())
		}
		var result documentCreationResult
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.PageID == "" {
			t.Fatalf("%s: %s %v", attempt, response.Body.String(), err)
		}
		want := "/artifacts/" + url.PathEscape(result.PageID) + "?" + url.Values{"workspace_id": {workspace}}.Encode()
		if result.URL != want {
			t.Fatalf("%s url %q, want %q", attempt, result.URL, want)
		}
	}
}

func TestArtifactCanonicalURL(t *testing.T) {
	for _, tc := range []struct{ page, workspace, want string }{
		{"page-brief", "ws1", "/artifacts/page-brief?workspace_id=ws1"},
		{"page a", "ws 1", "/artifacts/page%20a?workspace_id=ws+1"},
	} {
		if got := artifactCanonicalURL(tc.page, tc.workspace); got != tc.want {
			t.Errorf("artifactCanonicalURL(%q, %q) = %q, want %q", tc.page, tc.workspace, got, tc.want)
		}
	}
	if got, want := artifactCanonicalURL("page-brief", "ws1"), artifactProjectURL("page-brief", "ws1", 0); got != want {
		t.Errorf("document and project links differ: %q vs %q", got, want)
	}
}
