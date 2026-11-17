package orchestrator

import (
 "slices"
 "testing"
)

// A process-global environment variable must never grant outer-isolation consent.
func TestCodexGlobalSandboxOverrideIsNotAdapterAuthority(t *testing.T) {
 t.Setenv("CREWSHIP_CODEX_SANDBOX_MODE", "danger-full-access")
 for _, profile := range []string{"CODING", "STANDARD", "MINIMAL"} {
  cmd := (codexAdapter{}).BuildCommand(AgentRunRequest{ToolProfile:profile})
  i:=slices.Index(cmd,"--sandbox")
  if i<0 || i+1>=len(cmd) { t.Fatal("missing sandbox mode") }
  want:="workspace-write"
  if profile=="MINIMAL" { want="read-only" }
  if cmd[i+1]!=want { t.Errorf("%s: global env granted %s, want %s without per-run admission",profile,cmd[i+1],want) }
 }
}
