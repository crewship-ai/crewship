package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Drive authoring, signed ingress and durable run reads through the built CLI
// and real router. The recipe is agentless; the runner must never call a model.
func TestAcceptance_RoutineInputContracts(t *testing.T) {
	cfg, server := startReliabilityAcceptanceServer(t)
	definition := map[string]any{"name": "contract-form", "agentless": true,
		"inputs": []any{map[string]any{"name": "qty", "type": "integer", "widget": "number", "required": true, "min": 1}},
		"steps":  []any{map[string]any{"id": "echo", "type": "transform", "transform": map[string]any{"input": "{{ inputs.qty }}", "expression": "."}}},
	}
	save := func(expression string) (string, error) {
		definition["steps"].([]any)[0].(map[string]any)["transform"].(map[string]any)["expression"] = expression
		body, err := json.Marshal(map[string]any{"slug": "contract-form", "name": "contract-form", "definition": definition, "skip_test_gate": true})
		if err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(t.TempDir(), "definition.json")
		if err := os.WriteFile(file, body, 0600); err != nil {
			t.Fatal(err)
		}
		return runReliabilityCLI(t, cfg, "api", "request", "POST", "/api/v1/workspaces/"+reliabilityAcceptanceWorkspaceID+"/pipelines/save", "--input", file, "--yes")
	}
	if out, err := save(".qty + .unit"); err == nil || !strings.Contains(out, "422") || !strings.Contains(out, "expression") {
		t.Fatalf("CLI failed to surface transform admission error: %v\n%s", err, out)
	}
	if out, err := save("."); err != nil {
		t.Fatalf("save valid recipe: %v\n%s", err, out)
	}
	out, err := runReliabilityCLI(t, cfg, "-f", "json", "routine", "webhooks", "create", "--slug", "contract-form", "--base-url", server)
	if err != nil {
		t.Fatalf("create webhook: %v\n%s", err, out)
	}
	var wh struct {
		ID        string `json:"id"`
		PublicURL string `json:"public_url"`
		Secret    string `json:"signing_secret"`
	}
	if err := json.Unmarshal([]byte(out), &wh); err != nil || wh.ID == "" || wh.Secret == "" {
		t.Fatal("create webhook did not return required signing fields")
	}
	fire := func() (string, error) {
		return runReliabilityCLI(t, cfg, "-f", "json", "routine", "webhooks", "fire", wh.PublicURL, "--secret", wh.Secret, "--body", `{"qty":9}`, "--delivery-id", "input-contract-event")
	}
	if out, err := fire(); err == nil || !strings.Contains(out, "400") || strings.Contains(out, `"run_id"`) {
		t.Fatalf("invalid inputs must fail before a run handle is accepted: %v\n%s", err, out)
	}
	if out, err := runReliabilityCLI(t, cfg, "routine", "webhooks", "update", wh.ID, "--inputs-template", `{"qty":9}`); err != nil {
		t.Fatalf("correct inputs: %v\n%s", err, out)
	}
	out, err = fire()
	if err != nil {
		t.Fatalf("corrected signed delivery: %v\n%s", err, out)
	}
	accepted := decodeWebhookFire(t, out)
	if accepted.HTTPStatus != 202 || accepted.RunID == "" || accepted.Duplicate {
		t.Fatalf("corrected event was not freshly accepted: %+v", accepted)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out, err := runReliabilityCLI(t, cfg, "api", "request", "GET", "/api/v1/workspaces/"+reliabilityAcceptanceWorkspaceID+"/pipeline-runs/"+accepted.RunID)
		if err == nil {
			var result struct {
				Status string `json:"status"`
				Output string `json:"output"`
			}
			if err := json.Unmarshal([]byte(out), &result); err != nil {
				t.Fatal(err)
			}
			if result.Status == "completed" {
				if result.Output != "9" {
					t.Fatalf("wrong business output: %q", result.Output)
				}
				return
			}
			if result.Status == "failed" {
				t.Fatal("accepted valid webhook run failed")
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("accepted webhook did not produce a completed durable run")
}
