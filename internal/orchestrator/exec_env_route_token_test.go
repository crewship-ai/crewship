package orchestrator

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func TestBindLLMRouteTokenCoversEveryReverseProxyAdapter(t *testing.T) {
	t.Parallel()

	const token = "llmrv1.YWdlbnQ.deadbeef"
	const fingerprint = "abcdef123456"
	env := []string{
		"ANTHROPIC_API_KEY=sk-ant-dummy-crewship-sidecar",
		"OPENAI_API_KEY=sk-dummy-crewship-sidecar",
		"GOOGLE_API_KEY=dummy-crewship-sidecar",
		"GEMINI_API_KEY=dummy-crewship-sidecar",
		`OPENCODE_CONFIG_CONTENT={"provider":{"openrouter":{"options":{"apiKey":"dummy-crewship-sidecar"}}}}`,
		"REAL_KEY=leave-me-alone",
	}

	got := bindLLMRouteToken(env, token, fingerprint)
	for _, name := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GOOGLE_API_KEY", "GEMINI_API_KEY", "OPENCODE_CONFIG_CONTENT"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			found := false
			for _, entry := range got {
				if strings.HasPrefix(entry, name+"=") {
					found = strings.Contains(entry, token+internaltoken.RouteFingerprintDelimiter+fingerprint)
					break
				}
			}
			if !found {
				t.Errorf("%s did not receive the route token: %v", name, got)
			}
		})
	}
	if got[len(got)-1] != "REAL_KEY=leave-me-alone" {
		t.Fatalf("non-dummy credential changed: %q", got[len(got)-1])
	}
}

func TestBindLLMRouteTokenEmptyIsByteIdentical(t *testing.T) {
	t.Parallel()

	const original = "OPENAI_API_KEY=sk-dummy-crewship-sidecar"
	env := []string{original}
	if got := bindLLMRouteToken(env, "", ""); got[0] != original {
		t.Fatalf("empty token changed legacy env: %q", got[0])
	}
}
