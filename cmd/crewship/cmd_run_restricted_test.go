package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestRunCmdRestrictedAutomaticProfileAndSave(t *testing.T) {
	for _, complete := range []bool{true, false} {
		t.Run(map[bool]string{true: "complete", false: "revoked"}[complete], func(t *testing.T) {
			stub := clitest.NewStubServer()
			defer stub.Close()
			setupStubCLICov(t, stub)
			t.Cleanup(ResetAIFirstLatches)
			stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{{"id": covAgentID, "slug": "viktor"}}))
			stub.OnPost("/api/v1/agents/"+covAgentID+"/chats", clitest.JSONResponse(200, map[string]string{"id": "private-chat"}))
			stub.OnGet("/api/v1/chats/private-chat/execution-profile", clitest.JSONResponse(200, map[string]string{"mode": "restricted"}))
			stream := "data: {\"type\":\"text\",\"text\":\"private answer\"}\n\n"
			if complete {
				stream += "data: {\"type\":\"done\",\"text\":\"\"}\n\n"
			}
			stub.OnPost("/api/v1/chats/private-chat/restricted-run", clitest.TextResponse(200, stream))
			target := filepath.Join(t.TempDir(), "answer.txt")
			if err := os.WriteFile(target, []byte("previous"), 0600); err != nil {
				t.Fatal(err)
			}
			setFlagCov(t, runCmd, "save", target)
			setFlagCov(t, runCmd, "no-stream", "true")
			setFormatCov(t, "json")
			out, err := captureStdoutCov(t, func() error { return runCmd.RunE(runCmd, []string{"viktor", "private question"}) })
			if (err == nil) != complete {
				t.Fatalf("error %v", err)
			}
			data, readErr := os.ReadFile(target)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if complete {
				if string(data) != "private answer" || !strings.Contains(out, `"status": "completed"`) {
					t.Fatalf("saved %s output %s", data, out)
				}
			} else if string(data) != "previous" {
				t.Fatal("failed response overwrote prior save")
			}
			if len(stub.CallsFor("GET", "/api/v1/ws-token")) != 0 {
				t.Fatal("restricted run requested legacy WS token")
			}
		})
	}
}
