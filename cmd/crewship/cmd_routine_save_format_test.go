package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"gopkg.in/yaml.v3"
)

func TestRoutineSaveMachineFormats(t *testing.T) {
	for _, format := range []string{"json", "yaml", "ndjson"} {
		t.Run(format, func(t *testing.T) {
			stub := clitest.NewStubServer()
			defer stub.Close()
			setupStubCLICov(t, stub)
			stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{{"id": "crew-id", "slug": "ops"}}))
			stub.OnPost(pipelinesPathCov()+"/test_run", clitest.JSONResponse(200, map[string]any{"status": "DRY_RUN_OK", "save_token": "proof"}))
			stub.OnPost(pipelinesPathCov()+"/save", clitest.JSONResponse(200, map[string]any{
				"id": "saved-id", "slug": "format-probe", "definition_hash": "a",
				"trigger": map[string]any{"kind": "schedule", "schedule_id": "exact-schedule-id", "enabled": true},
			}))
			definition := filepath.Join(t.TempDir(), "definition.json")
			if err := os.WriteFile(definition, []byte(`{"dsl_version":"1.0","name":"format-probe","agentless":true,"steps":[{"id":"one","type":"transform","transform":{"input":"ok","expression":"."}}]}`), 0600); err != nil {
				t.Fatal(err)
			}
			setFlagCov(t, pipelineSaveCmd, "definition", definition)
			setFlagCov(t, pipelineSaveCmd, "name", "format-probe")
			setFlagCov(t, pipelineSaveCmd, "author-crew", "ops")
			flagFormat = format
			t.Cleanup(func() { flagFormat = "" })
			out, err := captureStdoutCov(t, func() error { return pipelineSaveCmd.RunE(pipelineSaveCmd, nil) })
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if format == "yaml" {
				err = yaml.Unmarshal([]byte(out), &got)
			} else {
				err = json.Unmarshal([]byte(out), &got)
			}
			if err != nil {
				t.Fatalf("machine output must be only a structured receipt: %v\n%s", err, out)
			}
			if got["id"] != "saved-id" || got["slug"] != "format-probe" || got["definition_hash"] != "a" {
				t.Fatalf("identity missing: %s", out)
			}
			trigger, ok := got["trigger"].(map[string]any)
			if !ok || trigger["schedule_id"] != "exact-schedule-id" {
				t.Fatalf("trigger metadata missing: %s", out)
			}
			if strings.Contains(out, "Validating routine") || strings.Contains(out, "Invoke with") {
				t.Fatalf("human output leaked: %s", out)
			}
		})
	}
}
