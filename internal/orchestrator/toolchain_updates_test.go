package orchestrator

import (
	"slices"
	"testing"
)

func TestManagedToolchainDisablesClaudeBackgroundUpdates(t *testing.T) {
	req := AgentRunRequest{CLIAdapter: "CLAUDE_CODE", AgentSlug: "managed", RunID: "run"}
	for name, env := range map[string][]string{"direct": BuildEnvVars(req, nil), "sidecar": BuildEnvVarsSidecar(req, false)} {
		t.Run(name, func(t *testing.T) {
			if !slices.Contains(env, "DISABLE_AUTOUPDATER=1") {
				t.Fatal("Claude must use the provisioned CLI without background updates")
			}
			if slices.Contains(env, "CLAUDE_CODE_DISABLE_AUTOUPDATE=1") {
				t.Fatal("unsupported update setting must not be emitted")
			}
		})
	}
}

func TestManagedToolchainDisablesCodexStartupUpdateCheck(t *testing.T) {
	cmd := (codexAdapter{}).BuildCommand(AgentRunRequest{})
	for i := 0; i+1 < len(cmd); i++ {
		if cmd[i] == "--config" && cmd[i+1] == "check_for_update_on_startup=false" {
			return
		}
	}
	t.Fatal("centrally managed Codex must disable startup update checks")
}
