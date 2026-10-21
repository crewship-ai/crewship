package main

import (
	"net/http"
	"testing"
)

func TestSharedIssueCLIReadsOnlyGuestProjection(t *testing.T) {
	stub := covStub(t)
	path := "/api/v1/workspaces/" + covWSCli3 + "/restricted-issues/CLIENT-1"
	stub.OnGet(path, func(*http.Request, []byte) (int, []byte, string) {
		return 200, []byte(`{"id":"issue-1","identifier":"CLIENT-1","title":"Shared","status":"TODO","project_id":"project-1","project_name":"Client","can_read":true,"description":"Brief"}`), "application/json"
	})
	if err := issueSharedCmd.RunE(newFlagCmd(nil, nil), []string{"CLIENT-1"}); err != nil {
		t.Fatal(err)
	}
	if len(stub.CallsFor("GET", path)) != 1 {
		t.Fatal("shared issue projection not requested")
	}
	if len(stub.Calls()) != 1 {
		t.Fatal("shared issue command requested extra resources")
	}
}
