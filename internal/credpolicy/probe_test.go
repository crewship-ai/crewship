package credpolicy

import "testing"

func TestTokenProbePolicyIsFailClosed(t *testing.T) {
	for typ := range policies {
		for _, provider := range []string{"GITLAB", "ANTHROPIC", "OPENAI", "GOOGLE"} {
			want := typ == "API_KEY" || typ == "CLI_TOKEN" || (typ == "AI_CLI_TOKEN" && provider != "OPENAI" && provider != "GOOGLE")
			if TokenProbeAllowed(typ, provider) != want {
				t.Errorf("unexpected token probe permission: %s/%s", typ, provider)
			}
		}
	}
	if TokenProbeAllowed("UNKNOWN", "GITLAB") || TokenProbeAllowed("", "GITLAB") || TokenProbeAllowed("AI_CLI_TOKEN", "google") {
		t.Fatal("unknown type or structured login allowed into token probe")
	}
}
