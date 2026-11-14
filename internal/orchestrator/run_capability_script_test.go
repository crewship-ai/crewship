package orchestrator

import (
	"strings"
	"testing"
)

// #5339: the host-minted capability of a run without a human reaches the crew
// sidecar on stdin through a heredoc, never argv, and nothing outside the two
// checked shapes can be spliced into the curl config.
func TestSidecarRunCapabilityScript(t *testing.T) {
	token := strings.Join([]string{"agtv2", "d3M", "YWdlbnQ", "cnVu", strings.Repeat("0a", 32)}, ".") // synthetic shape, no secret
	capability := "urcv1." + strings.Repeat("ab", 32)
	script := sidecarRunCapabilityScript(token, capability)
	for _, want := range []string{"/agent/run/artifact-capability", "-K /dev/fd/3", `header = "Authorization: Bearer ` + token + `"`, `header = "X-Crewship-Run-Capability: ` + capability + `"`, "|| true"} {
		if !strings.Contains(script, want) {
			t.Fatalf("script lacks %q:\n%s", want, script)
		}
	}
	if first := strings.SplitN(script, "\n", 2)[0]; strings.Contains(first, capability) || strings.Contains(first, token) {
		t.Fatal("a secret is on the curl command line")
	}
	for name, args := range map[string][2]string{
		"no capability":       {token, ""},
		"no token":            {"", capability},
		"v1 token":            {"agtv1.ws.agent." + strings.Repeat("0a", 32), capability},
		"injected capability": {token, capability + "\"\nurl = \"http://evil"},
		"injected token":      {token + "\"\nheader = \"x", capability},
		"short capability":    {token, "urcv1.abc"},
		"heredoc in token":    {"agtv2.a.b.c.0a\nRUNCAP_AUTH_HEREDOC", capability},
	} {
		if got := sidecarRunCapabilityScript(args[0], args[1]); got != "" {
			t.Fatalf("%s produced a script:\n%s", name, got)
		}
	}
}

func TestAgentRunTokenFromEnv(t *testing.T) {
	env := []string{"HOME=/x", "CREWSHIP_AGENT_TOKEN=agtv1.legacy", "CREWSHIP_AGENT_TOKEN=agtv2.run"}
	if got := agentRunTokenFromEnv(env); got != "agtv2.run" {
		t.Fatalf("got %q", got)
	}
	if got := agentRunTokenFromEnv([]string{"CREWSHIP_AGENT_TOKEN=agtv1.legacy"}); got != "" {
		t.Fatalf("v1 token used: %q", got)
	}
}
