package main

import (
	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"strconv"
	"strings"
	"testing"
)

func TestCrewProvisionRevisionsReportsHistoryAndHTTPFailure(t *testing.T) {
	for _, status := range []int{200, 403} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			stub := covSetupCli5(t)
			flagFormat = "json"
			body := map[string]any{"revisions": []map[string]any{{"id": "revision-one", "image_id": "sha256:artifact", "definition_hash": "recipe", "build_hash": "build", "created_at": "2026-10-03T00:00:00Z", "toolchain": nil}}}
			if status != 200 {
				body = map[string]any{"error": "Forbidden"}
			}
			stub.OnGet("/api/v1/crews/"+covCrewIDCli5+"/provision/revisions", clitest.JSONResponse(status, body))
			var err error
			out := covCaptureStdoutCli5(t, func() { err = crewProvisionRevisionsCmd.RunE(crewProvisionRevisionsCmd, []string{covCrewIDCli5}) })
			if status == 200 {
				if err != nil || !strings.Contains(out, "revision-one") || !strings.Contains(out, "sha256:artifact") {
					t.Fatalf("history: %s %v", out, err)
				}
			} else if err == nil {
				t.Fatal("HTTP error presented as empty history")
			}
		})
	}
}
