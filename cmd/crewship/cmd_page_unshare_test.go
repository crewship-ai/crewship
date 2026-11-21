package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestPageUnshareCLINeverConfirmsWithoutReviewedToken(t *testing.T) {
	stub := pageStub(t)
	calls := 0
	stub.OnPost("/api/v1/pages/fleet-201/return-private", func(_ *http.Request, _ []byte) (int, []byte, string) {
		calls++
		return 200, []byte(`{}`), "application/json"
	})
	for _, args := range [][]string{
		{"page", "unshare", "fleet-201", "--pages-version", "0"},
		{"page", "unshare", "fleet-201", "--yes", "--pages-version", "0"},
		{"page", "unshare", "fleet-201", "--dry-run", "--yes", "--pages-version", "0"},
		{"page", "unshare", "fleet-201", "--dry-run"},
	} {
		if _, err := runPageCLI(t, "", args...); err == nil {
			t.Fatalf("%v accepted", args)
		}
	}
	if calls != 0 {
		t.Fatal("unreviewed return reached the server")
	}
}

func TestPageUnshareHumanListsEveryRemovedAccess(t *testing.T) {
	out, err := pageUnshareHuman([]byte(`{"dry_run":true,"page":"q3","kind":"workbook","pages_version":4,"from_folder":{"id":"f","slug":"finance","acl_version":2},
"changes":[{"subject_type":"user","subject_id":"u1","label":"bob@ex.com","before":{"read":true,"comment":true,"edit":true},"after":{}}],
"removed_grants":[{"subject_type":"user","subject_id":"u1","label":"bob@ex.com","level":"write"}],
"revoked_public_links":[{"id":"link-1","expires_at":"2999-01-01T00:00:00Z"}],"revoked_mandates":2,
"notice":"copies already downloaded, exported or read cannot be recalled","changed":false,"review_token":"tok"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"leaves finance", "user bob@ex.com read,comment,edit -> none", "grant: user bob@ex.com write", "public link: link-1", "agent mandates: 2", "cannot be recalled", "--yes --pages-version 4 --review-token tok"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}
