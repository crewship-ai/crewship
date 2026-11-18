package api

import (
	"strings"
	"testing"
)

func TestKeeperBlockTeachesNativeCLIWithoutAuthAssembly(t *testing.T) {
	h := &InternalHandler{}
	block := h.buildKeeperBlock("fixture", []mcpCredEntry{{ID: "fixture", EnvVar: "GITLAB_TOKEN", Type: "SECRET", Value: "fixture-hidden-value"}})
	for _, command := range []string{"crewship keeper request", "crewship keeper execute", "crewship keeper request-status", "--command-stdin", "--approval-request-id"} {
		if !strings.Contains(block, command) {
			t.Errorf("Keeper prompt does not teach %q", command)
		}
	}
	for _, forbidden := range []string{"curl ", "Authorization: Bearer", "agent_slug", "fixture-hidden-value"} {
		if strings.Contains(block, forbidden) {
			t.Error("Keeper prompt requires manual transport/identity assembly or exposes credential material")
		}
	}
}
