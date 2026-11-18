package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestNativeCredentialCreateDoesNotProbeNonTokenTypes(t *testing.T) {
	for _, tc := range []struct{ typ, provider string }{
		{"SECRET", "GITLAB"}, {"GENERIC_SECRET", "GITLAB"}, {"USERPASS", "GITLAB"},
		{"SSH_KEY", "GITLAB"}, {"CERTIFICATE", "GITLAB"}, {"AI_CLI_TOKEN", "GOOGLE"},
	} {
		t.Run(tc.typ, func(t *testing.T) {
			stub := covStub(t)
			covResetFlags(t, credCreateCmd)
			stub.OnPost("/api/v1/credentials", clitest.JSONResponse(201, map[string]string{"id": covCredIDCli3, "name": "fixture"}))
			covWithStdin(t, "synthetic-only\n")
			flags := map[string]string{"name": "fixture", "type": tc.typ, "provider": tc.provider, "value-stdin": "true"}
			if tc.typ == "USERPASS" {
				delete(flags, "value-stdin")
				flags["password-stdin"], flags["username"] = "true", "fixture-user"
			}
			covSetFlags(t, credCreateCmd, flags)
			stderr := covCaptureStderr(t, func() {
				if err := credCreateCmd.RunE(credCreateCmd, nil); err != nil {
					t.Fatal(err)
				}
			})
			if len(stub.CallsFor("POST", "/api/v1/credentials/test")) != 0 {
				t.Fatal("non-token material entered the token probe")
			}
			if !strings.Contains(stderr, "No authentication test was performed") {
				t.Fatal("creation did not explain that authentication remains untested")
			}
		})
	}
}

func TestNativeCredentialUpdateProbesOnlyScalarAndMeteredValues(t *testing.T) {
	for _, tc := range []struct {
		typ, provider, mode string
		probe               bool
	}{
		{"SECRET", "GITLAB", "", false}, {"GENERIC_SECRET", "GITLAB", "", false},
		{"USERPASS", "GITLAB", "", false}, {"SSH_KEY", "GITLAB", "", false},
		{"CERTIFICATE", "GITLAB", "", false}, {"OAUTH2", "GITLAB", "", false},
		{"AI_CLI_TOKEN", "GOOGLE", "", false}, {"AI_CLI_TOKEN", "OPENAI", "", false},
		{"PROVIDER_LOGIN", "OPENAI", "subscription", false},
		{"PROVIDER_LOGIN", "OPENAI", "", false},
		{"PROVIDER_LOGIN", "OPENAI", "api_key", true},
		{"API_KEY", "GITLAB", "", true}, {"CLI_TOKEN", "GITLAB", "", true},
	} {
		t.Run(tc.typ+"/"+tc.provider+"/"+tc.mode, func(t *testing.T) {
			stub := covStub(t)
			covResetFlags(t, credUpdateCmd)
			meta := map[string]any{"type": tc.typ, "provider": tc.provider}
			if tc.mode != "" {
				meta["login"] = map[string]string{"mode": tc.mode}
			}
			stub.OnGet("/api/v1/credentials/"+covCredIDCli3, clitest.JSONResponse(200, meta))
			stub.OnPatch("/api/v1/credentials/"+covCredIDCli3, clitest.JSONResponse(200, map[string]string{"id": covCredIDCli3}))
			stub.OnPost("/api/v1/credentials/test", clitest.JSONResponse(200, map[string]any{"valid": true, "supported": true}))
			covWithStdin(t, "synthetic-only\n")
			covSetFlags(t, credUpdateCmd, map[string]string{"value-stdin": "true"})
			if err := credUpdateCmd.RunE(credUpdateCmd, []string{covCredIDCli3}); err != nil {
				t.Fatal(err)
			}
			calls := stub.CallsFor("POST", "/api/v1/credentials/test")
			if (len(calls) != 0) != tc.probe {
				t.Fatalf("unexpected token probe count: %d", len(calls))
			}
			if tc.probe && tc.typ == "PROVIDER_LOGIN" {
				var body map[string]any
				if err := json.Unmarshal(calls[0].Body, &body); err != nil || body["type"] != "API_KEY" {
					t.Fatal("metered login was not probed as an API key")
				}
			}
		})
	}
}
