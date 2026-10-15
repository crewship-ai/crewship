package main

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestPageArtifactDelegationCLI(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		verb, method, path := "delegate", "POST", "/api/v1/pages/report/artifact/delegations"
		if revoke {
			verb, method, path = "revoke", "DELETE", path+"/run-1"
		}
		t.Run(verb, func(t *testing.T) {
			stub := pageStub(t)
			resetPageArtifactCommand(t)
			stub.On(method, path, func(_ *http.Request, raw []byte) (int, []byte, string) {
				if revoke {
					return 204, nil, "application/json"
				}
				var body map[string]any
				if err := json.Unmarshal(raw, &body); err != nil {
					t.Fatal(err)
				}
				if len(body) != 1 || body["run_id"] != "run-1" {
					t.Fatalf("unexpected authority request %#v", body)
				}
				return 201, []byte(`{"run_id":"run-1","expires_at":"2026-10-13T16:00:00Z"}`), "application/json"
			})
			if out, err := runPageCLI(t, "", "page", "artifact", verb, "report", "--run", "run-1"); err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if len(stub.CallsFor(method, path)) != 1 {
				t.Fatal("expected one request")
			}
		})
	}
}

func TestPageArtifactDelegationCLIRequiresRun(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		cmd := newPageArtifactDelegationCommand(revoke)
		cmd.SetArgs([]string{"report"})
		if err := cmd.Execute(); err == nil {
			t.Fatal("accepted missing run")
		}
	}
}
