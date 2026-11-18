package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"github.com/spf13/cobra"
)

func TestCrewCodexSandboxConsentRequiresServerAcknowledgement(t *testing.T) {
	for _, which := range []string{"accepted", "old-server", "wrong-value"} {
		t.Run(which, func(t *testing.T) {
			stub := covSetupCli4(t)
			stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{{"id": covCrewIDCli4, "slug": "fixture"}}))
			response := map[string]any{"id": covCrewIDCli4}
			if which != "old-server" {
				response["allow_codex_outer_sandbox"] = which == "accepted"
			}
			stub.OnPatch("/api/v1/crews/"+covCrewIDCli4, clitest.JSONResponse(200, response))
			c := covFreshCmd(crewUpdateCmd, func(c *cobra.Command) {
				declareCrewUpdateFlags(c)
				c.Flags().Bool("allow-codex-outer-sandbox", false, "")
			})
			covSetFlagsCli4(t, c, map[string]string{"allow-codex-outer-sandbox": "true"})
			out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{"fixture"}) })
			if which == "accepted" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				if err == nil || strings.Contains(out, "Crew updated successfully") {
					t.Fatal("CLI acknowledged consent that the server did not retain")
				}
			}
			patches := stub.CallsFor("PATCH", "/api/v1/crews/"+covCrewIDCli4)
			if len(patches) != 1 {
				t.Fatal("missing native crew PATCH")
			}
			var body map[string]any
			if json.Unmarshal(patches[0].Body, &body) != nil || body["allow_codex_outer_sandbox"] != true {
				t.Fatal("consent flag missing from request")
			}
		})
	}
}
