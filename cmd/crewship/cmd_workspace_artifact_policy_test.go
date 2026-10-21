package main

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
)

func TestWorkspaceArtifactPolicyCLIRequiresExplicitReplacement(t *testing.T) {
	for _, args := range [][]string{
		{"set", "--enabled=true", "--crew-id", "crew-a"},
		{"set", "--base-version", "1", "--crew-id", "crew-a"},
		{"set", "--base-version", "1", "--enabled=false"},
		{"set", "--base-version", "1", "--enabled=true", "--crew-id", "crew-a", "--clear-crews"},
		{"set", "--base-version", "1", "--enabled=true", "--crew-id", "crew-a,crew-a"},
	} {
		command := newWorkspaceArtifactPolicyCommand()
		command.SetArgs(args)
		if err := command.Execute(); err == nil {
			t.Fatalf("accepted incomplete policy replacement: %v", args)
		}
	}
}

func TestWorkspaceArtifactPolicyCLIUsesCASWithoutAutomaticRetry(t *testing.T) {
	stub := pageStub(t)
	old := findSubcommand(workspaceCmd.Commands(), "artifact-policy")
	fresh := newWorkspaceArtifactPolicyCommand()
	workspaceCmd.RemoveCommand(old)
	workspaceCmd.AddCommand(fresh)
	t.Cleanup(func() { workspaceCmd.RemoveCommand(fresh); workspaceCmd.AddCommand(old) })
	endpoint := "/api/v1/workspaces/" + covWSCli3 + "/artifact-creation-policy"
	stub.On("PUT", endpoint, func(_ *http.Request, raw []byte) (int, []byte, string) {
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"enabled": false, "crew_ids": []any{}, "base_version": float64(3)}
		if !reflect.DeepEqual(body, want) {
			t.Fatalf("payload %v, want %v", body, want)
		}
		return 409, []byte(`{"error":"policy changed; review the current version"}`), "application/json"
	})
	if _, err := runPageCLI(t, "", "workspace", "artifact-policy", "set", "--base-version", "3", "--enabled=false", "--clear-crews"); err == nil {
		t.Fatal("conflict must be returned")
	}
	if len(stub.CallsFor("PUT", endpoint)) != 1 {
		t.Fatal("policy must not retry over a concurrent change")
	}
}
