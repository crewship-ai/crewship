package main

import (
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

// TestWorkflowCmdStructure pins the registered subcommand set. `update`
// closed a CLI↔API parity gap (PATCH /api/v1/workflow-templates/{id} had
// no CLI verb); this guards against a future refactor dropping it.
func TestWorkflowCmdStructure(t *testing.T) {
	t.Parallel()

	have := map[string]bool{}
	for _, sub := range workflowCmd.Commands() {
		have[sub.Name()] = true
	}
	for _, want := range []string{"list", "get", "create", "update", "delete"} {
		if !have[want] {
			t.Errorf("workflow missing subcommand %q; have %v", want, have)
		}
	}
}

// TestWorkflowUpdate_Args pins the slug-argument contract — `update` takes
// exactly one <slug>, just like `get` and `delete`.
func TestWorkflowUpdate_Args(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{"zero args", []string{}, true},
		{"one arg", []string{"engineering-standard"}, false},
		{"two args", []string{"a", "b"}, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := workflowUpdateCmd.Args(workflowUpdateCmd, tc.args)
			if tc.wantErr && err == nil {
				t.Errorf("args=%v: expected error", tc.args)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("args=%v: expected no error, got %v", tc.args, err)
			}
		})
	}
}

// TestWorkflowUpdate_HasFileFlag confirms `update` carries the same
// --file flag as `create` and — like create — does NOT declare a `-f`
// shorthand (root --format owns -f; a child collision panics cobra).
func TestWorkflowUpdate_HasFileFlag(t *testing.T) {
	t.Parallel()

	f := workflowUpdateCmd.Flags().Lookup("file")
	if f == nil {
		t.Fatal("workflow update missing --file flag")
	}
	if f.Shorthand != "" {
		t.Errorf("--file must not declare a shorthand (collides with root --format -f); got -%s", f.Shorthand)
	}
}

// TestWorkflowUpdate_RequiresFile mirrors create's guard: without --file
// the command fails fast before touching the network.
func TestWorkflowUpdate_RequiresFile(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: "ws-1"}
	t.Cleanup(func() {
		_ = workflowUpdateCmd.Flags().Set("file", "")
	})

	err := workflowUpdateCmd.RunE(workflowUpdateCmd, []string{"engineering-standard"})
	if err == nil || !strings.Contains(err.Error(), "--file is required") {
		t.Errorf("expected --file required; got %v", err)
	}
}

// TestLoadWorkflowTemplatePatchBody verifies the manifest→PATCH-body
// conversion: every populated spec field maps to the JSON key the Update
// handler reads, and stages round-trip into the canonical template_json.
func TestLoadWorkflowTemplatePatchBody(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := dir + "/wf.yaml"
	manifest := `apiVersion: crewship/v1
kind: WorkflowTemplate
metadata:
  name: Engineering Standard
  slug: engineering-standard
spec:
  description: Updated lifecycle
  icon: ":rocket:"
  color: "#10B981"
  stages:
    - { name: backlog, type: open, position: 1 }
    - { name: done, type: completed, position: 2 }
`
	if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	body, err := loadWorkflowTemplateBody(path)
	if err != nil {
		t.Fatalf("loadWorkflowTemplateBody: %v", err)
	}

	if body["name"] != "Engineering Standard" {
		t.Errorf("name: got %v", body["name"])
	}
	if body["description"] != "Updated lifecycle" {
		t.Errorf("description: got %v", body["description"])
	}
	if body["icon"] != ":rocket:" {
		t.Errorf("icon: got %v", body["icon"])
	}
	if body["color"] != "#10B981" {
		t.Errorf("color: got %v", body["color"])
	}
	tj, ok := body["template_json"].(string)
	if !ok || !strings.Contains(tj, "backlog") || !strings.Contains(tj, "done") {
		t.Errorf("template_json missing stages: got %v", body["template_json"])
	}
}
