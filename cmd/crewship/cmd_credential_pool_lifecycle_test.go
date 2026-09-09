package main

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestCredentialPoolLifecycleCLI(t *testing.T) {
	for _, tc := range []struct {
		name, method string
		args         []string
	}{
		{"update", "PUT", []string{"update", "pool", "--revision", "3", "--name", "Team", "--member", "account=4"}},
		{"delete", "DELETE", []string{"delete", "pool", "--revision", "3"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := covStub(t)
			stub.On(tc.method, "/api/v1/provider-logins/pools/pool", clitest.JSONResponse(204, nil))
			cmd := newCredentialPoolCmd()
			cmd.SetArgs(tc.args)
			if _, err := captureStdout(t, cmd.Execute); err != nil {
				t.Fatal(err)
			}
			calls := stub.CallsFor(tc.method, "/api/v1/provider-logins/pools/pool")
			if len(calls) != 1 || calls[0].Headers.Get("If-Match") != `"3"` {
				t.Fatalf("missing precondition: %+v", calls)
			}
		})
	}
}

func TestCredentialPoolLifecycleValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"missing revision", []string{"delete", "pool"}},
		{"negative revision", []string{"delete", "pool", "--revision", "-1"}},
		{"missing members", []string{"update", "pool", "--revision", "1", "--name", "Team"}},
		{"bad priority", []string{"update", "pool", "--revision", "1", "--name", "Team", "--member", "a=x"}},
		{"duplicates", []string{"update", "pool", "--revision", "1", "--name", "Team", "--member", "a", "--member", "a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := covStub(t)
			cmd := newCredentialPoolCmd()
			cmd.SetArgs(tc.args)
			if err := cmd.Execute(); err == nil {
				t.Fatal("invalid input accepted")
			}
			for _, method := range []string{"PUT", "DELETE"} {
				if len(stub.CallsFor(method, "/api/v1/provider-logins/pools/pool")) != 0 {
					t.Fatal("invalid request sent")
				}
			}
		})
	}
}

func TestCredentialPoolStaleEditDoesNotRetry(t *testing.T) {
	stub := covStub(t)
	stub.OnPut("/api/v1/provider-logins/pools/pool", clitest.JSONResponse(412, map[string]any{"error": "Provider pool changed; reload before retrying"}))
	cmd := newCredentialPoolCmd()
	cmd.SetArgs([]string{"update", "pool", "--revision", "1", "--name", "Team", "--member", "account"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("stale edit reported success")
	}
	if len(stub.CallsFor("PUT", "/api/v1/provider-logins/pools/pool")) != 1 {
		t.Fatal("stale edit retried")
	}
	if len(stub.CallsFor("GET", "/api/v1/provider-logins/pools/pool")) != 0 {
		t.Fatal("fresh revision fetched without user review")
	}
}
