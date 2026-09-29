package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
)

func TestMemberAccessSetPreservesExplicitRevisionAndDoesNotRetry(t *testing.T) {
	stub := covStub(t)
	stub.OnGet("/api/v1/workspaces/"+covWSCli3+"/members", func(*http.Request, []byte) (int, []byte, string) {
		return 200, []byte(memberIDRoster), "application/json"
	})
	path := "/api/v1/workspaces/" + covWSCli3 + "/members/m-viewer/access"
	stub.OnPut(path, func(_ *http.Request, b []byte) (int, []byte, string) {
		var p access.Policy
		if err := json.Unmarshal(b, &p); err != nil {
			t.Fatal(err)
		}
		if p.ID != "m-viewer" || p.Revision != 7 || p.Mode != "restricted" || p.Rights == nil || len(p.Rights) != 0 {
			t.Fatalf("changed replacement document: %+v", p)
		}
		return 409, []byte(`{"error":"policy changed"}`), "application/json"
	})
	cmd := newFlagCmd(map[string]string{"file": "-"}, nil)
	cmd.SetIn(strings.NewReader(`{"membership_id":"m-viewer","revision":7,"mode":"restricted","rights":[]}`))
	if err := workspaceMemberAccessSetCmd.RunE(cmd, []string{"u-viewer"}); err == nil || !strings.Contains(err.Error(), "409") {
		t.Fatalf("stale revision: %v", err)
	}
	if len(stub.CallsFor("PUT", path)) != 1 || len(stub.CallsFor("GET", path)) != 0 {
		t.Fatal("silently refreshed or retried stale policy")
	}
}

func TestMemberAccessSetRejectsAmbiguousOrForgedPolicy(t *testing.T) {
	for _, body := range []string{
		`{"membership_id":"m-viewer","revision":7,"mode":"restricted"}`,
		`{"membership_id":"m-viewer","revision":7,"mode":"restricted","rights":null}`,
		`{"membership_id":"m-viewer","revision":7,"mode":"restricted","rights":[],"actor":"owner"}`,
	} {
		t.Run(body, func(t *testing.T) {
			stub := covStub(t)
			cmd := newFlagCmd(map[string]string{"file": "-"}, nil)
			cmd.SetIn(strings.NewReader(body))
			if err := workspaceMemberAccessSetCmd.RunE(cmd, []string{"u-viewer"}); err == nil {
				t.Fatal("accepted invalid policy")
			}
			if len(stub.Calls()) != 0 {
				t.Fatal("invalid policy reached API")
			}
		})
	}
}
