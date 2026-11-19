package main

// #5334: `crewship apply` planned an update for every declared skill, even
// when the server already held exactly what importing it would write, so a
// steady-state apply never reported "no changes".
//
// Driven through the BUILT BINARY against the REAL api router (the
// acceptance_credential_admin_test.go rig): apply, then `apply --dry-run`
// twice, then change one byte and dry-run again.

import (
	"strings"
	"testing"
)

const skillNoopManifest = `apiVersion: crewship/v1
kind: Skill
metadata:
  name: Network Probe
  slug: noop-probe
spec:
  description: TCP/UDP probes against allow-listed hosts
  inline: |
    ---
    name: noop-probe
    description: Use when checking whether a host answers on a port.
    license: MIT
    ---
    # Network Probe
    Probe a host:port and report reachability.
`

// skillPlanLine returns the dry-run line that plans the skill.
func skillPlanLine(t *testing.T, plan string) string {
	t.Helper()
	for _, line := range strings.Split(plan, "\n") {
		if strings.Contains(line, "skill") && strings.Contains(line, "noop-probe") {
			return line
		}
	}
	t.Fatalf("no plan line for skill noop-probe:\n%s", plan)
	return ""
}

func TestAcceptance_ApplySkillUnchangedPlansNoop(t *testing.T) {
	rig := startCredAdminServer(t)
	cfg := rig.config(credAdminOwnerToken, "table")
	path := writeManifest(t, "skill.yaml", skillNoopManifest)

	if out, err := applyManifest(t, rig, cfg, path); err != nil {
		t.Fatalf("first apply: %v\n%s", err, out)
	}
	for i := 1; i <= 2; i++ {
		plan, err := rig.run(cfg, "", "apply", "--file", path, "--dry-run")
		if err != nil {
			t.Fatalf("dry-run %d: %v\n%s", i, err, plan)
		}
		if line := skillPlanLine(t, plan); !strings.Contains(line, "=") {
			t.Fatalf("dry-run %d plans a write for an unchanged skill: %q\n%s", i, line, plan)
		}
	}

	changed := writeManifest(t, "skill-changed.yaml", strings.Replace(skillNoopManifest, "report reachability.", "report reachability!", 1))
	plan, err := rig.run(cfg, "", "apply", "--file", changed, "--dry-run")
	if err != nil {
		t.Fatalf("dry-run after an edit: %v\n%s", err, plan)
	}
	if line := skillPlanLine(t, plan); !strings.Contains(line, "~") {
		t.Fatalf("a one-byte body change must plan an update: %q\n%s", line, plan)
	}
}
