package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/devcontainer"
)

func TestToolchainStatusDoesNotRenderRequestedSelectorAsInstalledVersion(t *testing.T) {
	status := &provisionToolchain{Requested: []devcontainer.ToolchainRequest{{Binary: "codex", Selector: "latest"}}}
	rows := toolchainDetailRows(status)
	if len(rows) != 1 || rows[0][1] != "not recorded" {
		t.Fatalf("unknown build=%v", rows)
	}
	status.Built = &devcontainer.ToolchainInventory{ImageID: "sha256:fixture", Tools: []devcontainer.ToolchainTool{{Binary: "codex", Version: "0.153.2", Status: "observed"}, {Binary: "claude", Status: "probe_failed"}}}
	rows = toolchainDetailRows(status)
	if rows[1][1] != "0.153.2" || !strings.HasPrefix(rows[2][1], "unknown") {
		t.Fatalf("built rows=%v", rows)
	}
}
