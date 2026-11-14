package main

// #5337: every crew update of a bare-image devcontainer dropped the agent
// account that crew create injects (common-utils with agent at UID/GID 1001).
// The second `apply` of the manifest that created the crew planned an update
// against the stored, injected config and PATCHed the bare one back, and
// `crew config --devcontainer` stored a bare file as is. The next build then
// skipped the feature and the crew ran UID 1001 without an account.
//
// Driven through the BUILT BINARY against the REAL api router (the
// acceptance_credential_admin_test.go rig), with the commands an operator
// runs: apply twice, `crew config --devcontainer`, `crew config --export`.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const reapplySlimManifest = `apiVersion: crewship/v1
kind: Crew
metadata:
  name: Slim
  slug: slim
spec:
  devcontainer:
    image: debian:bookworm-slim
  agents:
    - slug: slim-lead
      name: Lead
      agent_role: LEAD
`

// exportedAgentAccount returns the common-utils account options of the
// crew's stored devcontainer config, read through `crew config --export`.
func exportedAgentAccount(t *testing.T, rig *credAdminRig, cfgJSON, crew string) map[string]any {
	t.Helper()
	stdout, combined, err := rig.exec(cfgJSON, "", "crew", "config", crew, "--export")
	if err != nil {
		t.Fatalf("crew config --export: %v\n%s", err, combined)
	}
	var out struct {
		Devcontainer struct {
			Features map[string]map[string]any `json:"features"`
		} `json:"devcontainer_config"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("export is not JSON: %v\n%s", err, stdout)
	}
	return out.Devcontainer.Features["ghcr.io/devcontainers/features/common-utils:2"]
}

func requireAgentAccount(t *testing.T, step string, opts map[string]any) {
	t.Helper()
	if opts["username"] != "agent" || opts["userUid"] != "1001" || opts["userGid"] != "1001" {
		t.Fatalf("%s: stored config has no agent account at UID/GID 1001 (common-utils options %v); "+
			"the next build would run UID 1001 without an account (#5337)", step, opts)
	}
}

func TestAcceptance_CrewReapplyKeepsAgentAccount(t *testing.T) {
	rig := startCredAdminServer(t)
	cfgJSON := rig.config(credAdminOwnerToken, "json")
	cfgTable := rig.config(credAdminOwnerToken, "table")
	path := writeManifest(t, "slim.yaml", reapplySlimManifest)

	if out, err := applyManifest(t, rig, cfgTable, path); err != nil {
		t.Fatalf("first apply: %v\n%s", err, out)
	}
	requireAgentAccount(t, "after create", exportedAgentAccount(t, rig, cfgJSON, "slim"))

	plan, err := rig.run(cfgTable, "", "apply", "--file", path, "--dry-run")
	if err != nil {
		t.Fatalf("dry-run re-apply: %v\n%s", err, plan)
	}
	for _, line := range strings.Split(plan, "\n") {
		if strings.Contains(line, "~") && strings.Contains(line, "crew") && strings.Contains(line, "slim") && !strings.Contains(line, "crew-file") {
			t.Fatalf("re-applying the manifest that created the crew plans an update:\n%s", plan)
		}
	}

	if out, err := applyManifest(t, rig, cfgTable, path); err != nil {
		t.Fatalf("second apply: %v\n%s", err, out)
	}
	requireAgentAccount(t, "after re-apply", exportedAgentAccount(t, rig, cfgJSON, "slim"))

	bare := filepath.Join(t.TempDir(), "devcontainer.json")
	if err := os.WriteFile(bare, []byte(`{"image":"debian:bookworm-slim"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := rig.run(cfgTable, "", "crew", "config", "slim", "--devcontainer", bare); err != nil {
		t.Fatalf("crew config --devcontainer: %v\n%s", err, out)
	}
	requireAgentAccount(t, "after crew config --devcontainer", exportedAgentAccount(t, rig, cfgJSON, "slim"))
}
