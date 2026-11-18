package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// Exercise the native command against the HTTP contract. Structural acceptance
// (valid=true) without a supported authentication probe cannot earn a green tick.
func TestNativeCredentialProbeResultHonesty(t *testing.T) {
	for _, operation := range []string{"create", "update"} {
		for _, typ := range []string{"API_KEY", "PROVIDER_LOGIN"} {
			for _, outcome := range []string{"unsupported", "supported", "missing-supported", "invalid"} {
				t.Run(operation+"/"+typ+"/"+outcome, func(t *testing.T) {
					stub := covStub(t)
					cmd := credCreateCmd
					args := []string(nil)
					flags := map[string]string{"name": "fixture", "type": typ, "provider": "AWS", "value-stdin": "true"}
					if typ == "PROVIDER_LOGIN" {
						flags["mode"], flags["provider"] = "api_key", "OPENAI"
					}
					if operation == "update" {
						cmd, args = credUpdateCmd, []string{covCredIDCli3}
						flags = map[string]string{"value-stdin": "true"}
						meta := map[string]any{"type": typ, "provider": "AWS"}
						if typ == "PROVIDER_LOGIN" {
							meta["login"] = map[string]string{"mode": "api_key"}
							meta["provider"] = "OPENAI"
						}
						stub.OnGet("/api/v1/credentials/"+covCredIDCli3, clitest.JSONResponse(200, meta))
					}
					covResetFlags(t, cmd)
					covSetFlags(t, cmd, flags)
					covWithStdin(t, "synthetic-fixture-only\n")
					result := map[string]any{"valid": true}
					switch outcome {
					case "unsupported":
						result["supported"] = false
					case "supported":
						result["supported"] = true
					case "invalid":
						result["supported"], result["valid"], result["error"] = true, false, "synthetic rejection"
					}
					stub.OnPost("/api/v1/credentials/test", clitest.JSONResponse(200, result))
					stub.OnPost("/api/v1/credentials", clitest.JSONResponse(201, map[string]string{"id": covCredIDCli3}))
					stub.OnPatch("/api/v1/credentials/"+covCredIDCli3, clitest.JSONResponse(200, map[string]string{"id": covCredIDCli3}))
					output := covCaptureAll(t, func() {
						if err := cmd.RunE(cmd, args); err != nil {
							t.Error(err)
						}
					})
					success := strings.Contains(output, "Key validated successfully")
					if success != (outcome == "supported") {
						t.Errorf("authentication-success claim=%v for %s", success, outcome)
					}
					if outcome == "unsupported" || outcome == "missing-supported" {
						if !strings.Contains(output, "No authentication test was performed") {
							t.Error("missing unsupported authentication notice")
						}
					}
					if outcome == "invalid" && !strings.Contains(output, "Key validation failed: synthetic rejection") {
						t.Error("invalid-key warning lost")
					}
					if len(stub.CallsFor("POST", "/api/v1/credentials/test")) != 1 {
						t.Error("expected exactly one probe request")
					}
					method, path := "POST", "/api/v1/credentials"
					if operation == "update" {
						method, path = "PATCH", path+"/"+covCredIDCli3
					}
					if len(stub.CallsFor(method, path)) != 1 {
						t.Error("credential mutation did not complete")
					}
				})
			}
		}
	}
}
